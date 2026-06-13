package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"

	"github.com/yashikota/shito/internal/agent"
)

type Config struct {
	Command        []string
	CWD            string
	Model          string
	Effort         string
	ApprovalPolicy string
	SandboxPolicy  json.RawMessage
	ServiceName    string
}

type Agent struct {
	cfg   Config
	log   *slog.Logger
	cfgMu sync.RWMutex

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	cancel context.CancelFunc

	nextID atomic.Int64

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[int64]chan rpcResponse
	turns   map[string]chan agent.Event
	loaded  map[string]struct{}
	closed  bool
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
		return nil, errors.New("codex command is required")
	}
	if log == nil {
		log = slog.Default()
	}
	runCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(runCtx, cfg.Command[0], cfg.Command[1:]...)
	if cfg.CWD != "" {
		cmd.Dir = cfg.CWD
	}
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
		cfg:     cfg,
		log:     log,
		cmd:     cmd,
		stdin:   stdin,
		cancel:  cancel,
		pending: map[int64]chan rpcResponse{},
		turns:   map[string]chan agent.Event{},
		loaded:  map[string]struct{}{},
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	go a.readLoop(stdout)
	go a.logStderr(stderr)
	go func() {
		err := cmd.Wait()
		a.closeWithError(fmt.Errorf("codex app-server exited: %w", err))
	}()

	if err := a.initialize(ctx); err != nil {
		_ = a.Close()
		return nil, err
	}
	a.log.Info("codex app-server initialized")
	return a, nil
}

func (a *Agent) StartSession(ctx context.Context, req agent.StartSessionRequest) (agent.Session, error) {
	params := map[string]any{}
	a.applyThreadParams(params)
	if req.CWD != "" {
		params["cwd"] = req.CWD
	}
	result, err := a.request(ctx, "thread/start", params)
	if err != nil {
		return agent.Session{}, err
	}
	var decoded struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		return agent.Session{}, err
	}
	if decoded.Thread.ID == "" {
		return agent.Session{}, errors.New("thread/start returned empty thread id")
	}
	a.mu.Lock()
	a.loaded[decoded.Thread.ID] = struct{}{}
	a.mu.Unlock()
	return agent.Session{ID: decoded.Thread.ID}, nil
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
	params := map[string]any{"threadId": sessionID}
	a.applyThreadParams(params)
	result, err := a.request(ctx, "thread/resume", params)
	if err != nil {
		return agent.Session{}, err
	}
	var decoded struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		return agent.Session{}, err
	}
	if decoded.Thread.ID == "" {
		return agent.Session{}, errors.New("thread/resume returned empty thread id")
	}
	a.mu.Lock()
	a.loaded[decoded.Thread.ID] = struct{}{}
	a.mu.Unlock()
	return agent.Session{ID: decoded.Thread.ID}, nil
}

func (a *Agent) SendTurn(ctx context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	if req.SessionID == "" {
		return nil, errors.New("session id is required")
	}
	params := map[string]any{
		"threadId": req.SessionID,
		"input": []map[string]string{
			{"type": "text", "text": req.Input},
		},
	}
	result, err := a.request(ctx, "turn/start", params)
	if err != nil {
		return nil, err
	}
	var decoded struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		return nil, err
	}
	if decoded.Turn.ID == "" {
		return nil, errors.New("turn/start returned empty turn id")
	}
	ch := make(chan agent.Event, 64)
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		close(ch)
		return ch, nil
	}
	a.turns[decoded.Turn.ID] = ch
	a.mu.Unlock()
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

func (a *Agent) Interrupt(ctx context.Context, sessionID string) error {
	return errors.New("interrupt requires an active turn id and is not exposed by this adapter yet")
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
	for _, ch := range a.turns {
		close(ch)
	}
	a.mu.Unlock()
	return nil
}

func (a *Agent) initialize(ctx context.Context) error {
	params := map[string]any{
		"clientInfo": map[string]string{
			"name":    "shito",
			"title":   "shito",
			"version": "0.1.0",
		},
	}
	if _, err := a.request(ctx, "initialize", params); err != nil {
		return err
	}
	return a.notify("initialized", map[string]any{})
}

func (a *Agent) applyThreadParams(params map[string]any) {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()

	if a.cfg.Model != "" {
		params["model"] = a.cfg.Model
	}
	if a.cfg.CWD != "" {
		params["cwd"] = a.cfg.CWD
	}
	if a.cfg.Effort != "" {
		params["effort"] = a.cfg.Effort
	}
	if a.cfg.ApprovalPolicy != "" {
		params["approvalPolicy"] = a.cfg.ApprovalPolicy
	}
	if a.cfg.ServiceName != "" {
		params["serviceName"] = a.cfg.ServiceName
	}
	if len(a.cfg.SandboxPolicy) > 0 {
		var sandbox any
		if err := json.Unmarshal(a.cfg.SandboxPolicy, &sandbox); err == nil {
			params["sandboxPolicy"] = sandbox
		}
	}
}

func (a *Agent) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := a.nextID.Add(1)
	ch := make(chan rpcResponse, 1)
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil, errors.New("codex app-server is closed")
	}
	a.pending[id] = ch
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.pending, id)
		a.mu.Unlock()
	}()

	if err := a.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return nil, errors.New("codex app-server closed")
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("codex rpc %s failed: %s", method, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (a *Agent) notify(method string, params any) error {
	return a.write(map[string]any{"method": method, "params": params})
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
			a.log.Warn("failed to decode codex rpc message", "error", err)
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
	a.log.Warn("declining unsupported codex server request", "method", method)
	_ = a.write(map[string]any{
		"id": id,
		"error": map[string]any{
			"code":    -32601,
			"message": "shito does not implement Codex server request: " + method,
		},
	})
}

func (a *Agent) handleNotification(method string, params json.RawMessage) {
	switch method {
	case "item/agentMessage/delta":
		var p struct {
			TurnID string `json:"turnId"`
			Delta  string `json:"delta"`
			Text   string `json:"text"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			a.log.Warn("failed to decode agent delta", "error", err)
			return
		}
		delta := p.Delta
		if delta == "" {
			delta = p.Text
		}
		a.sendTurnEvent(p.TurnID, agent.Event{Type: agent.EventTextDelta, Text: delta})
	case "turn/completed":
		var p struct {
			Turn struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"turn"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			a.log.Warn("failed to decode turn completion", "error", err)
			return
		}
		if p.Turn.Status == "failed" {
			err := errors.New("turn failed")
			if p.Turn.Error != nil && p.Turn.Error.Message != "" {
				err = errors.New(p.Turn.Error.Message)
			}
			a.finishTurn(p.Turn.ID, agent.Event{Type: agent.EventFailed, Err: err})
			return
		}
		a.finishTurn(p.Turn.ID, agent.Event{Type: agent.EventCompleted})
	}
}

func (a *Agent) sendTurnEvent(turnID string, event agent.Event) {
	a.mu.Lock()
	ch := a.turns[turnID]
	a.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- event:
	default:
		a.log.Warn("dropping codex event because turn channel is full", "turn_id", turnID)
	}
}

func (a *Agent) finishTurn(turnID string, event agent.Event) {
	a.mu.Lock()
	ch := a.turns[turnID]
	delete(a.turns, turnID)
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
	for turnID, ch := range a.turns {
		ch <- agent.Event{Type: agent.EventFailed, Err: err}
		close(ch)
		delete(a.turns, turnID)
	}
	a.mu.Unlock()
}

func (a *Agent) logStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		a.log.Info("codex app-server", "stderr", scanner.Text())
	}
}
