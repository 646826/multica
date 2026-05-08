package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/multica-ai/multica/server/internal/service/benchmark/adapter"
)

func main() {
	cfg := loadConfig()
	if cfg.ServerURL == "" || cfg.Token == "" || cfg.EvaluatorID == "" {
		slog.Error("evaluator: required env missing (MULTICA_SERVER_URL, MULTICA_EVALUATOR_TOKEN, MULTICA_EVALUATOR_ID)")
		os.Exit(1)
	}

	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		slog.Error("evaluator: cannot create workdir", "err", err)
		os.Exit(1)
	}

	registry := adapter.NewRegistry()
	registry.RegisterEvaluator(adapter.NewProgramBenchEvaluator())
	registry.RegisterEvaluator(adapter.NewSWEBenchEvaluator())

	client := NewClient(cfg.ServerURL, cfg.Token)
	runner := NewRunner(client, registry, cfg.WorkDir)

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	go func() { <-sigs; slog.Info("evaluator: shutdown requested"); cancel() }()

	sema := make(chan struct{}, cfg.MaxConcurrent)
	var wg sync.WaitGroup

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	slog.Info("evaluator: started", "evaluator_id", cfg.EvaluatorID, "max_concurrent", cfg.MaxConcurrent)

	for {
		select {
		case <-rootCtx.Done():
			slog.Info("evaluator: draining in-flight runners")
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(30 * time.Minute):
				slog.Warn("evaluator: grace period exceeded; exiting anyway")
			}
			return
		case <-ticker.C:
			jobs, err := client.Claim(rootCtx, ClaimRequest{
				EvaluatorID:   cfg.EvaluatorID,
				AdapterKinds:  cfg.AdapterKinds,
				MaxConcurrent: cfg.MaxConcurrent,
			})
			if err != nil {
				slog.Warn("evaluator: claim failed", "err", err)
				continue
			}
			for _, j := range jobs {
				sema <- struct{}{}
				wg.Add(1)
				go func(job ClaimedJob) {
					defer wg.Done()
					defer func() { <-sema }()
					runner.Run(rootCtx, job)
				}(j)
			}
		}
	}
}

type Config struct {
	ServerURL     string
	Token         string
	EvaluatorID   string
	MaxConcurrent int
	AdapterKinds  []string
	WorkDir       string
}

func loadConfig() Config {
	c := Config{
		ServerURL:     os.Getenv("MULTICA_SERVER_URL"),
		Token:         os.Getenv("MULTICA_EVALUATOR_TOKEN"),
		EvaluatorID:   os.Getenv("MULTICA_EVALUATOR_ID"),
		MaxConcurrent: 2,
		AdapterKinds:  []string{"programbench"},
		WorkDir:       "/tmp/multica-evaluator",
	}
	if v := os.Getenv("MULTICA_MAX_CONCURRENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.MaxConcurrent = n
		}
	}
	if v := os.Getenv("MULTICA_ADAPTER_KINDS"); v != "" {
		c.AdapterKinds = strings.Split(v, ",")
		for i := range c.AdapterKinds {
			c.AdapterKinds[i] = strings.TrimSpace(c.AdapterKinds[i])
		}
	}
	if v := os.Getenv("MULTICA_WORK_DIR"); v != "" {
		c.WorkDir = v
	}
	return c
}
