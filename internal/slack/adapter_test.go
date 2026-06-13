package slack

import (
	"encoding/json"
	"log/slog"
	"testing"
)

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
