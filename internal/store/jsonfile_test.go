package store

import "testing"

func TestKeyStringOmitsEmptyMessageID(t *testing.T) {
	key := ConversationKey{
		ChatProvider: "slack",
		TeamID:       "T123",
		ChannelID:    "C123",
		ThreadID:     "1700000000.000000",
	}

	got := keyString(key)
	want := "slack\x00T123\x00C123\x001700000000.000000"
	if got != want {
		t.Fatalf("keyString() = %q, want %q", got, want)
	}
}

func TestKeyStringIncludesMessageID(t *testing.T) {
	key := ConversationKey{
		ChatProvider: "slack",
		TeamID:       "T123",
		ChannelID:    "C123",
		ThreadID:     "1700000000.000000",
		MessageID:    "T123:C123:1700000000.111111",
	}

	got := keyString(key)
	want := "slack\x00T123\x00C123\x001700000000.000000\x00T123:C123:1700000000.111111"
	if got != want {
		t.Fatalf("keyString() = %q, want %q", got, want)
	}
}
