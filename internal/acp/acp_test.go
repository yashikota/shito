package acp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/yashikota/shito/internal/agent"
)

func TestNew_InitializesViaACP(t *testing.T) {
	helper := buildHelper(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	a, err := New(ctx, Config{
		Command: []string{helper},
		CWD:     t.TempDir(),
		Model:   "test-model",
		Effort:  "high",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
}

func TestStartSession(t *testing.T) {
	helper := buildHelper(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	a, err := New(ctx, Config{
		Command: []string{helper},
		CWD:     "/tmp",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()

	sess, err := a.StartSession(ctx, agent.StartSessionRequest{CWD: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID == "" {
		t.Fatal("expected non-empty session ID")
	}
}

func TestSendTurn_StreamsAndCompletes(t *testing.T) {
	helper := buildHelper(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	a, err := New(ctx, Config{
		Command: []string{helper},
		CWD:     "/tmp",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()

	sess, err := a.StartSession(ctx, agent.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}

	events, err := a.SendTurn(ctx, agent.TurnRequest{
		SessionID: sess.ID,
		Input:     "hello",
	})
	if err != nil {
		t.Fatal(err)
	}

	var text strings.Builder
	var completed bool
	for ev := range events {
		switch ev.Type {
		case agent.EventTextDelta:
			text.WriteString(ev.Text)
		case agent.EventCompleted:
			completed = true
		case agent.EventFailed:
			t.Fatalf("unexpected failure: %v", ev.Err)
		}
	}
	if !completed {
		t.Fatal("expected EventCompleted")
	}
	if text.String() != "Hello from ACP!" {
		t.Fatalf("text = %q, want %q", text.String(), "Hello from ACP!")
	}
}

func TestResumeSession_Loaded(t *testing.T) {
	helper := buildHelper(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	a, err := New(ctx, Config{
		Command: []string{helper},
		CWD:     "/tmp",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()

	sess, err := a.StartSession(ctx, agent.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}

	resumed, err := a.ResumeSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ID != sess.ID {
		t.Fatalf("resumed session id = %q, want %q", resumed.ID, sess.ID)
	}
}

func TestInterrupt(t *testing.T) {
	helper := buildHelper(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	a, err := New(ctx, Config{
		Command: []string{helper},
		CWD:     "/tmp",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()

	err = a.Interrupt(ctx, "sess_test123")
	if err != nil {
		t.Fatal(err)
	}
}

func TestBuildEnv(t *testing.T) {
	cfg := Config{Model: "gpt-5", Effort: "high"}
	env := buildEnv(cfg)
	var foundModel, foundEffort bool
	for _, e := range env {
		if e == "CODEX_MODEL=gpt-5" {
			foundModel = true
		}
		if e == "CODEX_EFFORT=high" {
			foundEffort = true
		}
	}
	if !foundModel {
		t.Fatal("expected CODEX_MODEL in env")
	}
	if !foundEffort {
		t.Fatal("expected CODEX_EFFORT in env")
	}
}

func buildHelper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := dir + "/acp-mock"
	src := dir + "/main.go"
	if err := os.WriteFile(src, []byte(mockAgentSource), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build mock agent: %v\n%s", err, out)
	}
	return bin
}

var mockAgentSource = fmt.Sprintf(`package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type request struct {
	ID     *int64          %[1]sjson:"id"%[1]s
	Method string          %[1]sjson:"method"%[1]s
	Params json.RawMessage %[1]sjson:"params"%[1]s
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		if req.ID == nil {
			continue
		}
		switch req.Method {
		case "initialize":
			respond(*req.ID, map[string]any{
				"protocolVersion": 1,
				"agentCapabilities": map[string]any{},
				"agentInfo": map[string]string{
					"name": "mock-agent", "title": "Mock Agent", "version": "0.1.0",
				},
			})
		case "session/new":
			respond(*req.ID, map[string]any{"sessionId": "sess_test123"})
		case "session/resume":
			respond(*req.ID, map[string]any{})
		case "session/prompt":
			var p struct {
				SessionID string %[1]sjson:"sessionId"%[1]s
			}
			json.Unmarshal(req.Params, &p)
			notify("session/update", map[string]any{
				"sessionId": p.SessionID,
				"update": map[string]any{
					"sessionUpdate": "agent_message_chunk",
					"messageId":     "msg_1",
					"content":       map[string]string{"type": "text", "text": "Hello from ACP!"},
				},
			})
			respond(*req.ID, map[string]any{"stopReason": "end_turn"})
		}
	}
}

func respond(id int64, result any) {
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
	b, _ := json.Marshal(msg)
	fmt.Fprintf(os.Stdout, "%%s\n", b)
}

func notify(method string, params any) {
	msg := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	b, _ := json.Marshal(msg)
	fmt.Fprintf(os.Stdout, "%%s\n", b)
}
`, "`")
