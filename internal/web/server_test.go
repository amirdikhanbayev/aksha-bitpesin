package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"aksha-bitpesin/internal/config"
)

type fakeHealth struct{ err error }

func (f fakeHealth) Ping(context.Context) error { return f.err }

func TestHealthz(t *testing.T) {
	cfg := config.Config{HTTPAddr: ":0"}

	srv := New(cfg, nil, fakeHealth{}, nil)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("healthy: status = %d, want 200", rec.Code)
	}

	srv = New(cfg, nil, fakeHealth{err: errors.New("db is down")}, nil)
	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unhealthy: status = %d, want 503", rec.Code)
	}
}

func TestWebhookMountedAtConfiguredPath(t *testing.T) {
	cfg := config.Config{HTTPAddr: ":0", WebhookPath: "/telegram/webhook"}

	var called bool
	srv := New(cfg, func(w http.ResponseWriter, r *http.Request) { called = true }, fakeHealth{}, nil)

	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/telegram/webhook", nil))
	if !called {
		t.Error("webhook handler was not reached at its configured path")
	}

	// A different path must not be routed to the bot.
	called = false
	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/somewhere-else", nil))
	if called {
		t.Error("webhook handler was reached at an unexpected path")
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown path: status = %d, want 404", rec.Code)
	}
}

func TestSchedulerEndpointNeedsToken(t *testing.T) {
	cfg := config.Config{HTTPAddr: ":0", SchedulerToken: "s3cret"}

	var wg sync.WaitGroup
	var mu sync.Mutex
	runs := 0
	tick := func(context.Context) {
		mu.Lock()
		runs++
		mu.Unlock()
		wg.Done()
	}
	srv := New(cfg, nil, fakeHealth{}, tick)

	// No token at all.
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/tasks/run", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("no token: status = %d, want 403", rec.Code)
	}

	// Wrong token.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tasks/run", nil)
	req.Header.Set("X-Scheduler-Token", "guess")
	srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("wrong token: status = %d, want 403", rec.Code)
	}

	// Correct token, as a bearer header.
	wg.Add(1)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/tasks/run", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("valid token: status = %d, want 202", rec.Code)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler was not triggered")
	}

	mu.Lock()
	defer mu.Unlock()
	if runs != 1 {
		t.Errorf("scheduler ran %d times, want 1", runs)
	}
}

// Without a token configured the endpoint must not exist at all.
func TestSchedulerEndpointDisabledWithoutToken(t *testing.T) {
	srv := New(config.Config{HTTPAddr: ":0"}, nil, fakeHealth{}, func(context.Context) {
		t.Error("scheduler ran although no token is configured")
	})
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/tasks/run", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
