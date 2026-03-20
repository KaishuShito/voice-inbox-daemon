package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"voice-inbox-daemon/internal/config"
	"voice-inbox-daemon/internal/discord"
	"voice-inbox-daemon/internal/ingest"
	"voice-inbox-daemon/internal/obsidian"
	"voice-inbox-daemon/internal/pipeline"
	"voice-inbox-daemon/internal/state"
)

func main() {
	os.Exit(run())
}

func run() int {
	if len(os.Args) < 2 {
		printUsage()
		return 1
	}

	cmd := os.Args[1]
	switch cmd {
	case "help", "-h", "--help":
		printUsage()
		return 0
	}

	cfg, err := config.LoadForCommand(cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		return 1
	}

	if err := os.MkdirAll(cfg.AudioStoreDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "create audio store dir: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(cfg.LogDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "create log dir: %v\n", err)
		return 1
	}

	store, err := state.Open(cfg.StateDBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open state db: %v\n", err)
		return 1
	}
	defer store.Close()

	runner := pipeline.New(
		cfg,
		store,
		discord.NewWithBaseURL(cfg.DiscordBotToken, cfg.DiscordAPIBaseURL),
		obsidian.New(cfg.ObsidianBaseURL, cfg.ObsidianAuthHeader, cfg.ObsidianAPIKey, cfg.ObsidianVerifyTLS),
	)

	switch cmd {
	case "doctor":
		return runDoctor(runner, os.Args[2:])
	case "poll":
		return runPoll(runner, os.Args[2:])
	case "retry":
		return runRetry(runner, os.Args[2:])
	case "cleanup":
		return runCleanup(runner, os.Args[2:])
	case "status":
		return runStatus(runner, os.Args[2:])
	case "serve":
		return runServe(runner, store, cfg, os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		return 1
	}
}

func runDoctor(runner *pipeline.Runner, args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := runner.Doctor(ctx)
	printResult(res, *asJSON)
	if err != nil || res.Failed > 0 {
		return 1
	}
	return 0
}

func runPoll(runner *pipeline.Runner, args []string) int {
	fs := flag.NewFlagSet("poll", flag.ContinueOnError)
	once := fs.Bool("once", false, "run a single poll cycle")
	asJSON := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if !*once {
		fmt.Fprintln(os.Stderr, "poll requires --once in this daemon architecture")
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	res, err := runner.PollOnce(ctx)
	printResult(res, *asJSON)
	if err != nil {
		return res.ExitCode()
	}
	return 0
}

func runRetry(runner *pipeline.Runner, args []string) int {
	fs := flag.NewFlagSet("retry", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	res, err := runner.Retry(ctx)
	printResult(res, *asJSON)
	if err != nil {
		return res.ExitCode()
	}
	return 0
}

func runCleanup(runner *pipeline.Runner, args []string) int {
	fs := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	res, err := runner.Cleanup(ctx)
	printResult(res, *asJSON)
	if err != nil || res.Failed > 0 {
		return 1
	}
	return 0
}

func runStatus(runner *pipeline.Runner, args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	res, err := runner.Status(ctx)
	printResult(res, *asJSON)
	if err != nil || res.Failed > 0 {
		return 1
	}
	return 0
}

func runServe(runner *pipeline.Runner, store *state.Store, cfg config.Config, args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 1
	}

	server := ingest.NewServer(cfg, store)
	httpServer := &http.Server{
		Addr:              cfg.IngestListenAddr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			runCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
			res, err := runner.ProcessCapturesOnce(runCtx)
			cancel()
			if err != nil && (res.Processed > 0 || len(res.Errors) > 0) {
				fmt.Fprintf(os.Stderr, "capture processor error: %v\n", err)
			}

			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	fmt.Fprintf(os.Stdout, "listening on %s\n", cfg.IngestListenAddr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "serve error: %v\n", err)
		return 1
	}
	return 0
}

func printResult(res pipeline.Result, asJSON bool) {
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
		return
	}

	fmt.Printf("command=%s run_id=%s processed=%d succeeded=%d failed=%d requeued=%d skipped=%d duration_ms=%d\n",
		res.Command,
		res.RunID,
		res.Processed,
		res.Succeeded,
		res.Failed,
		res.Requeued,
		res.Skipped,
		res.DurationMS,
	)
	if len(res.Errors) > 0 {
		fmt.Fprintln(os.Stderr, "errors:")
		for _, e := range res.Errors {
			fmt.Fprintf(os.Stderr, "- %s\n", sanitize(e))
		}
	}
	if len(res.Data) > 0 {
		b, err := json.Marshal(res.Data)
		if err == nil {
			fmt.Printf("data=%s\n", string(b))
		}
	}
}

func sanitize(s string) string {
	if s == "" {
		return s
	}
	patterns := []string{"Bot ", "Bearer "}
	for _, p := range patterns {
		i := strings.Index(s, p)
		if i >= 0 {
			return s[:i+len(p)] + "[REDACTED]"
		}
	}
	return s
}

func printUsage() {
	msg := `voice-inbox: Discord voice inbox -> Obsidian journal CLI

Usage:
  voice-inbox doctor [--json]
  voice-inbox poll --once [--json]
  voice-inbox retry [--json]
  voice-inbox cleanup [--json]
  voice-inbox status [--json]
  voice-inbox serve
`
	_, _ = fmt.Fprint(os.Stderr, msg)
}
