package config

import (
	"os"
	"testing"
)

func TestLoadReadsEnvOverrides(t *testing.T) {
	t.Setenv("SLACK_APP_TOKEN", "xapp-test")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-test")
	t.Setenv("SHITO_SLACK_CHANNEL_IDS", "C1, C2")
	t.Setenv("SHITO_STORE_PATH", "/tmp/shito-test.json")
	t.Setenv("SHITO_MODEL", "gpt-test")
	t.Setenv("SHITO_MAX_CONCURRENT", "3")
	t.Setenv("SHITO_LANG", "en")

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Slack.AppToken != "xapp-test" || cfg.Slack.BotToken != "xoxb-test" {
		t.Fatalf("slack tokens were not loaded from env")
	}
	if len(cfg.Slack.ChannelIDs) != 2 || cfg.Slack.ChannelIDs[0] != "C1" || cfg.Slack.ChannelIDs[1] != "C2" {
		t.Fatalf("channel ids = %#v, want C1/C2", cfg.Slack.ChannelIDs)
	}
	if cfg.Agent.Model != "gpt-test" {
		t.Fatalf("model = %q, want gpt-test", cfg.Agent.Model)
	}
	if cfg.Orchestrator.MaxConcurrent != 3 {
		t.Fatalf("max concurrent = %d, want 3", cfg.Orchestrator.MaxConcurrent)
	}
	if cfg.Lang != "en" {
		t.Fatalf("lang = %q, want en", cfg.Lang)
	}
}

func TestLoadReadsJSONConfig(t *testing.T) {
	t.Setenv("SLACK_APP_TOKEN", "")
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("SHITO_SLACK_CHANNEL_IDS", "")
	t.Setenv("SHITO_STORE_PATH", "")
	t.Setenv("SHITO_MODEL", "")
	t.Setenv("SHITO_MAX_CONCURRENT", "")

	path := t.TempDir() + "/config.json"
	body := `{
		"slack": {
			"appToken": "xapp-json",
			"botToken": "xoxb-json",
			"channelIds": ["C1"]
		},
		"store": { "path": "/tmp/shito-json.json" },
		"agent": {
			"type": "acp",
			"command": ["codex", "app-server"],
			"model": "gpt-json"
		},
		"orchestrator": {
			"maxConcurrent": 2,
			"updateEvery": "1s"
		},
		"lang": "ja"
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Slack.AppToken != "xapp-json" {
		t.Fatalf("app token = %q, want xapp-json", cfg.Slack.AppToken)
	}
	if cfg.Agent.Model != "gpt-json" {
		t.Fatalf("model = %q, want gpt-json", cfg.Agent.Model)
	}
	if cfg.Lang != "ja" {
		t.Fatalf("lang = %q, want ja", cfg.Lang)
	}
}

func TestLoadAppliesTopLevelModel(t *testing.T) {
	t.Setenv("SLACK_APP_TOKEN", "xapp-test")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-test")
	t.Setenv("SHITO_SLACK_CHANNEL_IDS", "C1")

	path := t.TempDir() + "/config.json"
	body := `{
		"lang": "ja",
		"model": "gpt-5.5",
		"agent": {
			"type": "acp",
			"command": ["codex", "app-server"],
			"model": "gpt-agent"
		}
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "gpt-5.5" {
		t.Fatalf("top-level model = %q, want gpt-5.5", cfg.Model)
	}
	if cfg.Agent.Model != "gpt-5.5" {
		t.Fatalf("agent model = %q, want gpt-5.5", cfg.Agent.Model)
	}
	if cfg.Lang != "ja" {
		t.Fatalf("lang = %q, want ja", cfg.Lang)
	}
}

func TestLoadAppliesTopLevelEffort(t *testing.T) {
	t.Setenv("SLACK_APP_TOKEN", "xapp-test")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-test")
	t.Setenv("SHITO_SLACK_CHANNEL_IDS", "C1")

	path := t.TempDir() + "/config.json"
	body := `{
		"effort": "medium",
		"agent": {
			"type": "acp",
			"command": ["codex", "app-server"],
			"effort": "low"
		}
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Effort != "medium" {
		t.Fatalf("effort = %q, want medium", cfg.Effort)
	}
	if cfg.Agent.Effort != "medium" {
		t.Fatalf("agent effort = %q, want medium", cfg.Agent.Effort)
	}
}

func TestLoadAppliesTopLevelPath(t *testing.T) {
	t.Setenv("SLACK_APP_TOKEN", "xapp-test")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-test")
	t.Setenv("SHITO_SLACK_CHANNEL_IDS", "C1")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	path := t.TempDir() + "/config.json"
	body := `{
		"path": "~/prj",
		"agent": {
			"type": "acp",
			"command": ["codex", "app-server"],
			"cwd": "/tmp/agent-cwd"
		}
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := home + string(os.PathSeparator) + "prj"
	if cfg.Path != "~/prj" {
		t.Fatalf("path = %q, want ~/prj", cfg.Path)
	}
	if cfg.Agent.CWD != want {
		t.Fatalf("agent cwd = %q, want %q", cfg.Agent.CWD, want)
	}
}

func TestLoadEnvModelOverridesTopLevelModel(t *testing.T) {
	t.Setenv("SLACK_APP_TOKEN", "xapp-test")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-test")
	t.Setenv("SHITO_SLACK_CHANNEL_IDS", "C1")
	t.Setenv("SHITO_MODEL", "gpt-env")

	path := t.TempDir() + "/config.json"
	body := `{
		"model": "gpt-5.5",
		"agent": {
			"type": "acp",
			"command": ["codex", "app-server"]
		}
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "gpt-env" {
		t.Fatalf("top-level model = %q, want gpt-env", cfg.Model)
	}
	if cfg.Agent.Model != "gpt-env" {
		t.Fatalf("agent model = %q, want gpt-env", cfg.Agent.Model)
	}
}

func TestLoadEnvEffortOverridesTopLevel(t *testing.T) {
	t.Setenv("SLACK_APP_TOKEN", "xapp-test")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-test")
	t.Setenv("SHITO_SLACK_CHANNEL_IDS", "C1")
	t.Setenv("SHITO_EFFORT", "high")

	path := t.TempDir() + "/config.json"
	body := `{
		"effort": "medium",
		"agent": {
			"type": "acp",
			"command": ["codex", "app-server"]
		}
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Effort != "high" {
		t.Fatalf("effort = %q, want high", cfg.Effort)
	}
	if cfg.Agent.Effort != "high" {
		t.Fatalf("agent effort = %q, want high", cfg.Agent.Effort)
	}
}

func TestLoadEnvPathOverridesTopLevelPath(t *testing.T) {
	t.Setenv("SLACK_APP_TOKEN", "xapp-test")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-test")
	t.Setenv("SHITO_SLACK_CHANNEL_IDS", "C1")
	t.Setenv("SHITO_PATH", "/tmp/env-path")

	path := t.TempDir() + "/config.json"
	body := `{
		"path": "/tmp/config-path",
		"agent": {
			"type": "acp",
			"command": ["codex", "app-server"]
		}
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Path != "/tmp/env-path" {
		t.Fatalf("path = %q, want /tmp/env-path", cfg.Path)
	}
	if cfg.Agent.CWD != "/tmp/env-path" {
		t.Fatalf("agent cwd = %q, want /tmp/env-path", cfg.Agent.CWD)
	}
}

