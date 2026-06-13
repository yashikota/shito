package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"

	"github.com/yashikota/shito/internal/agent"
)

type Config struct {
	Command []string
	CWD     string
	Model   string
	Effort  string
	Version string
}

type Agent struct {
	cfg   Config
	log   *slog.Logger
	cfgMu sync.RWMutex

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	cancel context.CancelFunc

	nextID atomic.Int64

	writeMu  sync.Mutex
	mu       sync.Mutex
	pending  map[int64]chan rpcResponse
	sessions map[string]chan agent.Event
	loaded   map[string]struct{}
	closed   bool
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func New(ctx context.Context, cfg Config, log *slog.Logger) (*Agent, error) {
	if len(cfg.Command) == 0 {
		return nil, errors.New("agent command is required")
	}
	if cfg.Version == "" {
		cfg.Version = "0.1.0"
	}
	if log == nil {
		log = slog.Default()
	}
	runCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(runCtx, cfg.Command[0], cfg.Command[1:]...)
	if cfg.CWD != "" {
		cmd.Dir = cfg.CWD
	}
	cmd.Env = buildEnv(cfg)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}

	a := &Agent{
		cfg:      cfg,
		log:      log,
		cmd:      cmd,
		stdin:    stdin,
		cancel:   cancel,
		pending:  map[int64]chan rpcResponse{},
		sessions: map[string]chan agent.Event{},
		loaded:   map[string]struct{}{},
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	go a.readLoop(stdout)
	go a.logStderr(stderr)
	go func() {
		err := cmd.Wait()
		a.closeWithError(fmt.Errorf("agent process exited: %w", err))
	}()

	if err := a.initialize(ctx); err != nil {
		_ = a.Close()
		return nil, err
	}
	a.log.Info("acp agent initialized")
	return a, nil
}

func buildEnv(cfg Config) []string {
	env := os.Environ()
	if cfg.Model != "" {
		env = append(env, "CODEX_MODEL="+cfg.Model)
	}
	if cfg.Effort != "" {
		env = append(env, "CODEX_EFFORT="+cfg.Effort)
	}
	return env
}

func (a *Agent) StartSession(ctx context.Context, req agent.StartSessionRequest) (agent.Session, error) {
	params := map[string]any{}
	cwd := req.CWD
	if cwd == "" {
		a.cfgMu.RLock()
		cwd = a.cfg.CWD
		a.cfgMu.RUnlock()
	}
	if cwd != "" {
		params["cwd"] = cwd
	}
	result, err := a.request(ctx, "session/new", params)
	if err != nil {
		return agent.Session{}, err
	}
	var decoded struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		return agent.Session{}, err
	}
	if decoded.SessionID == "" {
		return agent.Session{}, errors.New("session/new returned empty session id")
	}
	a.mu.Lock()
	a.loaded[decoded.SessionID] = struct{}{}
	a.mu.Unlock()
	return agent.Session{ID: decoded.SessionID}, nil
}

func (a *Agent) ResumeSession(ctx context.Context, sessionID string) (agent.Session, error) {
	if sessionID == "" {
		return agent.Session{}, errors.New("session id is required")
	}
	a.mu.Lock()
	_, loaded := a.loaded[sessionID]
	a.mu.Unlock()
	if loaded {
		return agent.Session{ID: sessionID}, nil
	}
	params := map[string]any{"sessionId": sessionID}
	a.cfgMu.RLock()
	cwd := a.cfg.CWD
	a.cfgMu.RUnlock()
	if cwd != "" {
		params["cwd"] = cwd
	}
	if _, err := a.request(ctx, "session/resume", params); err != nil {
		return agent.Session{}, err
	}
	a.mu.Lock()
	a.loaded[sessionID] = struct{}{}
	a.mu.Unlock()
	return agent.Session{ID: sessionID}, nil
}

func (a *Agent) SendTurn(ctx context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	if req.SessionID == "" {
		return nil, errors.New("session id is required")
	}
	ch := make(chan agent.Event, 64)
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		close(ch)
		return ch, nil
	}
	a.sessions[req.SessionID] = ch
	a.mu.Unlock()

	params := map[string]any{
		"sessionId": req.SessionID,
		"prompt": []map[string]string{
			{"type": "text", "text": req.Input},
		},
	}

	go func() {
		result, err := a.request(ctx, "session/prompt", params)
		if err != nil {
			a.finishSession(req.SessionID, agent.Event{Type: agent.EventFailed, Err: err})
			return
		}
		var decoded struct {
			StopReason string `json:"stopReason"`
		}
		if err := json.Unmarshal(result, &decoded); err != nil {
			a.finishSession(req.SessionID, agent.Event{Type: agent.EventFailed, Err: err})
			return
		}
		switch decoded.StopReason {
		case "end_turn":
			a.finishSession(req.SessionID, agent.Event{Type: agent.EventCompleted})
		default:
			a.finishSession(req.SessionID, agent.Event{
				Type: agent.EventFailed,
				Err:  fmt.Errorf("stop reason: %s", decoded.StopReason),
			})
		}
	}()
	return ch, nil
}

func (a *Agent) SetRuntimeConfig(cfg agent.RuntimeConfig) {
	a.cfgMu.Lock()
	if cfg.Model != "" {
		a.cfg.Model = cfg.Model
	}
	if cfg.Effort != "" {
		a.cfg.Effort = cfg.Effort
	}
	a.cfgMu.Unlock()

	a.mu.Lock()
	a.loaded = map[string]struct{}{}
	a.mu.Unlock()
}

func (a *Agent) Interrupt(_ context.Context, sessionID string) error {
	if sessionID == "" {
		return errors.New("session id is required")
	}
	return a.notify("session/cancel", map[string]any{"sessionId": sessionID})
}

func (a *Agent) Close() error {
	a.cancel()
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	for _, ch := range a.pending {
		close(ch)
	}
	for _, ch := range a.sessions {
		close(ch)
	}
	a.mu.Unlock()
	return nil
}

func (a *Agent) initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"terminal": true,
		},
		"clientInfo": map[string]string{
			"name":    "shito",
			"title":   "shito",
			"version": a.cfg.Version,
		},
	}
	_, err := a.request(ctx, "initialize", params)
	return err
}

func (a *Agent) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := a.nextID.Add(1)
	ch := make(chan rpcResponse, 1)
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil, errors.New("agent process is closed")
	}
	a.pending[id] = ch
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.pending, id)
		a.mu.Unlock()
	}()

	if err := a.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return nil, errors.New("agent process closed")
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("acp rpc %s failed: %s", method, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (a *Agent) notify(method string, params any) error {
	return a.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (a *Agent) write(msg any) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = a.stdin.Write(b)
	return err
}

func (a *Agent) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var msg struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			a.log.Warn("failed to decode acp message", "error", err)
			continue
		}
		if msg.ID != nil && msg.Method != "" {
			a.handleServerRequest(*msg.ID, msg.Method)
			continue
		}
		if msg.ID != nil {
			a.mu.Lock()
			ch := a.pending[*msg.ID]
			a.mu.Unlock()
			if ch != nil {
				ch <- rpcResponse{Result: msg.Result, Error: msg.Error}
			}
			continue
		}
		a.handleNotification(msg.Method, msg.Params)
	}
	if err := scanner.Err(); err != nil {
		a.closeWithError(err)
	}
}

func (a *Agent) handleServerRequest(id int64, method string) {
	a.log.Warn("declining unsupported agent server request", "method", method)
	_ = a.write(map[string]any{
		"jsonrpc": "2.0",
		"id":     id,
		"error": map[string]any{
			"code":    -32601,
			"message": "method not supported: " + method,
		},
	})
}

func (a *Agent) handleNotification(method string, params json.RawMessage) {
	switch method {
	case "session/update":
		var p struct {
			SessionID string `json:"sessionId"`
			Update    struct {
				SessionUpdate string `json:"sessionUpdate"`
				Content       struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			a.log.Warn("failed to decode session/update", "error", err)
			return
		}
		if p.Update.SessionUpdate == "agent_message_chunk" && p.Update.Content.Text != "" {
			a.sendSessionEvent(p.SessionID, agent.Event{Type: agent.EventTextDelta, Text: p.Update.Content.Text})
		}
	}
}

func (a *Agent) sendSessionEvent(sessionID string, event agent.Event) {
	a.mu.Lock()
	ch := a.sessions[sessionID]
	a.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- event:
	default:
		a.log.Warn("dropping event because session channel is full", "session_id", sessionID)
	}
}

func (a *Agent) finishSession(sessionID string, event agent.Event) {
	a.mu.Lock()
	ch := a.sessions[sessionID]
	delete(a.sessions, sessionID)
	a.mu.Unlock()
	if ch == nil {
		return
	}
	ch <- event
	close(ch)
}

func (a *Agent) closeWithError(err error) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	for _, ch := range a.pending {
		close(ch)
	}
	for sessionID, ch := range a.sessions {
		ch <- agent.Event{Type: agent.EventFailed, Err: err}
		close(ch)
		delete(a.sessions, sessionID)
	}
	a.mu.Unlock()
}

func (a *Agent) logStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		a.log.Info("agent", "stderr", scanner.Text())
	}
}
