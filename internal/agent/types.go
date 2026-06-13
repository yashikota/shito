package agent

import "context"

type Session struct {
	ID string
}

type StartSessionRequest struct {
	CWD string
}

type TurnRequest struct {
	SessionID string
	Input     string
}

type RuntimeConfig struct {
	Model  string
	Effort string
}

type EventType string

const (
	EventTextDelta EventType = "text_delta"
	EventCompleted EventType = "completed"
	EventFailed    EventType = "failed"
)

type Event struct {
	Type EventType
	Text string
	Err  error
}

type Agent interface {
	StartSession(context.Context, StartSessionRequest) (Session, error)
	ResumeSession(context.Context, string) (Session, error)
	SendTurn(context.Context, TurnRequest) (<-chan Event, error)
	SetRuntimeConfig(RuntimeConfig)
	Interrupt(context.Context, string) error
	Close() error
}
