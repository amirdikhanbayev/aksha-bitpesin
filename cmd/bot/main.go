// Command bot — a personal finance tracking Telegram bot.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	// Timezone data compiled into the binary: month boundaries are computed in
	// the user's own timezone, and container images usually ship without tzdata.
	_ "time/tzdata"

	"aksha-bitpesin/internal/bot"
	"aksha-bitpesin/internal/config"
	"aksha-bitpesin/internal/scheduler"
	"aksha-bitpesin/internal/storage"
	"aksha-bitpesin/internal/web"
)

func main() {
	// The container image has no shell and no curl, so the binary is also its
	// own health probe: `bot -health` asks the running instance and nothing else.
	health := flag.Bool("health", false, "check the running instance and exit")
	flag.Parse()
	if *health {
		if err := checkHealth(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()})))

	if err := run(); err != nil {
		slog.Error("bot stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := storage.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := st.Migrate(ctx); err != nil {
		return err
	}

	b, err := bot.New(cfg, st)
	if err != nil {
		return err
	}

	sch := scheduler.New(st, b, cfg)
	go sch.Run(ctx)

	// The HTTP server must be listening before the webhook is registered,
	// otherwise Telegram's first deliveries bounce.
	if cfg.ServeHTTP() {
		var webhook http.HandlerFunc
		if cfg.Webhook() {
			webhook = b.WebhookHandler()
		}
		srv := web.New(cfg, webhook, st, sch.Tick)
		go func() {
			slog.Info("http listening", "addr", srv.Addr, "webhook_path", cfg.WebhookPath)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("http server", "err", err)
				stop() // a dead listener in webhook mode means no updates at all
			}
		}()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				slog.Warn("http shutdown", "err", err)
			}
		}()
	}

	slog.Info("bot started",
		"whitelist", len(cfg.AllowedUserIDs),
		"timezone", cfg.DefaultTZ,
		"currency", cfg.DefaultCurrency,
		"webhook", cfg.Webhook(),
		"scheduler_every", cfg.SchedulerEvery)

	if err := b.Run(ctx); err != nil { // blocks until the context is canceled
		return err
	}
	slog.Info("shutting down")
	return nil
}

// checkHealth calls /healthz on this instance. It reads only the address
// settings, so it works without the bot token or the database URL.
func checkHealth() error {
	// The same settings the bot itself started with, file included.
	if err := config.ApplyEnvFile(); err != nil {
		return err
	}
	addr := os.Getenv("HTTP_ADDR")
	if port := os.Getenv("PORT"); port != "" {
		addr = ":" + port
	}
	if addr == "" {
		return errors.New("neither HTTP_ADDR nor PORT is set: nothing to probe")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("address %q: %w", addr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://"+net.JoinHostPort(host, port)+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/healthz returned %s", resp.Status)
	}
	return nil
}

func logLevel() slog.Level {
	if os.Getenv("DEBUG") == "true" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}
