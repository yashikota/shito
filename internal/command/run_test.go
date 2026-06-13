package command

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var stdout bytes.Buffer
	err := Run(context.Background(), []string{"--version"}, Options{
		Stdout:  &stdout,
		Version: "v1.2.3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "v1.2.3" {
		t.Fatalf("version output = %q, want v1.2.3", got)
	}
}

func TestRunInvalidFlag(t *testing.T) {
	var stderr bytes.Buffer
	err := Run(context.Background(), []string{"--bad-flag"}, Options{
		Stderr: &stderr,
	})
	if err == nil {
		t.Fatal("expected invalid flag error")
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("stderr = %q, want invalid flag message", stderr.String())
	}
}
