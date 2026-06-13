package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/yashikota/shito/internal/chat"
)

type Config struct {
	AppToken   string
	BotToken   string
	ChannelIDs []string
}

type Adapter struct {
	cfg      Config
	log      *slog.Logger
	client   *http.Client
	channels map[string]struct{}
}

func New(cfg Config, log *slog.Logger) (*Adapter, error) {
	if cfg.AppToken == "" {
		return nil, errors.New("slack app token is required")
	}
	if cfg.BotToken == "" {
		return nil, errors.New("slack bot token is required")
	}
	if len(cfg.ChannelIDs) == 0 {
		return nil, errors.New("at least one slack channel id is required")
	}
	if log == nil {
		log = slog.Default()
	}
	channels := make(map[string]struct{}, len(cfg.ChannelIDs))
	for _, ch := range cfg.ChannelIDs {
		ch = strings.TrimSpace(ch)
		if ch != "" {
			channels[ch] = struct{}{}
		}
	}
	return &Adapter{
		cfg:      cfg,
		log:      log,
		client:   &http.Client{Timeout: 30 * time.Second},
		channels: channels,
	}, nil
}

func (a *Adapter) Run(ctx context.Context, handler chat.MessageHandler) error {
	backoff := time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := a.runOnce(ctx, handler)
		if err == nil || errors.Is(err, context.Canceled) {
			return err
		}
		a.log.Warn("slack socket disconnected", "error", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (a *Adapter) Send(ctx context.Context, msg chat.OutboundMessage) (chat.SentMessage, error) {
	var resp struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	err := a.webAPI(ctx, "chat.postMessage", map[string]any{
		"channel":   msg.ChannelID,
		"thread_ts": msg.ThreadID,
		"text":      msg.Text,
	}, &resp)
	if err != nil {
		return chat.SentMessage{}, err
	}
	if !resp.OK {
		return chat.SentMessage{}, fmt.Errorf("slack chat.postMessage failed: %s", resp.Error)
	}
	return chat.SentMessage{ChannelID: resp.Channel, Timestamp: resp.TS}, nil
}

func (a *Adapter) Update(ctx context.Context, msg chat.UpdateMessage) error {
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	err := a.webAPI(ctx, "chat.update", map[string]any{
		"channel": msg.ChannelID,
		"ts":      msg.Timestamp,
		"text":    msg.Text,
	}, &resp)
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("slack chat.update failed: %s", resp.Error)
	}
	return nil
}

func (a *Adapter) runOnce(ctx context.Context, handler chat.MessageHandler) error {
	socketURL, err := a.openSocket(ctx)
	if err != nil {
		return err
	}
	ws, err := dialWebSocket(ctx, socketURL, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = ws.Close()
	}()
	go func() {
		<-ctx.Done()
		_ = ws.Close()
	}()
	a.log.Info("slack socket connected")

	for {
		payload, err := ws.ReadMessage(ctx)
		if err != nil {
			return err
		}
		if err := a.handleSocketMessage(ctx, ws, payload, handler); err != nil {
			a.log.Warn("failed to handle slack socket message", "error", err)
		}
	}
}

func (a *Adapter) openSocket(ctx context.Context) (string, error) {
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		URL   string `json:"url"`
	}
	if err := a.webAPIWithToken(ctx, a.cfg.AppToken, "apps.connections.open", nil, &resp); err != nil {
		return "", err
	}
	if !resp.OK {
		return "", fmt.Errorf("slack apps.connections.open failed: %s", resp.Error)
	}
	if resp.URL == "" {
		return "", errors.New("slack apps.connections.open returned empty url")
	}
	return resp.URL, nil
}

func (a *Adapter) handleSocketMessage(ctx context.Context, ws *webSocketConn, payload []byte, handler chat.MessageHandler) error {
	var envelope struct {
		Type       string          `json:"type"`
		EnvelopeID string          `json:"envelope_id"`
		Payload    json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	switch envelope.Type {
	case "hello":
		return nil
	case "disconnect":
		return io.EOF
	case "events_api":
		if envelope.EnvelopeID != "" {
			if err := ws.WriteJSON(ctx, map[string]any{"envelope_id": envelope.EnvelopeID, "payload": map[string]any{}}); err != nil {
				return err
			}
		}
		msg, ok, err := a.toInboundMessage(envelope.Payload, envelope.EnvelopeID)
		if err != nil || !ok {
			return err
		}
		return handler(ctx, msg)
	case "slash_commands":
		if envelope.EnvelopeID != "" {
			if err := ws.WriteJSON(ctx, map[string]any{"envelope_id": envelope.EnvelopeID, "payload": map[string]any{}}); err != nil {
				return err
			}
		}
		msg, ok, err := a.toInboundSlashCommand(envelope.Payload, envelope.EnvelopeID)
		if err != nil || !ok {
			return err
		}
		return handler(ctx, msg)
	default:
		return nil
	}
}

func (a *Adapter) toInboundMessage(payload json.RawMessage, fallbackID string) (chat.InboundMessage, bool, error) {
	var p struct {
		EventID string `json:"event_id"`
		TeamID  string `json:"team_id"`
		Event   struct {
			Type        string `json:"type"`
			Subtype     string `json:"subtype"`
			Channel     string `json:"channel"`
			User        string `json:"user"`
			Text        string `json:"text"`
			TS          string `json:"ts"`
			ThreadTS    string `json:"thread_ts"`
			BotID       string `json:"bot_id"`
			ChannelType string `json:"channel_type"`
		} `json:"event"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return chat.InboundMessage{}, false, err
	}
	if p.EventID == "" {
		p.EventID = fallbackID
	}
	if _, ok := a.channels[p.Event.Channel]; !ok {
		return chat.InboundMessage{}, false, nil
	}
	if p.Event.BotID != "" || p.Event.Subtype != "" {
		return chat.InboundMessage{}, false, nil
	}
	if p.Event.Type != "message" && p.Event.Type != "app_mention" {
		return chat.InboundMessage{}, false, nil
	}
	threadID := p.Event.ThreadTS
	if threadID == "" {
		threadID = p.Event.TS
	}
	return chat.InboundMessage{
		ID:        p.EventID,
		Provider:  "slack",
		TeamID:    p.TeamID,
		ChannelID: p.Event.Channel,
		ThreadID:  threadID,
		UserID:    p.Event.User,
		Text:      p.Event.Text,
		Timestamp: p.Event.TS,
	}, true, nil
}

func (a *Adapter) toInboundSlashCommand(payload json.RawMessage, fallbackID string) (chat.InboundMessage, bool, error) {
	var p struct {
		TeamID    string `json:"team_id"`
		ChannelID string `json:"channel_id"`
		UserID    string `json:"user_id"`
		Command   string `json:"command"`
		Text      string `json:"text"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return chat.InboundMessage{}, false, err
	}
	if _, ok := a.channels[p.ChannelID]; !ok {
		return chat.InboundMessage{}, false, nil
	}
	return chat.InboundMessage{
		ID:             fallbackID,
		Provider:       "slack",
		TeamID:         p.TeamID,
		ChannelID:      p.ChannelID,
		UserID:         p.UserID,
		Text:           p.Text,
		IsSlashCommand: true,
		SlashCommand:   p.Command,
	}, true, nil
}

func (a *Adapter) webAPI(ctx context.Context, method string, body any, out any) error {
	return a.webAPIWithToken(ctx, a.cfg.BotToken, method, body, out)
}

func (a *Adapter) webAPIWithToken(ctx context.Context, token, method string, body any, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/"+method, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("slack %s returned HTTP %d", method, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
