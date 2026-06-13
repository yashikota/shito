package chat

import "context"

type MessageHandler func(context.Context, InboundMessage) error

type InboundMessage struct {
	ID             string
	Provider       string
	TeamID         string
	ChannelID      string
	ThreadID       string
	UserID         string
	Text           string
	Timestamp      string
	IsSlashCommand bool
	SlashCommand   string
}

type OutboundMessage struct {
	ChannelID string
	ThreadID  string
	Text      string
}

type SentMessage struct {
	ChannelID string
	Timestamp string
}

type UpdateMessage struct {
	ChannelID string
	Timestamp string
	Text      string
}

type Adapter interface {
	Run(context.Context, MessageHandler) error
	Send(context.Context, OutboundMessage) (SentMessage, error)
	Update(context.Context, UpdateMessage) error
}
