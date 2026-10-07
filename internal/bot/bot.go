// Package bot — the Telegram interface for money tracking.
package bot

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	tg "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"aksha-bitpesin/internal/config"
	"aksha-bitpesin/internal/storage"
)

type Bot struct {
	api   *tg.Bot
	st    *storage.Store
	cfg   config.Config
	http  *http.Client
	clock func() time.Time

	// locks holds one mutex per Telegram user. The library runs every update in
	// its own goroutine, so without it two quick taps on the same button would
	// both read the same draft and both save it.
	locks sync.Map // int64 → *sync.Mutex
}

// lockUser serializes the updates of one user: a dialog is a sequence, and its
// steps must see each other's writes. Different users still run in parallel.
func (b *Bot) lockUser(telegramID int64) (unlock func()) {
	m, _ := b.locks.LoadOrStore(telegramID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func New(cfg config.Config, st *storage.Store) (*Bot, error) {
	b := &Bot{
		st:    st,
		cfg:   cfg,
		http:  &http.Client{Timeout: 20 * time.Second},
		clock: time.Now,
	}

	opts := []tg.Option{
		tg.WithDefaultHandler(b.onMessage),
		tg.WithCallbackQueryDataHandler("", tg.MatchTypePrefix, b.onCallback),
		tg.WithErrorsHandler(func(err error) { slog.Error("telegram", "err", err) }),
	}
	if cfg.Webhook() {
		opts = append(opts, tg.WithWebhookSecretToken(cfg.WebhookSecret))
	}
	if cfg.TelegramAPIURL != "" {
		opts = append(opts, tg.WithServerURL(cfg.TelegramAPIURL))
	}
	api, err := tg.New(cfg.BotToken, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating the bot: %w", err)
	}
	b.api = api
	return b, nil
}

// WebhookHandler serves updates Telegram posts to us. Only meaningful in
// webhook mode; the HTTP server mounts it at the configured path.
func (b *Bot) WebhookHandler() http.HandlerFunc { return b.api.WebhookHandler() }

// Run receives updates until the context is canceled, either by webhook or by
// polling. The two are mutually exclusive on Telegram's side: while a webhook is
// registered getUpdates fails with 409, so switching back deletes it first.
func (b *Bot) Run(ctx context.Context) error {
	if err := b.setCommands(ctx); err != nil {
		slog.Warn("failed to set the command menu", "err", err)
	}

	if b.cfg.Webhook() {
		_, err := b.api.SetWebhook(ctx, &tg.SetWebhookParams{
			URL:         b.cfg.WebhookURL,
			SecretToken: b.cfg.WebhookSecret,
		})
		if err != nil {
			return fmt.Errorf("registering the webhook: %w", err)
		}
		slog.Info("receiving updates by webhook", "url", b.cfg.WebhookURL)
		b.api.StartWebhook(ctx)
		return nil
	}

	// Drop any webhook left over from a previous deploy, otherwise polling 409s.
	if _, err := b.api.DeleteWebhook(ctx, &tg.DeleteWebhookParams{}); err != nil {
		slog.Warn("could not delete a previous webhook", "err", err)
	}
	slog.Info("receiving updates by long polling")
	b.api.Start(ctx)
	return nil
}

func (b *Bot) setCommands(ctx context.Context) error {
	cmds := []models.BotCommand{
		{Command: "add", Description: "Add an operation"},
		{Command: "month", Description: "Statement for the current month"},
		{Command: "report", Description: "Report for a period: /report september"},
		{Command: "last", Description: "Recent operations"},
		{Command: "search", Description: "Find an operation"},
		{Command: "accounts", Description: "Accounts and balances"},
		{Command: "categories", Description: "Categories"},
		{Command: "transfer", Description: "Transfer between accounts"},
		{Command: "debts", Description: "Debts and loans"},
		{Command: "budget", Description: "Monthly limits"},
		{Command: "recurring", Description: "Recurring operations"},
		{Command: "rates", Description: "Exchange rates"},
		{Command: "export", Description: "Export to CSV"},
		{Command: "settings", Description: "Settings"},
		{Command: "erase", Description: "Erase all my data"},
		{Command: "help", Description: "How to use"},
		{Command: "cancel", Description: "Cancel the current input"},
	}
	_, err := b.api.SetMyCommands(ctx, &tg.SetMyCommandsParams{Commands: cmds})
	return err
}

// --- sending ---

func (b *Bot) send(ctx context.Context, chatID int64, text string, markup models.ReplyMarkup) {
	params := &tg.SendMessageParams{
		ChatID:             chatID,
		Text:               text,
		ParseMode:          models.ParseModeHTML,
		ReplyMarkup:        markup,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: ptr(true)},
	}
	if _, err := b.api.SendMessage(ctx, params); err != nil {
		if !b.retrySend(ctx, params, err) {
			slog.Error("send", "chat", chatID, "err", err)
		}
	}
}

// retrySend gives a failed message one more go, and only when another go could
// plausibly work: Telegram asking us to slow down, or a network hiccup. A message
// rejected on its merits — bad markup, blocked bot — is not worth repeating.
// One attempt, then the message is dropped: a bot that keeps retrying a monthly
// statement for an hour is worse than one that misses it.
func (b *Bot) retrySend(ctx context.Context, params *tg.SendMessageParams, cause error) bool {
	wait, ok := retryDelay(cause)
	if !ok {
		return false
	}
	slog.Warn("send failed, retrying once", "in", wait, "err", cause)
	select {
	case <-ctx.Done():
		return false
	case <-time.After(wait):
	}
	if _, err := b.api.SendMessage(ctx, params); err != nil {
		slog.Error("send failed again, giving up", "err", err)
	}
	return true
}

// retryDelay says how long to wait before a second attempt, and whether one is
// worth making at all.
func retryDelay(err error) (time.Duration, bool) {
	var tooMany *tg.TooManyRequestsError
	if errors.As(err, &tooMany) {
		wait := time.Duration(tooMany.RetryAfter) * time.Second
		if wait <= 0 {
			wait = time.Second
		}
		if wait > 30*time.Second { // waiting longer than this helps nobody
			return 0, false
		}
		return wait, true
	}
	// A verdict from Telegram is final: repeating it would only fail the same way.
	for _, final := range []error{
		tg.ErrorBadRequest, tg.ErrorForbidden, tg.ErrorUnauthorized,
		tg.ErrorNotFound, tg.ErrorConflict,
	} {
		if errors.Is(err, final) {
			return 0, false
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return 0, false
	}
	return 2 * time.Second, true // transport trouble: worth one more go
}

// Send — the public send method (needed by the scheduler).
func (b *Bot) Send(ctx context.Context, chatID int64, text string) {
	for _, part := range splitLong(text, 3800) {
		b.send(ctx, chatID, part, nil)
	}
}

func (b *Bot) sendLong(ctx context.Context, chatID int64, text string, markup models.ReplyMarkup) {
	parts := splitLong(text, 3800)
	for i, part := range parts {
		var m models.ReplyMarkup
		if i == len(parts)-1 {
			m = markup
		}
		b.send(ctx, chatID, part, m)
	}
}

func (b *Bot) edit(ctx context.Context, chatID int64, messageID int, text string, markup models.ReplyMarkup) {
	_, err := b.api.EditMessageText(ctx, &tg.EditMessageTextParams{
		ChatID:             chatID,
		MessageID:          messageID,
		Text:               text,
		ParseMode:          models.ParseModeHTML,
		ReplyMarkup:        markup,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: ptr(true)},
	})
	if err != nil && !strings.Contains(err.Error(), "message is not modified") {
		slog.Error("edit", "chat", chatID, "err", err)
	}
}

func (b *Bot) answer(ctx context.Context, q *models.CallbackQuery, text string) {
	_, err := b.api.AnswerCallbackQuery(ctx, &tg.AnswerCallbackQueryParams{
		CallbackQueryID: q.ID,
		Text:            text,
	})
	if err != nil {
		slog.Debug("answerCallback", "err", err)
	}
}

// --- routing ---

func (b *Bot) onMessage(ctx context.Context, _ *tg.Bot, update *models.Update) {
	if update.Message == nil || update.Message.From == nil {
		return
	}
	msg := update.Message
	if !b.cfg.Allowed(msg.From.ID) {
		b.send(ctx, msg.Chat.ID, "This bot is private. Access denied.", nil)
		slog.Warn("access denied", "telegram_id", msg.From.ID, "username", msg.From.Username)
		return
	}
	defer b.lockUser(msg.From.ID)()

	u, err := b.st.EnsureUser(ctx, msg.From.ID, msg.From.Username, b.cfg.DefaultTZ, b.cfg.DefaultCurrency)
	if err != nil {
		slog.Error("EnsureUser", "err", err)
		b.send(ctx, msg.Chat.ID, "The database is unavailable, please try again.", nil)
		return
	}

	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	// A new user is set up before anything else: timezone, currency, accounts.
	// Mid-setup the state is already saved, so this only catches the start.
	if text != "/help" && b.needsOnboarding(ctx, u) {
		if state, _, _ := b.st.LoadState(ctx, u.ID); state == "" {
			b.startOnboarding(ctx, u, msg.Chat.ID)
			return
		}
	}

	if strings.HasPrefix(text, "/") {
		cmd, args, _ := strings.Cut(text, " ")
		cmd = strings.ToLower(cmd)
		if i := strings.Index(cmd, "@"); i > 0 { // "/report@my_bot" in a group chat
			cmd = cmd[:i]
		}
		b.handleCommand(ctx, u, msg.Chat.ID, cmd, strings.TrimSpace(args))
		return
	}

	// Bottom keyboard buttons
	if handled := b.handleMenuButton(ctx, u, msg.Chat.ID, text); handled {
		return
	}

	// Continue the dialog if the bot is waiting for something
	state, data, err := b.st.LoadState(ctx, u.ID)
	if err != nil {
		slog.Error("LoadState", "err", err)
	}
	if state != "" {
		b.handleStateInput(ctx, u, msg.Chat.ID, state, data, text)
		return
	}

	// Nothing is pending: point at the menu, since everything starts from a button.
	b.nudge(ctx, msg.Chat.ID)
}

func (b *Bot) onCallback(ctx context.Context, _ *tg.Bot, update *models.Update) {
	q := update.CallbackQuery
	if q == nil || q.Message.Message == nil {
		return
	}
	if !b.cfg.Allowed(q.From.ID) {
		b.answer(ctx, q, "Access denied")
		return
	}
	defer b.lockUser(q.From.ID)()
	u, err := b.st.EnsureUser(ctx, q.From.ID, q.From.Username, b.cfg.DefaultTZ, b.cfg.DefaultCurrency)
	if err != nil {
		slog.Error("EnsureUser", "err", err)
		b.answer(ctx, q, "Database unavailable")
		return
	}
	b.handleCallback(ctx, u, q)
}

// nudge answers text nobody asked for. The bot is driven by buttons — only
// amounts and rates are typed, and only when it asks.
func (b *Bot) nudge(ctx context.Context, chatID int64) {
	b.send(ctx, chatID, "Tap a button to start: <b>➖ Expense</b>, <b>➕ Income</b> or <b>🔁 Transfer</b> — "+
		"or <b>🤝 Debts</b> for loans and money lent. /help explains everything.", mainMenu())
}

func ptr[T any](v T) *T { return &v }

func esc(s string) string { return html.EscapeString(s) }

// splitLong splits long text by lines to fit Telegram's message limit.
func splitLong(text string, limit int) []string {
	if len(text) <= limit {
		return []string{text}
	}
	var parts []string
	var cur strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if cur.Len()+len(line)+1 > limit && cur.Len() > 0 {
			parts = append(parts, cur.String())
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteByte('\n')
		}
		cur.WriteString(line)
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}
