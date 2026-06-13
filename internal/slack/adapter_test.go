package slack

import (
	"encoding/json"
	"log/slog"
	"testing"
)

func TestToInboundMessageUsesSlackMessageID(t *testing.T) {
	adapter, err := New(Config{
		AppToken:   "xapp-test",
		BotToken:   "xoxb-test",
		ChannelIDs: []string{"C1"},
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	messagePayload := slackEventPayload(t, "Ev-message", "message", "C1", "1700000000.000100")
	mentionPayload := slackEventPayload(t, "Ev-mention", "app_mention", "C1", "1700000000.000100")

	message, ok, err := adapter.toInboundMessage(messagePayload, "env-1")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("message event was not accepted")
	}
	mention, ok, err := adapter.toInboundMessage(mentionPayload, "env-2")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("app_mention event was not accepted")
	}

	if message.ID != "slack:T1:C1:1700000000.000100" {
		t.Fatalf("message ID = %q, want slack message ID", message.ID)
	}
	if mention.ID != message.ID {
		t.Fatalf("mention ID = %q, want same ID as message event %q", mention.ID, message.ID)
	}
}

func TestToInboundMessageFallsBackToEventIDWithoutTimestamp(t *testing.T) {
	adapter, err := New(Config{
		AppToken:   "xapp-test",
		BotToken:   "xoxb-test",
		ChannelIDs: []string{"C1"},
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	payload := slackEventPayload(t, "Ev-message", "message", "C1", "")
	msg, ok, err := adapter.toInboundMessage(payload, "env-1")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("message event was not accepted")
	}
	if msg.ID != "Ev-message" {
		t.Fatalf("message ID = %q, want event ID fallback", msg.ID)
	}
}

func TestToInboundSlashCommand(t *testing.T) {
	adapter, err := New(Config{
		AppToken:   "xapp-test",
		BotToken:   "xoxb-test",
		ChannelIDs: []string{"C1"},
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(map[string]string{
		"team_id":    "T1",
		"channel_id": "C1",
		"user_id":    "U1",
		"command":    "/shito",
		"text":       "model gpt-5.5",
	})
	if err != nil {
		t.Fatal(err)
	}

	msg, ok, err := adapter.toInboundSlashCommand(payload, "env-1")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("slash command was not accepted")
	}
	if !msg.IsSlashCommand {
		t.Fatal("message was not marked as slash command")
	}
	if msg.ID != "env-1" || msg.Provider != "slack" || msg.TeamID != "T1" || msg.ChannelID != "C1" || msg.UserID != "U1" {
		t.Fatalf("message metadata = %#v", msg)
	}
	if msg.SlashCommand != "/shito" || msg.Text != "model gpt-5.5" {
		t.Fatalf("slash command = %q text = %q", msg.SlashCommand, msg.Text)
	}
}

func TestToInboundSlashCommandIgnoresUnconfiguredChannel(t *testing.T) {
	adapter, err := New(Config{
		AppToken:   "xapp-test",
		BotToken:   "xoxb-test",
		ChannelIDs: []string{"C1"},
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(map[string]string{
		"team_id":    "T1",
		"channel_id": "C2",
		"user_id":    "U1",
		"command":    "/shito",
		"text":       "model gpt-5.5",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, ok, err := adapter.toInboundSlashCommand(payload, "env-1")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("slash command from unconfigured channel was accepted")
	}
}

func slackEventPayload(t *testing.T, eventID, eventType, channel, timestamp string) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"event_id": eventID,
		"team_id":  "T1",
		"event": map[string]string{
			"type":    eventType,
			"channel": channel,
			"user":    "U1",
			"text":    "hi",
			"ts":      timestamp,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
