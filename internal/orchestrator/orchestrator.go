package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/yashikota/shito/internal/agent"
	"github.com/yashikota/shito/internal/chat"
	"github.com/yashikota/shito/internal/store"
)

type Config struct {
	MaxConcurrent int
	InitialReply  string
	Lang          string
	Model         string
	Effort        string
	UpdateEvery   time.Duration
	CommandCWD    string
}

type Orchestrator struct {
	cfg        Config
	settingsMu sync.Mutex
	log        *slog.Logger
	chat       chat.Adapter
	agent      agent.Agent
	store      store.Store
	sem        chan struct{}
}

func New(cfg Config, log *slog.Logger, chatAdapter chat.Adapter, codingAgent agent.Agent, st store.Store) *Orchestrator {
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 1
	}
	if cfg.InitialReply == "" {
		cfg.InitialReply = "Working..."
	}
	if cfg.UpdateEvery <= 0 {
		cfg.UpdateEvery = 2 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &Orchestrator{
		cfg:   cfg,
		log:   log,
		chat:  chatAdapter,
		agent: codingAgent,
		store: st,
		sem:   make(chan struct{}, cfg.MaxConcurrent),
	}
}

func (o *Orchestrator) Run(ctx context.Context) error {
	return o.chat.Run(ctx, o.HandleMessage)
}

func (o *Orchestrator) HandleMessage(ctx context.Context, msg chat.InboundMessage) error {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return nil
	}
	if msg.ID == "" {
		return errors.New("message id is required")
	}
	accepted, err := o.store.TryMarkEventProcessed(ctx, msg.ID)
	if err != nil {
		return err
	}
	if !accepted {
		return nil
	}

	select {
	case o.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	go func() {
		defer func() { <-o.sem }()
		if err := o.processMessage(ctx, msg, text); err != nil {
			o.log.Error("failed to process message", "event_id", msg.ID, "error", err)
		}
	}()
	return nil
}

func (o *Orchestrator) processMessage(ctx context.Context, msg chat.InboundMessage, text string) error {
	if msg.IsSlashCommand {
		return o.processSlashCommand(ctx, msg, text)
	}

	if cmd, ok := extractCommandBlock(text); ok {
		return o.processCommand(ctx, msg, cmd)
	}

	key := store.ConversationKey{
		ChatProvider: msg.Provider,
		TeamID:       msg.TeamID,
		ChannelID:    msg.ChannelID,
		ThreadID:     msg.ThreadID,
	}
	conv, err := o.store.GetOrCreateConversation(ctx, key)
	if err != nil {
		return err
	}
	if conv.AgentSessionID == "" {
		session, err := o.agent.StartSession(ctx, agent.StartSessionRequest{})
		if err != nil {
			return err
		}
		conv.AgentSessionID = session.ID
		if err := o.store.UpdateAgentSession(ctx, key, session.ID); err != nil {
			return err
		}
	} else if _, err := o.agent.ResumeSession(ctx, conv.AgentSessionID); err != nil {
		return err
	}

	sent, err := o.chat.Send(ctx, chat.OutboundMessage{
		ChannelID: msg.ChannelID,
		ThreadID:  msg.ThreadID,
		Text:      o.cfg.InitialReply,
	})
	if err != nil {
		return err
	}

	events, err := o.agent.SendTurn(ctx, agent.TurnRequest{
		SessionID: conv.AgentSessionID,
		Input:     o.applyLang(text),
	})
	if err != nil {
		_ = o.chat.Update(ctx, chat.UpdateMessage{ChannelID: sent.ChannelID, Timestamp: sent.Timestamp, Text: fmt.Sprintf("Failed to start agent turn: %v", err)})
		return err
	}

	return o.streamToChat(ctx, sent, events)
}

func (o *Orchestrator) applyLang(text string) string {
	lang := strings.TrimSpace(o.cfg.Lang)
	if lang == "" {
		return text
	}
	return fmt.Sprintf("lang: %s\n\n%s", lang, text)
}

func (o *Orchestrator) processSlashCommand(ctx context.Context, msg chat.InboundMessage, text string) error {
	response := o.handleSettingsCommand(text)
	_, err := o.chat.Send(ctx, chat.OutboundMessage{
		ChannelID: msg.ChannelID,
		Text:      response,
	})
	return err
}

func (o *Orchestrator) handleSettingsCommand(text string) string {
	o.settingsMu.Lock()
	defer o.settingsMu.Unlock()

	fields := strings.Fields(text)
	if len(fields) == 0 || fields[0] == "help" {
		return "Usage: /shito model <model> | /shito effort <low|medium|high> | /shito status"
	}

	switch fields[0] {
	case "model":
		if len(fields) < 2 {
			return "Usage: /shito model <model>"
		}
		o.cfg.Model = fields[1]
		o.agent.SetRuntimeConfig(agent.RuntimeConfig{Model: o.cfg.Model})
		return fmt.Sprintf("model: %s", o.cfg.Model)
	case "effort":
		if len(fields) < 2 {
			return "Usage: /shito effort <low|medium|high>"
		}
		o.cfg.Effort = fields[1]
		o.agent.SetRuntimeConfig(agent.RuntimeConfig{Effort: o.cfg.Effort})
		return fmt.Sprintf("effort: %s", o.cfg.Effort)
	case "status":
		return fmt.Sprintf("model: %s\neffort: %s", valueOrUnset(o.cfg.Model), valueOrUnset(o.cfg.Effort))
	default:
		return "Usage: /shito model <model> | /shito effort <low|medium|high> | /shito status"
	}
}

func valueOrUnset(v string) string {
	if strings.TrimSpace(v) == "" {
		return "(unset)"
	}
	return v
}

func (o *Orchestrator) processCommand(ctx context.Context, msg chat.InboundMessage, command string) error {
	sent, err := o.chat.Send(ctx, chat.OutboundMessage{
		ChannelID: msg.ChannelID,
		ThreadID:  msg.ThreadID,
		Text:      "Running command...",
	})
	if err != nil {
		return err
	}

	output, err := o.runCommand(ctx, command)
	if err != nil {
		output = strings.TrimSpace(output + "\n" + err.Error())
	}
	if output == "" {
		output = "(no output)"
	}

	return o.chat.Update(ctx, chat.UpdateMessage{
		ChannelID: sent.ChannelID,
		Timestamp: sent.Timestamp,
		Text:      formatCommandOutput(output),
	})
}

func (o *Orchestrator) runCommand(ctx context.Context, command string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "/bin/sh", "-c", command)
	if o.cfg.CommandCWD != "" {
		cmd.Dir = o.cfg.CommandCWD
	}
	b, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(b))
	if runCtx.Err() == context.DeadlineExceeded {
		return output, errors.New("command timed out")
	}
	return output, err
}

func (o *Orchestrator) streamToChat(ctx context.Context, sent chat.SentMessage, events <-chan agent.Event) error {
	var (
		mu       sync.Mutex
		text     strings.Builder
		lastSent string
	)
	ticker := time.NewTicker(o.cfg.UpdateEvery)
	defer ticker.Stop()

	flush := func(force bool) error {
		mu.Lock()
		current := strings.TrimSpace(text.String())
		mu.Unlock()
		if current == "" || (!force && current == lastSent) {
			return nil
		}
		lastSent = current
		return o.chat.Update(ctx, chat.UpdateMessage{
			ChannelID: sent.ChannelID,
			Timestamp: sent.Timestamp,
			Text:      current,
		})
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := flush(false); err != nil {
				o.log.Warn("failed to update chat message", "error", err)
			}
		case event, ok := <-events:
			if !ok {
				return flush(true)
			}
			switch event.Type {
			case agent.EventTextDelta:
				mu.Lock()
				text.WriteString(event.Text)
				mu.Unlock()
			case agent.EventCompleted:
				if event.Text != "" {
					mu.Lock()
					text.WriteString(event.Text)
					mu.Unlock()
				}
				return flush(true)
			case agent.EventFailed:
				errText := "Agent turn failed"
				if event.Err != nil {
					errText = errText + ": " + event.Err.Error()
				}
				if err := o.chat.Update(ctx, chat.UpdateMessage{ChannelID: sent.ChannelID, Timestamp: sent.Timestamp, Text: errText}); err != nil {
					return err
				}
				return event.Err
			}
		}
	}
}

func extractCommandBlock(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	if strings.HasPrefix(text, "!") {
		cmd := strings.TrimSpace(strings.TrimPrefix(text, "!"))
		return cmd, cmd != ""
	}
	if strings.HasPrefix(text, "```") && strings.HasSuffix(text, "```") && len(text) >= 6 {
		cmd := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "```"), "```"))
		cmd = stripShellFenceLanguage(cmd)
		return cmd, cmd != ""
	}
	if strings.HasPrefix(text, "`") && strings.HasSuffix(text, "`") && len(text) >= 2 {
		cmd := strings.TrimSpace(strings.Trim(text, "`"))
		return cmd, cmd != ""
	}
	return "", false
}

func stripShellFenceLanguage(text string) string {
	first, rest, ok := strings.Cut(text, "\n")
	if !ok {
		return text
	}
	switch strings.ToLower(strings.TrimSpace(first)) {
	case "sh", "shell", "bash", "zsh", "console", "terminal":
		return strings.TrimSpace(rest)
	default:
		return text
	}
}

func formatCommandOutput(output string) string {
	const maxOutput = 3500
	output = strings.TrimSpace(output)
	if len(output) > maxOutput {
		output = output[:maxOutput] + "\n... truncated ..."
	}
	output = strings.ReplaceAll(output, "```", "'''")
	return "```\n" + output + "\n```"
}
