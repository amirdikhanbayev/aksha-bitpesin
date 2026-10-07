package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config — everything the bot reads from the environment.
type Config struct {
	BotToken        string
	DatabaseURL     string
	AllowedUserIDs  []int64       // empty list = bot is open to everyone
	DefaultTZ       string        // default timezone for new users
	DefaultCurrency string        // default main currency
	SchedulerEvery  time.Duration // how often to check statements and recurring operations; 0 disables the ticker
	FetchRates      bool          // pull NBRK exchange rates automatically

	// TelegramAPIURL points the bot at another Bot API server — a self-hosted
	// one, or a fake in end-to-end tests. Empty means api.telegram.org.
	TelegramAPIURL string

	// Delivery: with WebhookURL set, Telegram posts updates to us; empty means
	// the bot polls Telegram itself (getUpdates), which needs no inbound port.
	WebhookURL     string
	WebhookPath    string // path part of WebhookURL, where the handler is mounted
	WebhookSecret  string // verifies that a request really came from Telegram
	HTTPAddr       string // host:port for the HTTP server; empty disables it
	SchedulerToken string // guards /tasks/run, for platforms that sleep the instance
}

// Webhook reports whether updates arrive by webhook instead of polling.
func (c Config) Webhook() bool { return c.WebhookURL != "" }

// ServeHTTP reports whether the HTTP server should be started at all.
func (c Config) ServeHTTP() bool { return c.HTTPAddr != "" }

func Load() (Config, error) {
	// Settings from /etc/aksha/.env when running on a host, if there are any.
	// Real environment variables take priority over them.
	if err := ApplyEnvFile(); err != nil {
		return Config{}, err
	}

	c := Config{
		BotToken:        os.Getenv("TELEGRAM_BOT_TOKEN"),
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		DefaultTZ:       envOr("DEFAULT_TIMEZONE", "Asia/Almaty"),
		DefaultCurrency: strings.ToUpper(envOr("DEFAULT_CURRENCY", "KZT")),
		SchedulerEvery:  15 * time.Minute,
		FetchRates:      envOr("FETCH_RATES", "true") != "false",
	}
	if c.BotToken == "" {
		return c, fmt.Errorf("TELEGRAM_BOT_TOKEN is not set")
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is not set")
	}
	if _, err := time.LoadLocation(c.DefaultTZ); err != nil {
		return c, fmt.Errorf("DEFAULT_TIMEZONE=%q: %w", c.DefaultTZ, err)
	}
	if raw := os.Getenv("ALLOWED_TELEGRAM_IDS"); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			id, err := strconv.ParseInt(part, 10, 64)
			if err != nil {
				return c, fmt.Errorf("ALLOWED_TELEGRAM_IDS: %q is not a number", part)
			}
			c.AllowedUserIDs = append(c.AllowedUserIDs, id)
		}
	}
	if every := os.Getenv("SCHEDULER_INTERVAL"); every != "" {
		d, err := time.ParseDuration(every)
		if err != nil {
			return c, fmt.Errorf("SCHEDULER_INTERVAL: %w", err)
		}
		c.SchedulerEvery = d
	}

	c.TelegramAPIURL = strings.TrimRight(os.Getenv("TELEGRAM_API_URL"), "/")
	c.SchedulerToken = os.Getenv("SCHEDULER_TOKEN")
	c.WebhookSecret = os.Getenv("WEBHOOK_SECRET")

	if raw := strings.TrimSpace(os.Getenv("WEBHOOK_URL")); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return c, fmt.Errorf("WEBHOOK_URL must be a full https URL, e.g. https://bot.example.com/telegram/webhook")
		}
		if u.Path == "" || u.Path == "/" {
			u.Path = "/telegram/webhook"
		}
		c.WebhookURL, c.WebhookPath = u.String(), u.Path
		if c.WebhookSecret == "" {
			// Telegram echoes this back in a header, so a fresh one per start is fine.
			c.WebhookSecret, err = randomToken()
			if err != nil {
				return c, err
			}
		}
	}

	// Where to listen. HTTP_ADDR is the setting; PORT exists because Cloud Run,
	// Railway and Heroku inject it and won't be told otherwise.
	c.HTTPAddr = os.Getenv("HTTP_ADDR")
	if port := os.Getenv("PORT"); port != "" {
		c.HTTPAddr = ":" + port
	}
	if c.Webhook() && c.HTTPAddr == "" {
		return c, fmt.Errorf("WEBHOOK_URL is set but HTTP_ADDR is empty: " +
			"a webhook needs something listening")
	}

	return c, nil
}

func randomToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating a webhook secret: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Allowed reports whether this user is allowed to use the bot.
func (c Config) Allowed(telegramID int64) bool {
	if len(c.AllowedUserIDs) == 0 {
		return true
	}
	for _, id := range c.AllowedUserIDs {
		if id == telegramID {
			return true
		}
	}
	return false
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
