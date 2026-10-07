// Package web serves what a cloud deployment needs around the bot: the webhook
// endpoint, a health probe, and a trigger for the scheduler on platforms that
// put the instance to sleep between requests.
package web

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"time"

	"aksha-bitpesin/internal/config"
)

// Health is anything the probe can check — in practice the database.
type Health interface {
	Ping(ctx context.Context) error
}

// New builds the HTTP server. webhook may be nil when polling; tick may be nil
// when no external trigger is configured.
func New(cfg config.Config, webhook http.HandlerFunc, health Health, tick func(context.Context)) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if health != nil {
			if err := health.Ping(ctx); err != nil {
				slog.Error("health check failed", "err", err)
				http.Error(w, "database unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		_, _ = w.Write([]byte("ok"))
	})

	if webhook != nil && cfg.WebhookPath != "" {
		// The library verifies Telegram's secret header itself and always
		// answers 200, which is what Telegram wants — it retries on anything else.
		mux.HandleFunc("POST "+cfg.WebhookPath, webhook)
	}

	if tick != nil && cfg.SchedulerToken != "" {
		mux.HandleFunc("POST /tasks/run", func(w http.ResponseWriter, r *http.Request) {
			if !authorized(r, cfg.SchedulerToken) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			// Detached from the request: the cron caller shouldn't have to wait,
			// and shouldn't be able to cancel a run halfway through.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
			go func() {
				defer cancel()
				slog.Info("scheduler run triggered over HTTP")
				tick(ctx)
			}()
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("scheduled"))
		})
	}

	return &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// authorized accepts the token as a bearer header or an X-Scheduler-Token
// header, compared without leaking its length through timing.
func authorized(r *http.Request, want string) bool {
	got := r.Header.Get("X-Scheduler-Token")
	if got == "" {
		const prefix = "Bearer "
		if auth := r.Header.Get("Authorization"); len(auth) > len(prefix) {
			got = auth[len(prefix):]
		}
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
