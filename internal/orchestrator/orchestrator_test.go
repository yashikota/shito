package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yashikota/shito/internal/agent"
	"github.com/yashikota/shito/internal/chat"
	"github.com/yashikota/shito/internal/store"
)

func TestHandleMessageStartsSessionAndUpdatesChat(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := &fakeChat{}
	ag := &fakeAgent{events: []agent.Event{
		{Type: agent.EventTextDelta, Text: "hello"},
		{Type: agent.EventTextDelta, Text: " world"},
		{Type: agent.EventCompleted},
	}}
	st := newMemoryStore()
	orch := New(Config{MaxConcurrent: 1, UpdateEvery: time.Millisecond}, nil, ch, ag, st)

	if err := orch.HandleMessage(ctx, chat.InboundMessage{
		ID:        "evt-1",
		Provider:  "slack",
		TeamID:    "T1",
		ChannelID: "C1",
		ThreadID:  "100.1",
		Text:      "do work",
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return len(ch.updates) > 0 && ch.updates[len(ch.updates)-1].Text == "hello world"
	})
	if ag.started != 1 {
		t.Fatalf("started sessions = %d, want 1", ag.started)
	}
}

func TestHandleMessageAddsLang(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := &fakeChat{}
	ag := &fakeAgent{events: []agent.Event{{Type: agent.EventCompleted, Text: "done"}}}
	st := newMemoryStore()
	orch := New(Config{MaxConcurrent: 1, UpdateEvery: time.Millisecond, Lang: "ja"}, nil, ch, ag, st)

	if err := orch.HandleMessage(ctx, chat.InboundMessage{
		ID:        "evt-1",
		Provider:  "slack",
		TeamID:    "T1",
		ChannelID: "C1",
		ThreadID:  "100.1",
		Text:      "do work",
	}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, ctx, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		return ag.lastInput != ""
	})
	if !strings.Contains(ag.lastInput, "lang: ja") {
		t.Fatalf("agent input = %q, want lang instruction", ag.lastInput)
	}
}

func TestHandleMessageDeduplicatesEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := &fakeChat{}
	ag := &fakeAgent{events: []agent.Event{{Type: agent.EventCompleted, Text: "done"}}}
	st := newMemoryStore()
	orch := New(Config{MaxConcurrent: 1, UpdateEvery: time.Millisecond}, nil, ch, ag, st)
	msg := chat.InboundMessage{ID: "evt-1", Provider: "slack", TeamID: "T1", ChannelID: "C1", ThreadID: "100.1", Text: "do work"}
	if err := orch.HandleMessage(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if err := orch.HandleMessage(ctx, msg); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return len(ch.updates) > 0
	})
	if ag.turns != 1 {
		t.Fatalf("turns = %d, want 1", ag.turns)
	}
}

func TestHandleMessageRunsCommandBlock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := &fakeChat{}
	ag := &fakeAgent{}
	st := newMemoryStore()
	orch := New(Config{MaxConcurrent: 1, UpdateEvery: time.Millisecond}, nil, ch, ag, st)

	if err := orch.HandleMessage(ctx, chat.InboundMessage{
		ID:        "evt-1",
		Provider:  "slack",
		TeamID:    "T1",
		ChannelID: "C1",
		ThreadID:  "100.1",
		Text:      "```sh\nprintf hello\n```",
	}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, ctx, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return len(ch.updates) > 0 && ch.updates[len(ch.updates)-1].Text == "```\nhello\n```"
	})
	if ag.started != 0 || ag.turns != 0 {
		t.Fatalf("agent calls = started %d turns %d, want none", ag.started, ag.turns)
	}
}

func TestHandleSlashCommandUpdatesModel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := &fakeChat{}
	ag := &fakeAgent{}
	st := newMemoryStore()
	orch := New(Config{MaxConcurrent: 1, UpdateEvery: time.Millisecond}, nil, ch, ag, st)

	if err := orch.HandleMessage(ctx, chat.InboundMessage{
		ID:             "cmd-1",
		Provider:       "slack",
		TeamID:         "T1",
		ChannelID:      "C1",
		UserID:         "U1",
		Text:           "model gpt-5.5",
		IsSlashCommand: true,
		SlashCommand:   "/shito",
	}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, ctx, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return len(ch.sends) > 0 && ch.sends[len(ch.sends)-1].Text == "model: gpt-5.5"
	})
	ag.mu.Lock()
	defer ag.mu.Unlock()
	if ag.runtimeConfig.Model != "gpt-5.5" {
		t.Fatalf("runtime model = %q, want gpt-5.5", ag.runtimeConfig.Model)
	}
	if ag.started != 0 || ag.turns != 0 {
		t.Fatalf("agent calls = started %d turns %d, want none", ag.started, ag.turns)
	}
}

func TestHandleSlashCommandUpdatesEffort(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := &fakeChat{}
	ag := &fakeAgent{}
	st := newMemoryStore()
	orch := New(Config{MaxConcurrent: 1, UpdateEvery: time.Millisecond}, nil, ch, ag, st)

	if err := orch.HandleMessage(ctx, chat.InboundMessage{
		ID:             "cmd-1",
		Provider:       "slack",
		TeamID:         "T1",
		ChannelID:      "C1",
		UserID:         "U1",
		Text:           "effort medium",
		IsSlashCommand: true,
		SlashCommand:   "/shito",
	}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, ctx, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return len(ch.sends) > 0 && ch.sends[len(ch.sends)-1].Text == "effort: medium"
	})
	ag.mu.Lock()
	defer ag.mu.Unlock()
	if ag.runtimeConfig.Effort != "medium" {
		t.Fatalf("runtime effort = %q, want medium", ag.runtimeConfig.Effort)
	}
}

func TestHandleSlashCommandDoesNotAcceptLongEffortAlias(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := &fakeChat{}
	ag := &fakeAgent{}
	st := newMemoryStore()
	orch := New(Config{MaxConcurrent: 1, UpdateEvery: time.Millisecond}, nil, ch, ag, st)

	if err := orch.HandleMessage(ctx, chat.InboundMessage{
		ID:             "cmd-1",
		Provider:       "slack",
		TeamID:         "T1",
		ChannelID:      "C1",
		UserID:         "U1",
		Text:           "model_reasoning_effort medium",
		IsSlashCommand: true,
		SlashCommand:   "/shito",
	}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, ctx, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return len(ch.sends) > 0 && strings.HasPrefix(ch.sends[len(ch.sends)-1].Text, "Usage:")
	})
	ag.mu.Lock()
	defer ag.mu.Unlock()
	if ag.runtimeConfig.Effort != "" {
		t.Fatalf("runtime effort = %q, want empty", ag.runtimeConfig.Effort)
	}
}

func TestHandleSlashCommandStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := &fakeChat{}
	ag := &fakeAgent{}
	st := newMemoryStore()
	orch := New(Config{MaxConcurrent: 1, UpdateEvery: time.Millisecond, Model: "gpt-5.5", Effort: "medium"}, nil, ch, ag, st)

	if err := orch.HandleMessage(ctx, chat.InboundMessage{
		ID:             "cmd-1",
		Provider:       "slack",
		TeamID:         "T1",
		ChannelID:      "C1",
		UserID:         "U1",
		Text:           "status",
		IsSlashCommand: true,
		SlashCommand:   "/shito",
	}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, ctx, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return len(ch.sends) > 0 && ch.sends[len(ch.sends)-1].Text == "model: gpt-5.5\neffort: medium"
	})
}

func TestHandleMessageRunsInlineCommandInConfiguredCWD(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "target-file"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	ch := &fakeChat{}
	ag := &fakeAgent{}
	st := newMemoryStore()
	orch := New(Config{MaxConcurrent: 1, UpdateEvery: time.Millisecond, CommandCWD: dir}, nil, ch, ag, st)

	if err := orch.HandleMessage(ctx, chat.InboundMessage{
		ID:        "evt-1",
		Provider:  "slack",
		TeamID:    "T1",
		ChannelID: "C1",
		ThreadID:  "100.1",
		Text:      "`ls`",
	}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, ctx, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return len(ch.updates) > 0 && ch.updates[len(ch.updates)-1].Text == "```\ntarget-file\n```"
	})
}

func TestExtractCommandBlock(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
		ok   bool
	}{
		{name: "fenced", text: "```\nls\n```", want: "ls", ok: true},
		{name: "fenced shell language", text: "```bash\nls\n```", want: "ls", ok: true},
		{name: "inline", text: "`ls`", want: "ls", ok: true},
		{name: "bang command", text: "! ls", want: "ls", ok: true},
		{name: "empty bang command", text: "!   ", ok: false},
		{name: "normal message", text: "please run `ls`", ok: false},
		{name: "empty fence", text: "```\n```", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := extractCommandBlock(tt.text)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("extractCommandBlock() = %q, %v; want %q, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

type fakeChat struct {
	mu      sync.Mutex
	sends   []chat.OutboundMessage
	updates []chat.UpdateMessage
}

func (f *fakeChat) Run(context.Context, chat.MessageHandler) error { return nil }

func (f *fakeChat) Send(_ context.Context, msg chat.OutboundMessage) (chat.SentMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, msg)
	return chat.SentMessage{ChannelID: msg.ChannelID, Timestamp: "reply-ts"}, nil
}

func (f *fakeChat) Update(_ context.Context, msg chat.UpdateMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, msg)
	return nil
}

type fakeAgent struct {
	mu            sync.Mutex
	started       int
	turns         int
	lastInput     string
	events        []agent.Event
	runtimeConfig agent.RuntimeConfig
}

func (f *fakeAgent) StartSession(context.Context, agent.StartSessionRequest) (agent.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started++
	return agent.Session{ID: "agent-thread-1"}, nil
}

func (f *fakeAgent) ResumeSession(context.Context, string) (agent.Session, error) {
	return agent.Session{ID: "agent-thread-1"}, nil
}

func (f *fakeAgent) SendTurn(_ context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	f.mu.Lock()
	f.turns++
	f.lastInput = req.Input
	events := append([]agent.Event(nil), f.events...)
	f.mu.Unlock()
	ch := make(chan agent.Event, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return ch, nil
}

func (f *fakeAgent) SetRuntimeConfig(cfg agent.RuntimeConfig) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cfg.Model != "" {
		f.runtimeConfig.Model = cfg.Model
	}
	if cfg.Effort != "" {
		f.runtimeConfig.Effort = cfg.Effort
	}
}

func (f *fakeAgent) Interrupt(context.Context, string) error { return nil }
func (f *fakeAgent) Close() error                            { return nil }

type memoryStore struct {
	mu        sync.Mutex
	conv      map[string]store.Conversation
	processed map[string]struct{}
}

func newMemoryStore() *memoryStore {
	return &memoryStore{conv: map[string]store.Conversation{}, processed: map[string]struct{}{}}
}

func (m *memoryStore) GetOrCreateConversation(_ context.Context, key store.ConversationKey) (store.Conversation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := key.ChannelID + "/" + key.ThreadID
	if c, ok := m.conv[id]; ok {
		return c, nil
	}
	c := store.Conversation{Key: key}
	m.conv[id] = c
	return c, nil
}

func (m *memoryStore) UpdateAgentSession(_ context.Context, key store.ConversationKey, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := key.ChannelID + "/" + key.ThreadID
	c, ok := m.conv[id]
	if !ok {
		return errors.New("missing conversation")
	}
	c.AgentSessionID = sessionID
	m.conv[id] = c
	return nil
}

func (m *memoryStore) TryMarkEventProcessed(_ context.Context, eventID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.processed[eventID]; ok {
		return false, nil
	}
	m.processed[eventID] = struct{}{}
	return true, nil
}

func (m *memoryStore) Close() error { return nil }

func waitFor(t *testing.T, ctx context.Context, ok func() bool) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ok() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for condition")
		case <-ticker.C:
		}
	}
}
