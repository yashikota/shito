package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Slack        SlackConfig        `json:"slack"`
	Agent        AgentConfig        `json:"agent"`
	Store        StoreConfig        `json:"store"`
	Orchestrator OrchestratorConfig `json:"orchestrator"`
	Lang         string             `json:"lang"`
	Model        string             `json:"model"`
	Effort       string             `json:"effort"`
	Path         string             `json:"path"`
}

type SlackConfig struct {
	AppToken   string   `json:"appToken"`
	BotToken   string   `json:"botToken"`
	ChannelIDs []string `json:"channelIds"`
}

type AgentConfig struct {
	Type    string   `json:"type"`
	Command []string `json:"command"`
	CWD     string   `json:"cwd"`
	Model   string   `json:"model"`
	Effort  string   `json:"effort"`
}

type StoreConfig struct {
	Path string `json:"path"`
}

type OrchestratorConfig struct {
	MaxConcurrent  int           `json:"maxConcurrent"`
	InitialReply   string        `json:"initialReply"`
	UpdateEvery    time.Duration `json:"-"`
	UpdateEveryRaw string        `json:"updateEvery"`
}

func Load(path string) (Config, error) {
	cfg := Defaults()
	if path == "" {
		path = os.Getenv("SHITO_CONFIG")
	}
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return Config{}, err
		}
		if err := json.Unmarshal(b, &cfg); err != nil {
			return Config{}, err
		}
	}
	applyTopLevelAliases(&cfg)
	applyEnv(&cfg)
	if cfg.Orchestrator.UpdateEveryRaw != "" {
		d, err := time.ParseDuration(cfg.Orchestrator.UpdateEveryRaw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid orchestrator.updateEvery: %w", err)
		}
		cfg.Orchestrator.UpdateEvery = d
	}
	if cfg.Orchestrator.UpdateEvery == 0 {
		cfg.Orchestrator.UpdateEvery = 2 * time.Second
	}
	return cfg, cfg.Validate()
}

func Defaults() Config {
	home, _ := os.UserHomeDir()
	statePath := filepath.Join(home, ".local", "state", "shito", "state.json")
	return Config{
		Agent: AgentConfig{
			Type:    "acp",
			Command: []string{"codex-acp"},
		},
		Store: StoreConfig{Path: statePath},
		Orchestrator: OrchestratorConfig{
			MaxConcurrent: 1,
			InitialReply:  "Working...",
			UpdateEvery:   2 * time.Second,
		},
		Lang: "ja",
	}
}

func applyTopLevelAliases(cfg *Config) {
	if cfg.Model != "" {
		cfg.Agent.Model = cfg.Model
	}
	if cfg.Effort != "" {
		cfg.Agent.Effort = cfg.Effort
	}
	if cfg.Path != "" {
		cfg.Agent.CWD = expandHome(cfg.Path)
	}
}

func (c Config) Validate() error {
	if c.Slack.AppToken == "" {
		return errors.New("slack app token is required")
	}
	if c.Slack.BotToken == "" {
		return errors.New("slack bot token is required")
	}
	if len(c.Slack.ChannelIDs) == 0 {
		return errors.New("at least one slack channel id is required")
	}
	if c.Agent.Type != "acp" {
		return fmt.Errorf("unsupported agent type: %s", c.Agent.Type)
	}
	if len(c.Agent.Command) == 0 {
		return errors.New("agent command is required")
	}
	if c.Store.Path == "" {
		return errors.New("store path is required")
	}
	return nil
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("SLACK_APP_TOKEN"); v != "" {
		cfg.Slack.AppToken = v
	}
	if v := os.Getenv("SLACK_BOT_TOKEN"); v != "" {
		cfg.Slack.BotToken = v
	}
	if v := os.Getenv("SHITO_SLACK_CHANNEL_IDS"); v != "" {
		cfg.Slack.ChannelIDs = splitCSV(v)
	}
	if v := os.Getenv("SHITO_STORE_PATH"); v != "" {
		cfg.Store.Path = v
	}
	if v := os.Getenv("SHITO_AGENT_COMMAND"); v != "" {
		cfg.Agent.Command = strings.Fields(v)
	}
	if v := os.Getenv("SHITO_AGENT_CWD"); v != "" {
		cfg.Agent.CWD = v
	}
	if v := os.Getenv("SHITO_MODEL"); v != "" {
		cfg.Model = v
		cfg.Agent.Model = v
	}
	if v := os.Getenv("SHITO_EFFORT"); v != "" {
		cfg.Effort = v
		cfg.Agent.Effort = v
	}
	if v := os.Getenv("SHITO_PATH"); v != "" {
		cfg.Path = v
		cfg.Agent.CWD = expandHome(v)
	}
	if v := os.Getenv("SHITO_MAX_CONCURRENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Orchestrator.MaxConcurrent = n
		}
	}
	if v := os.Getenv("SHITO_LANG"); v != "" {
		cfg.Lang = v
	}
}

func splitCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func expandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}
