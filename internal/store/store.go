package store

import "context"

type ConversationKey struct {
	ChatProvider string `json:"chatProvider"`
	TeamID       string `json:"teamId"`
	ChannelID    string `json:"channelId"`
	ThreadID     string `json:"threadId"`
}

type Conversation struct {
	Key            ConversationKey `json:"key"`
	AgentSessionID string          `json:"agentSessionId"`
	CreatedAt      int64           `json:"createdAt"`
	UpdatedAt      int64           `json:"updatedAt"`
}

type Store interface {
	GetOrCreateConversation(context.Context, ConversationKey) (Conversation, error)
	UpdateAgentSession(context.Context, ConversationKey, string) error
	TryMarkEventProcessed(context.Context, string) (bool, error)
	Close() error
}
