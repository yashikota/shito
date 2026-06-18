package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type JSONFileStore struct {
	mu   sync.Mutex
	path string
	data fileData
}

type fileData struct {
	Conversations   map[string]Conversation `json:"conversations"`
	ProcessedEvents map[string]int64        `json:"processedEvents"`
}

func NewJSONFileStore(path string) (*JSONFileStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("store path is required")
	}
	s := &JSONFileStore{path: path}
	s.data.Conversations = map[string]Conversation{}
	s.data.ProcessedEvents = map[string]int64{}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *JSONFileStore) GetOrCreateConversation(ctx context.Context, key ConversationKey) (Conversation, error) {
	if err := ctx.Err(); err != nil {
		return Conversation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	id := keyString(key)
	if conv, ok := s.data.Conversations[id]; ok {
		return conv, nil
	}
	now := time.Now().Unix()
	conv := Conversation{Key: key, CreatedAt: now, UpdatedAt: now}
	s.data.Conversations[id] = conv
	if err := s.saveLocked(); err != nil {
		return Conversation{}, err
	}
	return conv, nil
}

func (s *JSONFileStore) UpdateAgentSession(ctx context.Context, key ConversationKey, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	id := keyString(key)
	conv, ok := s.data.Conversations[id]
	if !ok {
		return fmt.Errorf("conversation not found: %s", id)
	}
	conv.AgentSessionID = sessionID
	conv.UpdatedAt = time.Now().Unix()
	s.data.Conversations[id] = conv
	return s.saveLocked()
}

func (s *JSONFileStore) TryMarkEventProcessed(ctx context.Context, eventID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return false, errors.New("event id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.ProcessedEvents[eventID]; ok {
		return false, nil
	}
	s.data.ProcessedEvents[eventID] = time.Now().Unix()
	if err := s.saveLocked(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *JSONFileStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *JSONFileStore) load() error {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return err
	}
	if s.data.Conversations == nil {
		s.data.Conversations = map[string]Conversation{}
	}
	if s.data.ProcessedEvents == nil {
		s.data.ProcessedEvents = map[string]int64{}
	}
	return nil
}

func (s *JSONFileStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func keyString(key ConversationKey) string {
	parts := []string{key.ChatProvider, key.TeamID, key.ChannelID, key.ThreadID}
	if key.MessageID != "" {
		parts = append(parts, key.MessageID)
	}
	return strings.Join(parts, "\x00")
}
