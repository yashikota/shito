package command

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"

	"github.com/yashikota/shito/internal/codex"
	"github.com/yashikota/shito/internal/config"
	"github.com/yashikota/shito/internal/orchestrator"
	"github.com/yashikota/shito/internal/slack"
	"github.com/yashikota/shito/internal/store"
)

type Options struct {
	Logger  *slog.Logger
	Stdout  io.Writer
	Stderr  io.Writer
	Version string
}

func Run(ctx context.Context, args []string, opts Options) error {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}

	var (
		configPath  string
		showVersion bool
	)
	fs := flag.NewFlagSet("shito", flag.ContinueOnError)
	fs.SetOutput(opts.Stderr)
	fs.StringVar(&configPath, "config", "", "path to shito JSON config")
	fs.BoolVar(&showVersion, "version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if showVersion {
		_, err := fmt.Fprintln(opts.Stdout, opts.Version)
		return err
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	st, err := store.NewJSONFileStore(cfg.Store.Path)
	if err != nil {
		return err
	}
	defer func() {
		_ = st.Close()
	}()

	chatAdapter, err := slack.New(slack.Config{
		AppToken:   cfg.Slack.AppToken,
		BotToken:   cfg.Slack.BotToken,
		ChannelIDs: cfg.Slack.ChannelIDs,
	}, opts.Logger)
	if err != nil {
		return err
	}

	codingAgent, err := codex.New(ctx, codex.Config{
		Command:        cfg.Agent.Command,
		CWD:            cfg.Agent.CWD,
		Model:          cfg.Agent.Model,
		Effort:         cfg.Agent.Effort,
		ApprovalPolicy: cfg.Agent.ApprovalPolicy,
		SandboxPolicy:  cfg.Agent.SandboxPolicy,
		ServiceName:    cfg.Agent.ServiceName,
	}, opts.Logger)
	if err != nil {
		return err
	}
	defer func() {
		_ = codingAgent.Close()
	}()

	orch := orchestrator.New(orchestrator.Config{
		MaxConcurrent: cfg.Orchestrator.MaxConcurrent,
		InitialReply:  cfg.Orchestrator.InitialReply,
		Lang:          cfg.Lang,
		Model:         cfg.Agent.Model,
		Effort:        cfg.Agent.Effort,
		UpdateEvery:   cfg.Orchestrator.UpdateEvery,
		CommandCWD:    cfg.Agent.CWD,
	}, opts.Logger, chatAdapter, codingAgent, st)
	opts.Logger.Info("shito started", "channels", cfg.Slack.ChannelIDs, "agent", cfg.Agent.Type)
	return orch.Run(ctx)
}
