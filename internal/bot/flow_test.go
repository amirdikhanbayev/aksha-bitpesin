package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"aksha-bitpesin/internal/config"
	"aksha-bitpesin/internal/storage"
)

// These tests drive the whole bot the way a person does — sending messages and
// tapping buttons — against a fake Telegram server and a real Postgres. They run
// only when TEST_DATABASE_URL is set.

// --- a fake Telegram Bot API ---

type fakeMessage struct {
	id      int
	seq     int
	text    string
	buttons [][]models.InlineKeyboardButton
}

type fakeTelegram struct {
	mu   sync.Mutex
	next int
	seq  int
	msgs map[int]*fakeMessage
	docs []string // file names of the documents sent
	srv  *httptest.Server

	// latency delays every call, the way the real API answers over a network:
	// it widens the window in which two updates of one user overlap.
	latency time.Duration
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{msgs: map[int]*fakeMessage{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

// record stores what the bot showed; an edit replaces the message's earlier state.
func (f *fakeTelegram) record(id int, text, markup string) {
	f.seq++
	m := &fakeMessage{id: id, seq: f.seq, text: text}
	if markup != "" {
		var kb struct {
			Inline [][]models.InlineKeyboardButton `json:"inline_keyboard"`
		}
		if err := json.Unmarshal([]byte(markup), &kb); err == nil {
			m.buttons = kb.Inline
		}
	}
	f.msgs[id] = m
}

func (f *fakeTelegram) handle(w http.ResponseWriter, r *http.Request) {
	time.Sleep(f.latency)
	f.mu.Lock()
	defer f.mu.Unlock()

	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	_ = r.ParseMultipartForm(1 << 20)
	w.Header().Set("Content-Type", "application/json")

	switch method {
	case "getMe":
		fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Fake","username":"fake_bot"}}`)
	case "sendMessage":
		f.next++
		f.record(f.next, r.FormValue("text"), r.FormValue("reply_markup"))
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"date":0,"chat":{"id":1,"type":"private"}}}`, f.next)
	case "editMessageText":
		var id int
		fmt.Sscanf(r.FormValue("message_id"), "%d", &id)
		f.record(id, r.FormValue("text"), r.FormValue("reply_markup"))
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"date":0,"chat":{"id":1,"type":"private"}}}`, id)
	case "sendDocument":
		if r.MultipartForm != nil {
			for _, fs := range r.MultipartForm.File {
				for _, fh := range fs {
					f.docs = append(f.docs, fh.Filename)
				}
			}
		}
		f.next++
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"date":0,"chat":{"id":1,"type":"private"}}}`, f.next)
	default:
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	}
}

// documents lists the files the bot has sent so far.
func (f *fakeTelegram) documents() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.docs...)
}

// visible lists the messages a person would see, newest first.
func (f *fakeTelegram) visible() []*fakeMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*fakeMessage, 0, len(f.msgs))
	for _, m := range f.msgs {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].seq > out[j].seq })
	return out
}

// --- a virtual user ---

type person struct {
	t      *testing.T
	b      *Bot
	tg     *fakeTelegram
	tgID   int64
	nextID int
}

func newPerson(t *testing.T, st *storage.Store, offset int64) *person {
	t.Helper()
	tg := newFakeTelegram(t)
	b, err := New(config.Config{
		BotToken:        "123:TEST",
		TelegramAPIURL:  tg.srv.URL,
		DefaultTZ:       "Asia/Almaty",
		DefaultCurrency: "KZT",
	}, st)
	if err != nil {
		t.Fatalf("creating the bot: %v", err)
	}
	return &person{t: t, b: b, tg: tg, tgID: time.Now().UnixNano()%1_000_000_000 + offset}
}

func (p *person) user() *models.User { return &models.User{ID: p.tgID, Username: "tester"} }

// say sends a text message, as typed.
func (p *person) say(text string) {
	p.t.Helper()
	p.nextID++
	p.b.onMessage(context.Background(), nil, &models.Update{Message: &models.Message{
		ID: p.nextID, From: p.user(), Chat: models.Chat{ID: p.tgID}, Text: text,
	}})
}

// tap presses the newest visible button with exactly this label.
func (p *person) tap(label string) {
	p.t.Helper()
	for _, m := range p.tg.visible() {
		for _, row := range m.buttons {
			for _, bt := range row {
				if bt.Text != label {
					continue
				}
				p.b.onCallback(context.Background(), nil, &models.Update{CallbackQuery: &models.CallbackQuery{
					ID: "cb", From: *p.user(), Data: bt.CallbackData,
					Message: models.MaybeInaccessibleMessage{
						Message: &models.Message{ID: m.id, Chat: models.Chat{ID: p.tgID}},
					},
				}})
				return
			}
		}
	}
	p.t.Fatalf("no button %q on screen.\nVisible buttons:\n%s", label, p.screen())
}

// screen describes what is on offer right now, for failure messages.
func (p *person) screen() string {
	var b strings.Builder
	for i, m := range p.tg.visible() {
		if i >= 4 {
			break
		}
		fmt.Fprintf(&b, "  [%d] %q\n", m.id, oneLine(plain(m.text)))
		for _, row := range m.buttons {
			labels := make([]string, 0, len(row))
			for _, bt := range row {
				labels = append(labels, bt.Text)
			}
			fmt.Fprintf(&b, "       %v\n", labels)
		}
	}
	return b.String()
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 110 {
		s = s[:110] + "…"
	}
	return s
}

// plain is a message as a person reads it: the HTML the bot sends, without its tags.
func plain(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>' && inTag:
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&").Replace(b.String())
}

// sees asserts that one of the latest messages reads as this text.
func (p *person) sees(want string) {
	p.t.Helper()
	for i, m := range p.tg.visible() {
		if i >= 3 {
			break
		}
		if strings.Contains(plain(m.text), want) {
			return
		}
	}
	p.t.Fatalf("expected to see %q.\nOn screen:\n%s", want, p.screen())
}

// tapFirstOperation opens the card of the newest operation offered in a list,
// without depending on today's date appearing in its label.
func (p *person) tapFirstOperation() {
	p.t.Helper()
	for _, m := range p.tg.visible() {
		for _, row := range m.buttons {
			for _, bt := range row {
				if strings.HasPrefix(bt.CallbackData, "txshow:") {
					p.tap(bt.Text)
					return
				}
			}
		}
	}
	p.t.Fatalf("no operation button on screen.\n%s", p.screen())
}

// hasButton reports whether a button with this label is on screen.
func (p *person) hasButton(label string) bool {
	for _, m := range p.tg.visible() {
		for _, row := range m.buttons {
			for _, bt := range row {
				if bt.Text == label {
					return true
				}
			}
		}
	}
	return false
}

func openBotStore(t *testing.T) (*storage.Store, context.Context) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	st, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	return st, ctx
}

// withCategories gives the user the categories a test then picks from, the way
// they would exist after having been used once. The database itself starts empty.
func withCategories(t *testing.T, st *storage.Store, ctx context.Context, u storage.User, kind string, names ...string) {
	t.Helper()
	emojis := map[string]string{
		"Groceries": "🛒", "Transport": "🚕", "Salary": "💼", "Pets": "🐾", "Coffee": "☕",
	}
	for _, name := range names {
		if _, err := st.CreateCategory(ctx, u.ID, name, kind, emojis[name]); err != nil {
			t.Fatalf("CreateCategory %s: %v", name, err)
		}
	}
}

// userOf looks up the bot user behind a virtual person.
func userOf(t *testing.T, st *storage.Store, ctx context.Context, p *person) storage.User {
	t.Helper()
	u, err := st.EnsureUser(ctx, p.tgID, "tester", "Asia/Almaty", "KZT")
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	return u
}

// --- the scenarios ---

// From the very first message to a recorded operation, only amounts are typed.
func TestGuidedFlowFromScratch(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 101)

	// A new user is set up first: timezone, currency, where the money is.
	p.say("/start")
	p.tap("Keep Asia/Almaty")
	p.tap("Keep KZT")
	p.tap("💵 Cash")
	p.tap("Done ▶️")
	p.tap("KZT")
	p.tap("Done ▶️")
	p.sees("Cash · KZT — how much is there right now?")
	p.say("100000") // the only thing typed so far
	p.sees("You're all set!")

	u := userOf(t, st, ctx, p)

	// An expense: the amount is typed, the category and source are tapped. There
	// are no categories yet, so the first one is picked from what the bot offers.
	p.say("➖ Expense")
	p.sees("How much? Send the amount in KZT")
	p.say("5000")
	p.sees("Your first expense category")
	p.say("🛒 Groceries") // named by the user: nothing is offered up front
	p.sees("Who was it paid to")
	p.tap("⏭ No source")
	p.sees("Expense recorded")
	p.sees("5,000 KZT")
	p.sees("Category: 🛒 Groceries")

	// It really is in the books, and the balance moved.
	last, err := st.LastTransactions(ctx, u.ID, 1)
	if err != nil || len(last) != 1 {
		t.Fatalf("LastTransactions: %v %v", err, last)
	}
	if last[0].Kind != storage.KindExpense || last[0].Amount != 5_000_00 || last[0].CategoryName != "Groceries" {
		t.Errorf("recorded %+v, want a 5000 KZT Groceries expense", last[0])
	}
	accs, _ := st.ListAccountsWithBalance(ctx, u.ID, false)
	if len(accs) != 1 || accs[0].Balance != 95_000_00 {
		t.Errorf("balance = %+v, want 95000 KZT", accs)
	}

	// Backdating is a tap too: the date picker offers recent days.
	p.tap("📅 Date")
	p.sees("When did it happen?")
	p.tap("Yesterday")
	p.sees("Expense recorded")
	moved, _ := st.Transaction(ctx, u.ID, last[0].ID)
	loc, _ := time.LoadLocation("Asia/Almaty")
	want := time.Now().In(loc).AddDate(0, 0, -1).Format("2006-01-02")
	if got := moved.OccurredAt.In(loc).Format("2006-01-02"); got != want {
		t.Errorf("operation date = %s, want yesterday (%s)", got, want)
	}

	// Income works the same way.
	p.say("➕ Income")
	p.sees("How much?")
	p.say("300000")
	p.sees("Your first income category")
	p.say("💼 Salary")
	p.tap("⏭ No source")
	p.sees("Income recorded")
	accs, _ = st.ListAccountsWithBalance(ctx, u.ID, false)
	if accs[0].Balance != 395_000_00 {
		t.Errorf("balance after income = %d, want 39500000", accs[0].Balance)
	}
}

// A purchase in tenge from a dollar card: the user picks the account, picks
// “another currency”, types the amount, and then types what the bank debited.
func TestGuidedCrossCurrencyByDebitedAmount(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 102)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 100_000_00); err != nil {
		t.Fatal(err)
	}
	usd, err := st.CreateAccount(ctx, u.ID, "Card", "USD", 1_000_00)
	if err != nil {
		t.Fatal(err)
	}
	withCategories(t, st, ctx, u, storage.KindExpense, "Groceries")

	p.say("➖ Expense")
	p.sees("paid from which account?") // two accounts: the bot asks, it doesn't assume
	p.tap("💳 Card · USD")
	p.sees("Send the amount in USD")
	p.tap("💱 Another currency")
	p.sees("Which currency was the operation in?")
	if p.hasButton("USD") {
		t.Error("the account's own currency was offered as “another currency”")
	}
	p.tap("KZT")
	p.sees("How much in KZT?")
	p.say("5000")
	p.sees("How much USD was debited from the account?")
	p.say("10.64")
	p.sees("your rate: 1 USD = 469.92") // derived from the two real amounts, shown to 4 places
	p.tap("🛒 Groceries")
	p.tap("⏭ No source")
	p.sees("Purchase: 5,000 KZT")

	last, _ := st.LastTransactions(ctx, u.ID, 1)
	got := last[0]
	if got.AccountID != usd.ID || got.Amount != 10_64 {
		t.Errorf("debited %d from account %d, want 1064 from %d", got.Amount, got.AccountID, usd.ID)
	}
	if got.OriginalAmount == nil || *got.OriginalAmount != 5_000_00 || got.OriginalCurrency != "KZT" {
		t.Errorf("original = %v %s, want 500000 KZT", got.OriginalAmount, got.OriginalCurrency)
	}
	if !got.Converted() {
		t.Error("the operation should carry the user's rate")
	}
}

// Same purchase, but the user knows the rate rather than the debited amount.
func TestGuidedCrossCurrencyByRate(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 103)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAccount(ctx, u.ID, "Card", "USD", 1_000_00); err != nil {
		t.Fatal(err)
	}
	withCategories(t, st, ctx, u, storage.KindExpense, "Groceries")

	p.say("➖ Expense")
	p.tap("💳 Card · USD")
	p.tap("💱 Another currency")
	p.tap("KZT")
	p.say("5000")
	p.tap("Enter the rate instead")
	p.sees("How do you want to enter the rate?")
	p.tap("1 USD = ? KZT") // the way it is quoted at a bank
	p.sees("How many KZT per 1 USD?")
	p.say("470")
	p.sees("your rate: 1 USD = 470 KZT")
	p.tap("🛒 Groceries")
	p.tap("⏭ No source")

	last, _ := st.LastTransactions(ctx, u.ID, 1)
	if last[0].Amount != 10_64 { // 5000 / 470 = 10.638…
		t.Errorf("debited = %d, want 1064", last[0].Amount)
	}
	if last[0].Rate == nil || *last[0].Rate < 0.00212 || *last[0].Rate > 0.00214 {
		t.Errorf("stored rate = %v, want ≈ 1/470", last[0].Rate)
	}
}

// Only a number is accepted where a number is expected.
func TestGuidedAmountRejectsWords(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 104)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 0); err != nil {
		t.Fatal(err)
	}
	withCategories(t, st, ctx, u, storage.KindExpense, "Groceries")
	p.say("➖ Expense")
	p.say("five thousand")
	p.sees("as a number")
	p.say("5000") // and it recovers
	p.sees("Category?")
}

// New accounts are named from suggestions; a taken name gets the currency added.
func TestAddingACurrencyToAnExistingPlace(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 106)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 0); err != nil {
		t.Fatal(err)
	}

	p.say("/newaccount")
	p.sees("Where is the money kept?")
	p.tap("💵 Cash") // the place exists: this adds another currency to it
	p.sees("which currencies does it hold?")
	if p.hasButton("KZT") {
		t.Error("Cash already holds KZT, so it must not be offered again")
	}
	p.tap("USD")
	p.tap("Done ▶️")
	p.tap("0️⃣ Start from zero")
	p.sees("✅ 💵 Cash · USD — 0 USD")
	p.sees("💳 Accounts and balances") // the wizard ends on the updated list

	accs, _ := st.ListAccounts(ctx, u.ID, false)
	if len(accs) != 2 {
		t.Fatalf("accounts = %d, want 2", len(accs))
	}
	for _, a := range accs {
		if a.Name != "Cash" {
			t.Errorf("account named %q, want both under the one place “Cash”", a.Name)
		}
	}
}

// A recurring operation: the day of the month is tapped from a grid.
func TestRecurringDayIsTapped(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 107)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 0); err != nil {
		t.Fatal(err)
	}
	withCategories(t, st, ctx, u, storage.KindIncome, "Salary")
	p.say("/recurring")
	p.tap("➕ Income")
	p.tap("💼 Salary")
	p.say("500000")
	p.sees("On which day of the month?")
	p.tap("5")
	p.sees("I will record")
	list, err := st.ListRecurring(ctx, u.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListRecurring: %v %v", err, list)
	}
	if list[0].DayOfMonth != 5 || list[0].Amount != 500_000_00 || list[0].Kind != storage.KindIncome {
		t.Errorf("recurring = %+v, want income of 500000 on day 5", list[0])
	}
}

// The report is asked for by tapping a month, not by typing one.
func TestReportPeriodIsTapped(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 108)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 0); err != nil {
		t.Fatal(err)
	}
	p.say("📊 Reports")
	p.sees("For which period?")
	for _, label := range []string{"This month", "Last month", "This week", "Today", "This year"} {
		if !p.hasButton(label) {
			t.Errorf("no %q button on the period picker", label)
		}
	}
	// A specific older month, labelled the way a person reads it.
	loc, _ := time.LoadLocation("Asia/Almaty")
	now := time.Now().In(loc)
	older := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, -3, 0)
	p.tap(older.Format("Jan 2006"))
	p.sees("Statement:")
}

// Text nobody asked for, however sensible, doesn't start an operation: everything
// begins from a button.
func TestTextOutsideAFlowPointsAtTheMenu(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 109)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 0); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"-5000 groceries Magnum", "5000", "hello"} {
		p.say(text)
		p.sees("Tap a button to start")
	}
	if last, _ := st.LastTransactions(ctx, u.ID, 1); len(last) != 0 {
		t.Errorf("typed text recorded an operation: %+v", last)
	}
}

// The prompts follow the logic of what is being recorded.
func TestPromptsFollowTheOperation(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 110)
	u := userOf(t, st, ctx, p)
	for _, a := range []struct{ name, cur string }{{"Cash", "KZT"}, {"Card", "USD"}} {
		if _, err := st.CreateAccount(ctx, u.ID, a.name, a.cur, 100_000_00); err != nil {
			t.Fatal(err)
		}
	}
	p.say("➖ Expense")
	p.sees("paid from which account?")
	p.tap("✖️ Cancel")
	p.say("➕ Income")
	p.sees("received on which account?")
	p.tap("✖️ Cancel")
	p.say("🔁 Transfer")
	p.sees("from which account?")
}

// A transfer between currencies: accounts and rate direction are tapped, the
// amount and the rate are typed.
func TestTransferBetweenCurrencies(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 111)
	u := userOf(t, st, ctx, p)
	kzt, _ := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 100_000_00)
	usd, _ := st.CreateAccount(ctx, u.ID, "Card", "USD", 0)

	p.say("🔁 Transfer")
	p.tap("💵 Cash · KZT")
	p.sees("How much to debit")
	p.say("54000")
	p.sees("Which account receives it?")
	p.tap("💳 Card · USD")
	p.sees("What rate did you exchange at?")
	p.tap("1 USD = ? KZT")
	p.say("470")
	p.sees("Crediting 114.89 USD")
	p.sees("your rate: 1 USD = 470 KZT")

	last, _ := st.LastTransactions(ctx, u.ID, 1)
	got := last[0]
	if got.Kind != storage.KindTransfer || got.AccountID != kzt.ID || got.Amount != 54_000_00 {
		t.Errorf("transfer = %+v, want 54000 KZT out of Cash", got)
	}
	if got.ToAccountID == nil || *got.ToAccountID != usd.ID || got.ToAmount == nil || *got.ToAmount != 114_89 {
		t.Errorf("credited = %v to %v, want 11489 into Card USD", got.ToAmount, got.ToAccountID)
	}
}

// Settings is a hub: preferences are chosen from buttons, no command needed.
func TestSettingsHub(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 112)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 0); err != nil {
		t.Fatal(err)
	}

	p.say("⚙️ Settings")
	for _, label := range []string{"💳 Accounts", "🏷 Categories", "🎯 Budgets", "🔁 Recurring",
		"💱 Main currency", "🕒 Timezone", "📈 Exchange rates", "🔍 Search", "🗑 Erase all my data"} {
		if !p.hasButton(label) {
			t.Errorf("Settings has no %q button", label)
		}
	}

	p.tap("🕒 Timezone")
	p.tap("Dubai")
	p.sees("Timezone: Asia/Dubai")

	p.tap("💱 Main currency")
	p.tap("USD")
	p.sees("Main currency: USD")

	p.tap("🔕 Monthly statement: on")
	p.sees("Monthly statement: off")

	got, _ := st.UserByID(ctx, u.ID)
	if got.Timezone != "Asia/Dubai" || got.MainCurrency != "USD" || got.MonthlyReport {
		t.Errorf("stored settings = %+v, want Dubai / USD / statement off", got)
	}
}

// A category is created by picking a ready-made one — and one already taken
// is not offered again.
func TestCategoryFromSuggestions(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 113)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 0); err != nil {
		t.Fatal(err)
	}

	p.say("⚙️ Settings")
	p.tap("🏷 Categories")
	p.sees("none yet — they appear as you name them")
	p.tap("➕ Expense")
	p.sees("expense category")
	p.say("☕ Coffee")
	p.sees("Category “☕ Coffee” created")

	cat, err := st.FindCategory(ctx, u.ID, storage.KindExpense, "coffee")
	if err != nil {
		t.Fatalf("Coffee was not created: %v", err)
	}
	if cat.Emoji != "☕" || cat.Name != "Coffee" {
		t.Errorf("category = %q %q, want ☕ and Coffee — the leading emoji becomes the icon", cat.Emoji, cat.Name)
	}

	// It is a button from now on.
	p.say("➖ Expense")
	p.say("100")
	p.sees("Category?")
	if !p.hasButton("☕ Coffee") {
		t.Errorf("a named category should become a button:\n%s", p.screen())
	}
}

// Missing the category you need while recording an operation is not a dead end:
// pick a new one and the operation carries on.
func TestNewCategoryMidOperation(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 114)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 50_000_00); err != nil {
		t.Fatal(err)
	}
	// Categories exist, but not the one this expense needs.
	withCategories(t, st, ctx, u, storage.KindExpense, "Groceries", "Transport")

	p.say("➖ Expense")
	p.say("3000")
	p.sees("Category?")
	p.tap("➕ New category")
	p.sees("New expense category")
	p.say("🐾 Pets")
	p.sees("Who was it paid to") // straight on to the next question
	p.tap("⏭ No source")
	p.sees("Expense recorded")
	p.sees("Category: 🐾 Pets")
	last, _ := st.LastTransactions(ctx, u.ID, 1)
	if last[0].CategoryName != "Pets" || last[0].Amount != 3_000_00 {
		t.Errorf("recorded %+v, want 3000 under Pets", last[0])
	}
}

// The bottom menu is the whole app: three ways to record, three places to go,
// and the debts.
func TestMainMenuLayout(t *testing.T) {
	m := mainMenu()
	want := [][]string{
		{"➖ Expense", "➕ Income", "🔁 Transfer"},
		{"📊 Reports", "💳 Accounts", "⚙️ Settings"},
		{"🤝 Debts"},
	}
	if len(m.Keyboard) != len(want) {
		t.Fatalf("menu has %d rows, want %d", len(m.Keyboard), len(want))
	}
	for i, row := range want {
		for j, label := range row {
			if got := m.Keyboard[i][j].Text; got != label {
				t.Errorf("menu[%d][%d] = %q, want %q", i, j, got, label)
			}
		}
	}
}

// The places already in use come first — adding a currency to one is the common
// case — and none is offered twice.
func TestSuggestPlaces(t *testing.T) {
	got := suggestPlaces([]string{"Kaspi Gold", "cash"})
	if got[0] != "Kaspi Gold" || got[1] != "cash" {
		t.Errorf("suggestions start with %v, want the existing places first", got[:2])
	}
	seen := map[string]bool{}
	for _, p := range got {
		if seen[strings.ToLower(p)] {
			t.Errorf("%q offered twice: %v", p, got)
		}
		seen[strings.ToLower(p)] = true
	}
	if !seen["card"] || !seen["deposit"] {
		t.Errorf("the usual places are missing: %v", got)
	}
}

// A place is not offered a currency it already holds.
func TestCurrencyOptionsSkipWhatIsHeld(t *testing.T) {
	got := currencyOptions("KZT", []string{"KZT", "EUR"})
	for _, c := range got {
		if c == "KZT" || c == "EUR" {
			t.Errorf("%s is already held but was offered: %v", c, got)
		}
	}
	if len(got) == 0 || got[0] != "USD" {
		t.Errorf("options = %v, want USD first once KZT is held", got)
	}
	// The main currency leads when the place holds nothing yet.
	if first := currencyOptions("USD", nil)[0]; first != "USD" {
		t.Errorf("options start with %q, want the main currency", first)
	}
}

func TestToggleCurrency(t *testing.T) {
	d := draft{Sources: []string{"KZT", "USD"}}
	if !toggleCurrency(&d, 1) || len(d.Currencies) != 1 || d.Currencies[0] != "USD" {
		t.Fatalf("first tap: %v", d.Currencies)
	}
	if !toggleCurrency(&d, 1) || len(d.Currencies) != 0 {
		t.Errorf("second tap should untick: %v", d.Currencies)
	}
	if toggleCurrency(&d, 5) {
		t.Error("an out-of-range option was accepted")
	}
}

func TestDayAndDateKeyboards(t *testing.T) {
	kb := dayKeyboard()
	days := 0
	for i, row := range kb.InlineKeyboard {
		for _, b := range row {
			if strings.HasPrefix(b.CallbackData, "recday:") {
				days++
			}
		}
		if i < 4 && len(row) != 7 {
			t.Errorf("calendar row %d has %d days, want 7", i, len(row))
		}
	}
	if days != 31 {
		t.Errorf("calendar offers %d days, want 31", days)
	}

	loc, _ := time.LoadLocation("Asia/Almaty")
	now := time.Date(2026, time.September, 2, 15, 30, 0, 0, loc)
	if got := dateDaysAgo(now, loc, 0); !got.Equal(now) {
		t.Errorf("today = %v, want the current moment", got)
	}
	if got := dateDaysAgo(now, loc, 3); got.Format("2006-01-02 15:04") != "2026-08-30 12:00" {
		t.Errorf("3 days ago = %v, want 2026-08-30 at noon (across a month boundary)", got)
	}
}

// A statement is asked for by tapping, and the numbers open onto the operations
// behind them and a CSV.
func TestStatementOnDemandWithOperationsAndCSV(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 115)
	u := userOf(t, st, ctx, p)
	acc, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 100_000_00)
	if err != nil {
		t.Fatal(err)
	}
	food, err := st.CreateCategory(ctx, u.ID, "Groceries", storage.KindExpense, "🛒")
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Asia/Almaty")
	now := time.Now().In(loc)
	lastMonth := time.Date(now.Year(), now.Month(), 1, 12, 0, 0, 0, loc).AddDate(0, -1, 0)
	for i, src := range []string{"Magnum", "Small"} {
		if _, err := st.CreateTransaction(ctx, storage.Transaction{
			UserID: u.ID, AccountID: acc.ID, CategoryID: &food.ID, Kind: storage.KindExpense,
			Amount: int64(i+1) * 1_000_00, Source: src, OccurredAt: lastMonth.AddDate(0, 0, 2+i),
		}); err != nil {
			t.Fatal(err)
		}
	}

	p.say("📊 Reports")
	p.tap("Last month")
	p.sees("Statement:")
	p.sees("Operations: 2")

	p.tap("🧾 Operations")
	p.sees("Operations: " + lastMonth.Format("January 2006"))
	p.sees("Magnum")
	p.sees("Small")
	// Oldest first, the way a statement reads.
	text := plain(p.tg.visible()[0].text)
	if strings.Index(text, "Magnum") > strings.Index(text, "Small") {
		t.Errorf("operations are not in chronological order:\n%s", text)
	}

	p.tap("📄 CSV")
	if docs := p.tg.documents(); len(docs) != 1 || !strings.HasSuffix(docs[0], ".csv") {
		t.Errorf("documents sent = %v, want one .csv", docs)
	}
}

// Any period can be asked for with taps alone: month, day, month, day.
func TestCustomRangeIsTapped(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 116)
	u := userOf(t, st, ctx, p)
	acc, _ := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 0)
	food, err := st.CreateCategory(ctx, u.ID, "Groceries", storage.KindExpense, "🛒")
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Asia/Almaty")
	now := time.Now().In(loc)
	start := time.Date(now.Year(), now.Month(), 1, 12, 0, 0, 0, loc).AddDate(0, -2, 0)

	// One operation inside the range, one just before it.
	for _, o := range []struct {
		src string
		at  time.Time
	}{{"Outside", start.AddDate(0, 0, 3)}, {"Inside", start.AddDate(0, 0, 8)}} {
		if _, err := st.CreateTransaction(ctx, storage.Transaction{
			UserID: u.ID, AccountID: acc.ID, CategoryID: &food.ID, Kind: storage.KindExpense,
			Amount: 500_00, Source: o.src, OccurredAt: o.at,
		}); err != nil {
			t.Fatal(err)
		}
	}

	p.say("📊 Reports")
	p.tap("📅 Custom range")
	p.sees("From which month?")
	p.tap(start.Format("Jan 2006"))
	p.sees("From which day")
	p.tap("6") // the 6th
	p.sees("To which month?")
	p.tap(start.Format("Jan 2006"))
	p.sees("To which day")
	if p.hasButton("5") {
		t.Error("a day before the first day was offered as the last day")
	}
	p.tap("15")
	p.sees("Statement:")
	p.sees(fmt.Sprintf("06.%s — 15.%s", start.Format("01.2006"), start.Format("01.2006")))
	p.sees("Operations: 1") // only the operation inside the range counts

	p.tap("🧾 Operations")
	p.sees("Inside")
	if strings.Contains(plain(p.tg.visible()[0].text), "Outside") {
		t.Error("an operation outside the range was listed")
	}
}

// A range can be a single day.
func TestCustomRangeJustOneDay(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 117)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 0); err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Asia/Almaty")
	month := time.Date(time.Now().In(loc).Year(), time.Now().In(loc).Month(), 1, 0, 0, 0, 0, loc)

	p.say("📊 Reports")
	p.tap("📅 Custom range")
	p.tap(month.Format("Jan 2006"))
	p.tap("1")
	p.tap("📌 Just 1 " + month.Format("Jan"))
	p.sees("Statement:")
	p.sees(fmt.Sprintf("01.%s — 01.%s", month.Format("01.2006"), month.Format("01.2006")))
}

// --- first-run setup ---

// A new user is asked for a timezone, a currency, and where the money is — and
// leaves with accounts holding what they said, ready to record operations.
func TestOnboardingWalkthrough(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 120)

	p.say("/start")
	p.sees("Step 1 of 3 · Timezone")
	p.tap("Dubai")
	p.sees("Step 2 of 3 · Your currency")
	p.tap("USD")
	p.sees("Step 3 of 3 · Where the money is")
	p.tap("💵 Cash")
	p.sees("Selected: Cash")
	p.tap("💳 Card")
	p.sees("Selected: Cash, Card")
	p.tap("Done ▶️")

	p.sees("Cash — which currencies does it hold?")
	p.tap("KZT")
	p.tap("Done ▶️")
	p.sees("Cash · KZT — how much is there right now?")
	p.say("100000")
	p.sees("💵 Cash · KZT — 100,000 KZT")

	p.sees("Card — which currencies does it hold?")
	p.tap("USD")
	p.tap("Done ▶️")
	p.say("500")
	p.sees("You're all set!")
	p.sees("In total: 100,000 KZT + 500 USD")
	p.sees("Timezone Asia/Dubai · main currency USD")

	u := userOf(t, st, ctx, p)
	if u.Timezone != "Asia/Dubai" || u.MainCurrency != "USD" || !u.Onboarded() {
		t.Errorf("user = %s / %s / onboarded %v, want Asia/Dubai / USD / true", u.Timezone, u.MainCurrency, u.Onboarded())
	}
	accs, _ := st.ListAccountsWithBalance(ctx, u.ID, false)
	got := map[string]int64{}
	for _, a := range accs {
		got[a.Name+" "+a.Currency] = a.Balance
	}
	if len(got) != 2 || got["Cash KZT"] != 100_000_00 || got["Card USD"] != 500_00 {
		t.Errorf("accounts = %v, want Cash in KZT 100000 and Card in USD 500", got)
	}

	// And the app is usable straight away.
	p.say("➖ Expense")
	p.sees("paid from which account?")
}

// The first message of any kind starts the setup; a place can be named by the user
// and a listed one skipped.
func TestOnboardingCustomPlaceAndSkippingOne(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 121)

	p.say("hello")
	p.sees("Step 1 of 3 · Timezone")
	p.tap("Keep Asia/Almaty")
	p.tap("Keep KZT")
	p.tap("✏️ Other place")
	p.sees("Type a name for the place")
	p.say("Kaspi Gold")
	p.sees("Selected: Kaspi Gold")
	p.tap("💵 Cash")
	p.tap("Done ▶️")

	// Set up in the order of the list, the user's own place after the usual ones.
	p.sees("Cash — which currencies does it hold?")
	p.tap("⏭ Skip this one")
	p.sees("Kaspi Gold — which currencies does it hold?")
	p.tap("KZT")
	p.tap("Done ▶️")
	p.tap("0️⃣ Start from zero")
	p.sees("You're all set!")

	u := userOf(t, st, ctx, p)
	accs, _ := st.ListAccounts(ctx, u.ID, false)
	if len(accs) != 1 || accs[0].Name != "Kaspi Gold" {
		t.Errorf("accounts = %+v, want only Kaspi Gold", accs)
	}
}

// The last step can be skipped — and skipped stays skipped.
func TestOnboardingCanBeSkipped(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 122)

	p.say("/start")
	p.tap("Keep Asia/Almaty")
	p.tap("Keep KZT")
	p.tap("⏭ Skip for now")
	p.sees("Setup skipped")

	u := userOf(t, st, ctx, p)
	if !u.Onboarded() {
		t.Error("a skipped setup should still count as done")
	}
	p.say("hello")
	p.sees("Tap a button to start") // not the setup all over again
	if p.hasButton("Dubai") {
		t.Error("the setup started again after being skipped")
	}
}

// Someone who already has an account was set up before this existed: no setup
// for them, and they are marked so it never comes up.
func TestOnboardingIsSkippedForExistingUsers(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 123)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 0); err != nil {
		t.Fatal(err)
	}
	p.say("/start")
	p.sees("Welcome back!")
	if p.hasButton("Dubai") {
		t.Error("an existing user was put through the setup")
	}
	if got, _ := st.UserByID(ctx, u.ID); !got.Onboarded() {
		t.Error("an existing user should have been marked as set up")
	}
}

// Ticking nothing is not an answer.
func TestOnboardingNeedsAPlaceOrASkip(t *testing.T) {
	st, _ := openBotStore(t)
	p := newPerson(t, st, 124)
	p.say("/start")
	p.tap("Keep Asia/Almaty")
	p.tap("Keep KZT")
	p.tap("Done ▶️")
	p.sees("Step 3 of 3 · Where the money is") // still here
	if strings.Contains(plain(p.tg.visible()[0].text), "which currency is it in?") {
		t.Error("the setup moved on without a single place chosen")
	}
}

// A place ticked twice is unticked; a typed duplicate ticks the listed one.
func TestTogglePlaceAndTypedDuplicate(t *testing.T) {
	d := draft{Sources: []string{"Cash", "Card"}}
	if !togglePlace(&d, 0) || len(d.Queue) != 1 || d.Queue[0] != "Cash" {
		t.Fatalf("first tap: %v", d.Queue)
	}
	if !togglePlace(&d, 0) || len(d.Queue) != 0 {
		t.Errorf("second tap should untick: %v", d.Queue)
	}
	if togglePlace(&d, 9) {
		t.Error("an out-of-range option was accepted")
	}
	if got := orderedBy([]string{"Cash", "Card", "Mine"}, []string{"Mine", "Cash"}); strings.Join(got, ",") != "Cash,Mine" {
		t.Errorf("orderedBy = %v, want Cash,Mine", got)
	}
}

// The database starts empty: no categories are seeded, so nobody carries a list
// they never chose. A category exists once it is actually picked.
func TestDatabaseStartsWithoutCategories(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 130)
	u := userOf(t, st, ctx, p)

	for _, kind := range []string{storage.KindExpense, storage.KindIncome} {
		cats, err := st.ListCategories(ctx, u.ID, kind)
		if err != nil {
			t.Fatal(err)
		}
		if len(cats) != 0 {
			t.Errorf("%s categories = %d, want none before any is picked", kind, len(cats))
		}
	}

	// The currency reference table, on the other hand, must be there — the
	// foreign keys and the default main currency depend on it.
	if _, err := st.Currency(ctx, "KZT"); err != nil {
		t.Errorf("KZT missing from the currency table: %v", err)
	}

	// First expense: the category is offered, created on the tap, and the
	// operation carries on to the source question.
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 10_000_00); err != nil {
		t.Fatal(err)
	}
	p.say("➖ Expense")
	p.say("700")
	p.sees("Your first expense category")
	p.say("🚕 Transport")
	p.sees("Who was it paid to")
	p.tap("⏭ No source")
	p.sees("Expense recorded")
	p.sees("Category: 🚕 Transport")

	cats, _ := st.ListCategories(ctx, u.ID, storage.KindExpense)
	if len(cats) != 1 || cats[0].Name != "Transport" {
		t.Errorf("categories after the first expense = %+v, want only Transport", cats)
	}

	// The second expense offers the category that now exists, rather than the
	// full list of what could be created.
	p.say("➖ Expense")
	p.say("800")
	p.sees("Category?")
	if !p.hasButton("🚕 Transport") || !p.hasButton("➕ New category") {
		t.Errorf("the existing category or the new-category button is missing:\n%s", p.screen())
	}
}

// Every offered category is offered once, with an icon, and the everyday ones come first.

// One place, several currencies: each keeps its own balance, they are grouped
// under the place, and an operation picks the currency it was paid in.
func TestOnePlaceHoldsSeveralCurrencies(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 140)

	p.say("/start")
	p.tap("Keep Asia/Almaty")
	p.tap("Keep KZT")
	p.tap("💵 Cash")
	p.tap("Done ▶️")

	// Cash holds tenge and dollars.
	p.sees("which currencies does it hold?")
	p.tap("KZT")
	p.tap("USD")
	p.sees("Selected: KZT, USD")
	p.tap("Done ▶️")

	p.sees("Cash · KZT — how much is there right now?")
	p.say("100000")
	p.sees("Cash · USD — how much is there right now?")
	p.say("500")
	p.sees("You're all set!")

	u := userOf(t, st, ctx, p)
	accs, err := st.ListAccountsWithBalance(ctx, u.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(accs) != 2 {
		t.Fatalf("accounts = %d, want 2 (one place, two currencies)", len(accs))
	}
	got := map[string]int64{}
	for _, a := range accs {
		if a.Name != "Cash" {
			t.Errorf("account named %q, want both under “Cash”", a.Name)
		}
		got[a.Currency] = a.Balance
	}
	if got["KZT"] != 100_000_00 || got["USD"] != 500_00 {
		t.Errorf("balances = %v, want KZT 100000 and USD 500", got)
	}

	// The accounts screen groups them under the one place.
	p.say("💳 Accounts")
	p.sees("💵 Cash")
	p.sees("• 100,000 KZT")
	p.sees("• 500 USD")

	// And an operation picks which of them paid.
	withCategories(t, st, ctx, u, storage.KindExpense, "Groceries")
	p.say("➖ Expense")
	p.sees("paid from which account?")
	if !p.hasButton("💵 Cash · KZT") || !p.hasButton("💵 Cash · USD") {
		t.Errorf("both currencies of the place should be offered:\n%s", p.screen())
	}
	p.tap("💵 Cash · USD")
	p.say("20")
	p.tap("🛒 Groceries")
	p.tap("⏭ No source")
	p.sees("Expense recorded")
	p.sees("20 USD")

	// Only the dollar balance moved.
	accs, _ = st.ListAccountsWithBalance(ctx, u.ID, false)
	for _, a := range accs {
		want := int64(100_000_00)
		if a.Currency == "USD" {
			want = 480_00
		}
		if a.Balance != want {
			t.Errorf("%s balance = %d, want %d", a.Currency, a.Balance, want)
		}
	}
}

// Renaming a place renames every currency kept there: it is one place.
func TestRenamingAPlaceRenamesAllItsCurrencies(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 141)
	u := userOf(t, st, ctx, p)
	for _, cur := range []string{"KZT", "USD"} {
		if _, err := st.CreateAccount(ctx, u.ID, "Card", cur, 0); err != nil {
			t.Fatal(err)
		}
	}

	p.say("💳 Accounts")
	p.tap("✏️ Edit")
	p.tap("💳 Card · KZT")
	p.tap("✏️ Rename place")
	p.say("Kaspi Gold")

	accs, _ := st.ListAccounts(ctx, u.ID, false)
	if len(accs) != 2 {
		t.Fatalf("accounts = %d, want 2", len(accs))
	}
	for _, a := range accs {
		if a.Name != "Kaspi Gold" {
			t.Errorf("%s account is named %q, want Kaspi Gold", a.Currency, a.Name)
		}
	}
}

// The same place cannot hold the same currency twice.
func TestPlaceCannotHoldACurrencyTwice(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 142)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 0); err == nil {
		t.Error("a second Cash/KZT account was accepted")
	}
	// Another currency at the same place is fine, as is the same currency elsewhere.
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "USD", 0); err != nil {
		t.Errorf("Cash/USD should be allowed: %v", err)
	}
	if _, err := st.CreateAccount(ctx, u.ID, "Card", "KZT", 0); err != nil {
		t.Errorf("Card/KZT should be allowed: %v", err)
	}
	held, err := st.PlaceCurrencies(ctx, u.ID, "cash")
	if err != nil || len(held) != 2 {
		t.Errorf("PlaceCurrencies = %v, %v; want two currencies", held, err)
	}
}

// The accounts screen groups currencies under their place and totals per currency.
func TestAccountsListGroupsByPlace(t *testing.T) {
	accs := []storage.Account{
		{Name: "Cash", Currency: "KZT", Decimals: 2, Balance: 100_000_00},
		{Name: "Cash", Currency: "USD", Decimals: 2, Balance: 500_00},
		{Name: "Card", Currency: "KZT", Decimals: 2, Balance: 50_000_00},
	}
	got := plain(accountsList(accs))
	for _, want := range []string{"💵 Cash", "• 100,000 KZT", "• 500 USD", "💳 Card — 50,000 KZT", "Total: 150,000 KZT + 500 USD"} {
		if !strings.Contains(got, want) {
			t.Errorf("the list is missing %q:\n%s", want, got)
		}
	}
	// A single place needs no total line.
	if single := plain(accountsList(accs[:1])); strings.Contains(single, "Total") {
		t.Errorf("one place should not get a total:\n%s", single)
	}
}

// --- correcting a mistyped amount ---

// A plain expense: the amount is corrected and the balance follows.
func TestCorrectAmountOfAnExpense(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 150)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 100_000_00); err != nil {
		t.Fatal(err)
	}
	withCategories(t, st, ctx, u, storage.KindExpense, "Groceries")

	p.say("➖ Expense")
	p.say("50000") // one zero too many
	p.tap("🛒 Groceries")
	p.tap("⏭ No source")
	p.sees("Expense recorded")

	p.tap("💰 Amount")
	p.sees("Currently 50,000 KZT")
	p.say("5000")
	p.sees("Amount corrected")
	p.sees("5,000 KZT")

	last, _ := st.LastTransactions(ctx, u.ID, 1)
	if last[0].Amount != 5_000_00 {
		t.Errorf("amount = %d, want 500000", last[0].Amount)
	}
	accs, _ := st.ListAccountsWithBalance(ctx, u.ID, false)
	if accs[0].Balance != 95_000_00 {
		t.Errorf("balance = %d, want 9500000 — the correction should move it", accs[0].Balance)
	}
}

// An operation paid in another currency: the purchase is corrected and what left
// the account is recomputed at the rate already recorded, which stays untouched.
func TestCorrectAmountOfAConvertedOperation(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 151)
	u := userOf(t, st, ctx, p)
	usd, err := st.CreateAccount(ctx, u.ID, "Card", "USD", 1_000_00)
	if err != nil {
		t.Fatal(err)
	}
	food, err := st.CreateCategory(ctx, u.ID, "Groceries", storage.KindExpense, "🛒")
	if err != nil {
		t.Fatal(err)
	}
	original := int64(5_000_00) // 5 000 KZT paid with the dollar card at 1 USD = 470 KZT
	rate := 1.0 / 470.0
	saved, err := st.CreateTransaction(ctx, storage.Transaction{
		UserID: u.ID, AccountID: usd.ID, CategoryID: &food.ID, Kind: storage.KindExpense,
		Amount: 10_64, OriginalAmount: &original, OriginalCurrency: "KZT", Rate: &rate,
		OccurredAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	p.say("/last")
	p.tapFirstOperation() // the list opens the card, so older operations are reachable too
	p.sees("Expense recorded")
	p.tap("💰 Amount")
	p.sees("Currently 5,000 KZT") // the figure that was typed, not the debit
	p.sees("recompute what left") // and it says what follows from it
	p.say("7500")
	p.sees("Amount corrected")
	p.sees("Purchase: 7,500 KZT")

	got, _ := st.Transaction(ctx, u.ID, saved.ID)
	if got.OriginalAmount == nil || *got.OriginalAmount != 7_500_00 {
		t.Errorf("purchase = %v, want 750000", got.OriginalAmount)
	}
	if got.Amount != 15_96 { // 7500 / 470 = 15.957…
		t.Errorf("debited = %d, want 1596", got.Amount)
	}
	// The rate is left alone — it is what the bank applied. (It comes back rounded
	// to the 8 decimals the column keeps, so compare with a tolerance.)
	if got.Rate == nil || math.Abs(*got.Rate-rate) > 1e-8 {
		t.Errorf("rate = %v, want it left alone at %v", got.Rate, rate)
	}
}

// A transfer between currencies: the debit is corrected and the credit follows.
func TestCorrectAmountOfATransfer(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 152)
	u := userOf(t, st, ctx, p)
	kzt, _ := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 200_000_00)
	usd, _ := st.CreateAccount(ctx, u.ID, "Card", "USD", 0)

	p.say("🔁 Transfer")
	p.tap("💵 Cash · KZT")
	p.say("54000")
	p.tap("💳 Card · USD")
	p.tap("1 USD = ? KZT")
	p.say("470")
	p.sees("Transfer recorded")

	p.tap("💰 Amount")
	p.sees("Currently 54,000 KZT")
	p.sees("recompute what arrives")
	p.say("94000")
	p.sees("Amount corrected")

	last, _ := st.LastTransactions(ctx, u.ID, 1)
	got := last[0]
	if got.Amount != 94_000_00 {
		t.Errorf("debited = %d, want 9400000", got.Amount)
	}
	if got.ToAmount == nil || *got.ToAmount != 200_00 { // 94000 / 470 = 200
		t.Errorf("credited = %v, want 20000", got.ToAmount)
	}
	// Both balances follow the correction.
	accs, _ := st.ListAccountsWithBalance(ctx, u.ID, false)
	for _, a := range accs {
		want := int64(106_000_00) // 200000 − 94000
		if a.ID == usd.ID {
			want = 200_00
		}
		if a.Balance != want {
			t.Errorf("%s balance = %d, want %d", a.Currency, a.Balance, want)
		}
	}
	_ = kzt
}

// Words and zero are refused, and the operation is left as it was.
func TestCorrectAmountRejectsNonsense(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 153)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 0); err != nil {
		t.Fatal(err)
	}
	withCategories(t, st, ctx, u, storage.KindExpense, "Groceries")

	p.say("➖ Expense")
	p.say("1000")
	p.tap("🛒 Groceries")
	p.tap("⏭ No source")
	p.tap("💰 Amount")

	for _, bad := range []string{"a lot", "0", "-500"} {
		p.say(bad)
		p.sees("as a number")
	}
	last, _ := st.LastTransactions(ctx, u.ID, 1)
	if last[0].Amount != 1_000_00 {
		t.Errorf("amount = %d, want it unchanged at 100000", last[0].Amount)
	}

	p.say("1500") // and a good one still lands
	p.sees("Amount corrected")
	last, _ = st.LastTransactions(ctx, u.ID, 1)
	if last[0].Amount != 1_500_00 {
		t.Errorf("amount = %d, want 150000", last[0].Amount)
	}
}

// Correcting an amount into a budget's red zone warns, just like entering it would.
func TestCorrectAmountWarnsAboutTheBudget(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 154)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 500_000_00); err != nil {
		t.Fatal(err)
	}
	food, err := st.CreateCategory(ctx, u.ID, "Groceries", storage.KindExpense, "🛒")
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Asia/Almaty")
	now := time.Now().In(loc)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	if err := st.SetBudget(ctx, u.ID, food.ID, month, "KZT", 20_000_00); err != nil {
		t.Fatal(err)
	}

	p.say("➖ Expense")
	p.say("5000")
	p.tap("🛒 Groceries")
	p.tap("⏭ No source")

	p.tap("💰 Amount")
	p.say("30000") // over the 20 000 limit
	p.sees("Amount corrected")
	p.sees("Limit for “Groceries” exceeded")
}

// An operation already in the books can be opened from the list and corrected —
// not only the one just entered.
func TestOlderOperationIsReachableFromTheList(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 155)
	u := userOf(t, st, ctx, p)
	acc, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 100_000_00)
	if err != nil {
		t.Fatal(err)
	}
	food, err := st.CreateCategory(ctx, u.ID, "Groceries", storage.KindExpense, "🛒")
	if err != nil {
		t.Fatal(err)
	}
	// Recorded days ago, so there is no card left in the chat.
	old, err := st.CreateTransaction(ctx, storage.Transaction{
		UserID: u.ID, AccountID: acc.ID, CategoryID: &food.ID, Kind: storage.KindExpense,
		Amount: 9_000_00, Source: "Magnum", OccurredAt: time.Now().AddDate(0, 0, -4),
	})
	if err != nil {
		t.Fatal(err)
	}

	p.say("/last")
	p.sees("Tap one to correct")
	p.tapFirstOperation()
	p.sees("Expense recorded")
	p.sees("9,000 KZT")

	p.tap("💰 Amount")
	p.say("900")
	p.sees("Amount corrected")

	got, _ := st.Transaction(ctx, u.ID, old.ID)
	if got.Amount != 900_00 {
		t.Errorf("amount = %d, want 90000", got.Amount)
	}
	// Correcting the amount leaves everything else alone.
	if got.Source != "Magnum" || got.CategoryName != "Groceries" ||
		got.OccurredAt.Format("2006-01-02") != old.OccurredAt.Format("2006-01-02") {
		t.Errorf("the correction changed more than the amount: %+v", got)
	}
}

// Nothing is suggested: the categories are the user's own words, and they become
// buttons in order of use, most-used first.
func TestCategoriesAreOnlyTheUsersOwnAndSortedByUse(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 160)
	u := userOf(t, st, ctx, p)
	acc, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 500_000_00)
	if err != nil {
		t.Fatal(err)
	}

	// First expense: nothing to pick from, so it is named.
	p.say("➖ Expense")
	p.say("1000")
	p.sees("Your first expense category")
	if p.hasButton("🛒 Groceries") || p.hasButton("✏️ Other name") {
		t.Errorf("no category should be offered up front:\n%s", p.screen())
	}
	p.say("Taxi")
	p.tap("⏭ No source")
	p.sees("Expense recorded")

	// A second one, named the same way.
	p.say("➖ Expense")
	p.say("2000")
	p.sees("Category?") // Taxi is a button now
	p.tap("➕ New category")
	p.say("🛒 Groceries")
	p.tap("⏭ No source")

	// Use Groceries twice more, so it becomes the most-used.
	for i := 0; i < 2; i++ {
		if _, err := st.CreateTransaction(ctx, storage.Transaction{
			UserID: u.ID, AccountID: acc.ID, Kind: storage.KindExpense, Amount: 300_00,
			CategoryID: categoryID(t, st, ctx, u, storage.KindExpense, "groceries"),
			OccurredAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	cats, err := st.ListCategories(ctx, u.ID, storage.KindExpense)
	if err != nil {
		t.Fatal(err)
	}
	if len(cats) != 2 {
		t.Fatalf("categories = %d, want the two that were named", len(cats))
	}
	if cats[0].Name != "Groceries" {
		t.Errorf("first category = %q, want Groceries — the most used one leads", cats[0].Name)
	}
	if cats[0].Emoji != "🛒" {
		t.Errorf("emoji = %q, want 🛒 from the name it was given", cats[0].Emoji)
	}
}

func categoryID(t *testing.T, st *storage.Store, ctx context.Context, u storage.User, kind, name string) *int64 {
	t.Helper()
	c, err := st.FindCategory(ctx, u.ID, kind, name)
	if err != nil {
		t.Fatalf("FindCategory %s: %v", name, err)
	}
	return &c.ID
}

// The card shows the balance after the operation — the figure that matters next.
func TestCardShowsBalanceAfterTheOperation(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 161)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 100_000_00); err != nil {
		t.Fatal(err)
	}
	withCategories(t, st, ctx, u, storage.KindExpense, "Groceries")

	// The amount question already says what is on the account.
	p.say("➖ Expense")
	p.sees("100,000 KZT")
	p.say("5000")
	p.tap("🛒 Groceries")
	p.tap("⏭ No source")

	p.sees("Expense recorded")
	p.sees("Account: 💵 Cash · KZT")
	p.sees("Balance: 95,000 KZT")

	// And it follows a correction.
	p.tap("💰 Amount")
	p.say("10000")
	p.sees("Balance: 90,000 KZT")
}

// The first-run setup says where in the three steps you are.
func TestOnboardingShowsProgress(t *testing.T) {
	st, _ := openBotStore(t)
	p := newPerson(t, st, 162)
	p.say("/start")
	p.sees("Step 1 of 3 · Timezone")
	p.tap("Keep Asia/Almaty")
	p.sees("Step 2 of 3 · Your currency")
	p.tap("Keep KZT")
	p.sees("Step 3 of 3 · Where the money is")
}

// --- erasing everything ---

// Erasing takes two deliberate taps, wipes everything the person has, and leaves
// the bot ready to set them up again from scratch.
func TestEraseEverythingAndStartOver(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 170)
	u := userOf(t, st, ctx, p)

	acc, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 100_000_00)
	if err != nil {
		t.Fatal(err)
	}
	food, err := st.CreateCategory(ctx, u.ID, "Groceries", storage.KindExpense, "🛒")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateTransaction(ctx, storage.Transaction{
		UserID: u.ID, AccountID: acc.ID, CategoryID: &food.ID, Kind: storage.KindExpense,
		Amount: 5_000_00, Source: "Magnum", OccurredAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Asia/Almaty")
	month := time.Date(time.Now().In(loc).Year(), time.Now().In(loc).Month(), 1, 0, 0, 0, 0, loc)
	if err := st.SetBudget(ctx, u.ID, food.ID, month, "KZT", 20_000_00); err != nil {
		t.Fatal(err)
	}

	// Someone else's books must survive all of this.
	other := newPerson(t, st, 171)
	otherUser := userOf(t, st, ctx, other)
	if _, err := st.CreateAccount(ctx, otherUser.ID, "Cash", "KZT", 7_000_00); err != nil {
		t.Fatal(err)
	}

	p.say("⚙️ Settings")
	p.tap("🗑 Erase all my data")
	p.sees("Erase everything?")
	p.sees("1 operation")
	p.sees("1 account")
	p.sees("cannot be undone")
	if !p.hasButton("📄 Export everything first") {
		t.Error("the CSV should be offered before erasing")
	}

	// Backing out leaves everything alone.
	p.tap("◀️ Keep my data")
	p.sees("Settings")
	if left, _ := st.LastTransactions(ctx, u.ID, 1); len(left) != 1 {
		t.Fatalf("backing out deleted data: %d operations left", len(left))
	}

	// Going through with it takes a second, differently worded tap.
	p.tap("🗑 Erase all my data")
	p.tap("🗑 Erase everything")
	p.sees("Last check")
	p.tap("🗑 Yes, erase it all")
	p.sees("everything is erased")

	// Nothing of theirs is left anywhere.
	if _, err := st.UserByID(ctx, u.ID); err == nil {
		t.Error("the user row survived")
	}
	for name, count := range map[string]int{
		"accounts":     countRows(t, st, ctx, "accounts", u.ID),
		"transactions": countRows(t, st, ctx, "transactions", u.ID),
		"categories":   countRows(t, st, ctx, "categories", u.ID),
		"budgets":      countRows(t, st, ctx, "budgets", u.ID),
		"user_states":  countRows(t, st, ctx, "user_states", u.ID),
	} {
		if count != 0 {
			t.Errorf("%s: %d rows left", name, count)
		}
	}

	// The other person is untouched.
	otherAccs, err := st.ListAccountsWithBalance(ctx, otherUser.ID, false)
	if err != nil || len(otherAccs) != 1 || otherAccs[0].Balance != 7_000_00 {
		t.Errorf("another user's data was affected: %v %v", otherAccs, err)
	}

	// And the next message starts them over from the first question.
	p.say("hello")
	p.sees("Step 1 of 3 · Timezone")
}

// countRows counts what is left of a user in one table.
func countRows(t *testing.T, st *storage.Store, ctx context.Context, table string, userID int64) int {
	t.Helper()
	column := "user_id"
	var n int
	if err := st.Pool().QueryRow(ctx,
		"SELECT COUNT(*) FROM "+table+" WHERE "+column+" = $1", userID).Scan(&n); err != nil {
		t.Fatalf("counting %s: %v", table, err)
	}
	return n
}

// With nothing recorded, erasing is still offered — it resets the settings — but
// it doesn't pretend there is data to lose.
func TestEraseWithNothingRecorded(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 172)
	u := userOf(t, st, ctx, p)

	// Someone who went through the setup but skipped adding accounts.
	p.say("/start")
	p.tap("Keep Asia/Almaty")
	p.tap("Keep KZT")
	p.tap("⏭ Skip for now")

	p.say("/erase")
	p.sees("There is nothing recorded yet")
	if p.hasButton("📄 Export everything first") {
		t.Error("nothing to export, so the button should not be there")
	}
	p.tap("🗑 Erase everything")
	p.tap("🗑 Yes, erase it all")
	p.sees("everything is erased")

	if _, err := st.UserByID(ctx, u.ID); err == nil {
		t.Error("the user row survived")
	}
}

// A half-finished dialog doesn't survive the erasure either.
func TestEraseClearsAnUnfinishedDialog(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 173)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 0); err != nil {
		t.Fatal(err)
	}

	p.say("➖ Expense") // leaves the bot waiting for an amount
	if state, _, _ := st.LoadState(ctx, u.ID); state == "" {
		t.Fatal("expected a dialog in progress")
	}

	p.say("/erase")
	p.tap("🗑 Erase everything")
	p.tap("🗑 Yes, erase it all")

	// The state belonged to a user that no longer exists.
	if n := countRows(t, st, ctx, "user_states", u.ID); n != 0 {
		t.Errorf("user_states: %d rows left", n)
	}
	p.say("500") // would have been read as the amount before
	p.sees("Step 1 of 3 · Timezone")
}

func TestPlural(t *testing.T) {
	cases := []struct {
		n    int
		word string
		want string
	}{
		{1, "operation", "operation"},
		{2, "operation", "operations"},
		{1, "category", "category"},
		{3, "category", "categories"},
	}
	for _, c := range cases {
		if got := plural(c.n, c.word); got != c.want {
			t.Errorf("plural(%d, %q) = %q, want %q", c.n, c.word, got, c.want)
		}
	}
}

// --- searching, repeating, changing everything ---

// Search finds operations by a remembered word and by amount, and sums what it found.
func TestSearch(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 180)
	u := userOf(t, st, ctx, p)
	acc, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 500_000_00)
	if err != nil {
		t.Fatal(err)
	}
	food, err := st.CreateCategory(ctx, u.ID, "Groceries", storage.KindExpense, "🛒")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range []struct {
		amount int64
		source string
	}{{5_000_00, "Magnum"}, {70_000_00, "Magnum"}, {1_200_00, "Small"}} {
		if _, err := st.CreateTransaction(ctx, storage.Transaction{
			UserID: u.ID, AccountID: acc.ID, CategoryID: &food.ID, Kind: storage.KindExpense,
			Amount: o.amount, Source: o.source, OccurredAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// By word.
	p.say("/search magnum")
	p.sees("2 found")
	p.sees("Net: −75,000 KZT")
	if strings.Contains(plain(p.tg.visible()[0].text), "Small") {
		t.Error("a non-matching operation was listed")
	}

	// By amount.
	p.say("/search > 50000")
	p.sees("1 found")
	p.sees("70,000 KZT")

	// Both at once.
	p.say("/search magnum < 10000")
	p.sees("1 found")
	p.sees("5,000 KZT")

	// Nothing matching says so, and offers another go.
	p.say("/search nonsense")
	p.sees("Nothing matched")
	if !p.hasButton("🔍 Search again") {
		t.Error("no way to search again")
	}

	// And the results open their cards.
	p.say("/search magnum")
	p.tapFirstOperation()
	p.sees("Expense recorded")
}

func TestParseSearch(t *testing.T) {
	cases := []struct {
		line string
		text string
		min  int64
		max  int64
	}{
		{"magnum", "magnum", 0, 0},
		{"> 50000", "", 50_000_00, 0},
		{">50000", "", 50_000_00, 0},
		{"< 2000", "", 0, 2_000_00},
		{"1000-5000", "", 1_000_00, 5_000_00},
		{"coffee > 500", "coffee", 500_00, 0},
		{"del papa 1000-5000", "del papa", 1_000_00, 5_000_00},
		{"", "", 0, 0},
		{"-5000", "-5000", 0, 0}, // a leading minus is text, not a range
	}
	for _, c := range cases {
		got := parseSearch(c.line, 2)
		if got.Text != c.text || got.Min != c.min || got.Max != c.max {
			t.Errorf("parseSearch(%q) = %+v, want text=%q min=%d max=%d", c.line, got, c.text, c.min, c.max)
		}
	}
}

// Repeating an operation carries over everything but the amount.
func TestRepeatOperation(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 181)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 100_000_00); err != nil {
		t.Fatal(err)
	}
	withCategories(t, st, ctx, u, storage.KindExpense, "Coffee")

	p.say("➖ Expense")
	p.say("900")
	p.tap("☕ Coffee")
	p.say("Starbucks")
	p.sees("Expense recorded")

	p.tap("🔁 Again")
	p.sees("☕ Coffee · Starbucks") // the same again, only the amount is asked
	p.say("1200")
	p.sees("Expense recorded")

	txs, _ := st.LastTransactions(ctx, u.ID, 2)
	if len(txs) != 2 {
		t.Fatalf("operations = %d, want 2", len(txs))
	}
	if txs[0].Amount != 1_200_00 || txs[0].CategoryName != "Coffee" || txs[0].Source != "Starbucks" {
		t.Errorf("the repeat = %+v, want 1200 under Coffee from Starbucks", txs[0])
	}
}

// Category and account of a recorded operation can both be changed.
func TestChangeCategoryAndAccount(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 182)
	u := userOf(t, st, ctx, p)
	cash, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 100_000_00)
	if err != nil {
		t.Fatal(err)
	}
	card, err := st.CreateAccount(ctx, u.ID, "Card", "KZT", 50_000_00)
	if err != nil {
		t.Fatal(err)
	}
	withCategories(t, st, ctx, u, storage.KindExpense, "Groceries", "Transport")

	p.say("➖ Expense")
	p.tap("💵 Cash · KZT")
	p.say("3000")
	p.tap("🛒 Groceries")
	p.tap("⏭ No source")

	// Wrong category.
	p.tap("🏷 Category")
	p.sees("Which category does this belong to?")
	p.tap("🚕 Transport")
	p.sees("Category: 🚕 Transport")

	// Wrong account — same currency, so the amount stands.
	p.tap("💳 Account")
	p.sees("Which account did it go through?")
	p.tap("💳 Card · KZT")
	p.sees("Account: 💳 Card · KZT")

	last, _ := st.LastTransactions(ctx, u.ID, 1)
	if last[0].AccountID != card.ID || last[0].CategoryName != "Transport" {
		t.Errorf("operation = account %d / %s, want account %d / Transport",
			last[0].AccountID, last[0].CategoryName, card.ID)
	}
	if last[0].Amount != 3_000_00 {
		t.Errorf("amount = %d, want it unchanged", last[0].Amount)
	}
	// Both balances followed the move.
	accs, _ := st.ListAccountsWithBalance(ctx, u.ID, false)
	for _, a := range accs {
		want := int64(100_000_00)
		if a.ID == card.ID {
			want = 47_000_00
		}
		if a.Balance != want {
			t.Errorf("%s balance = %d, want %d", a.Name, a.Balance, want)
		}
	}
	_ = cash
}

// Moving an operation to an account in another currency asks for the amount
// again — the old number means nothing there.
func TestMovingToAnotherCurrencyAsksForTheAmount(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 183)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 100_000_00); err != nil {
		t.Fatal(err)
	}
	usd, err := st.CreateAccount(ctx, u.ID, "Card", "USD", 1_000_00)
	if err != nil {
		t.Fatal(err)
	}
	withCategories(t, st, ctx, u, storage.KindExpense, "Groceries")

	p.say("➖ Expense")
	p.tap("💵 Cash · KZT")
	p.say("5000")
	p.tap("🛒 Groceries")
	p.tap("⏭ No source")

	p.tap("💳 Account")
	p.tap("💳 Card · USD")
	p.sees("needs restating")
	p.say("11")
	p.sees("Amount corrected")

	last, _ := st.LastTransactions(ctx, u.ID, 1)
	if last[0].AccountID != usd.ID || last[0].Amount != 11_00 {
		t.Errorf("operation = account %d amount %d, want account %d amount 1100",
			last[0].AccountID, last[0].Amount, usd.ID)
	}
}

// The evening nudge goes to someone who recorded nothing, once a day, and only
// when they want it.
func TestEveningReminderSetting(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 184)
	u := userOf(t, st, ctx, p)
	if !u.DailyReminder {
		t.Error("the reminder should be on by default")
	}
	if _, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 0); err != nil {
		t.Fatal(err)
	}
	p.say("⚙️ Settings")
	p.sees("Evening reminder: on (21:00, if nothing recorded)")
	p.tap("🔕 Evening reminder: on")
	p.sees("Evening reminder: off")

	got, _ := st.UserByID(ctx, u.ID)
	if got.DailyReminder {
		t.Error("the setting was not saved")
	}
}

// The statement offers a prompt to hand to an AI, with the CSV.
func TestAIPromptIsOffered(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 185)
	u := userOf(t, st, ctx, p)
	acc, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 100_000_00)
	if err != nil {
		t.Fatal(err)
	}
	food, err := st.CreateCategory(ctx, u.ID, "Groceries", storage.KindExpense, "🛒")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateTransaction(ctx, storage.Transaction{
		UserID: u.ID, AccountID: acc.ID, CategoryID: &food.ID, Kind: storage.KindExpense,
		Amount: 5_000_00, Source: "Magnum", OccurredAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	p.say("/month")
	p.sees("Statement:")
	p.tap("🤖 Prompt for AI analysis")
	p.sees("For your AI")

	// The CSV goes with it, and the prompt itself arrives as its own message.
	if docs := p.tg.documents(); len(docs) != 1 {
		t.Errorf("documents sent = %v, want the CSV", docs)
	}
	var prompt string
	for _, m := range p.tg.visible() {
		if strings.Contains(m.text, "Below is my personal spending data") {
			prompt = m.text
		}
	}
	if prompt == "" {
		t.Fatalf("the prompt was not sent:\n%s", p.screen())
	}
	for _, must := range []string{
		"Transfers are not income or expense", // the mistake an AI would otherwise make
		"Keep currencies apart",
		"my own tracker",
		"KZT",
	} {
		if !strings.Contains(prompt, must) {
			t.Errorf("the prompt does not explain %q", must)
		}
	}
	if strings.Contains(prompt, "<b>") || strings.Contains(prompt, "&amp;") {
		t.Error("the prompt should be plain text — it is meant to be copied")
	}
}

// messageWith finds the newest message carrying a button with this data.
func (p *person) messageWith(data string) int {
	p.t.Helper()
	for _, m := range p.tg.visible() {
		for _, row := range m.buttons {
			for _, bt := range row {
				if bt.CallbackData == data {
					return m.id
				}
			}
		}
	}
	p.t.Fatalf("no button with data %q.\n%s", data, p.screen())
	return 0
}

// pressAt presses a button on a given message, as a client does even after the
// bot has moved on — a double tap, or a tap on an old message.
func (p *person) pressAt(msgID int, data string) {
	p.b.onCallback(context.Background(), nil, &models.Update{CallbackQuery: &models.CallbackQuery{
		ID: "cb", From: *p.user(), Data: data,
		Message: models.MaybeInaccessibleMessage{
			Message: &models.Message{ID: msgID, Chat: models.Chat{ID: p.tgID}},
		},
	}})
}

// Two quick taps on the last button record the operation once, and the same
// button pressed later on the old message does nothing.
func TestDoubleTapRecordsOnce(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 191)
	u := userOf(t, st, ctx, p)
	acc, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 100_000_00)
	if err != nil {
		t.Fatal(err)
	}
	withCategories(t, st, ctx, u, storage.KindExpense, "Groceries")
	cat := categoryID(t, st, ctx, u, storage.KindExpense, "Groceries")
	if _, err := st.CreateTransaction(ctx, storage.Transaction{UserID: u.ID, AccountID: acc.ID,
		CategoryID: cat, Kind: storage.KindExpense, Amount: 100, Source: "Magnum",
		OccurredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	p.say("➖ Expense")
	p.say("500")
	p.tap("🛒 Groceries")
	p.sees("Who was it paid to")

	p.tg.latency = 30 * time.Millisecond
	msg := p.messageWith("src:i0")
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); p.pressAt(msg, "src:i0") }()
	}
	wg.Wait()
	p.pressAt(msg, "src:i0")                      // later, on the old message
	p.pressAt(msg-1, "pickcat:"+fmt.Sprint(*cat)) // an older step of the same dialog

	txs, _ := st.LastTransactions(ctx, u.ID, 10)
	if len(txs) != 2 {
		t.Fatalf("operations = %d, want 2 (the earlier one and this one)", len(txs))
	}
}

// Categories set up from the categories screen come one after another: each
// name is followed by the next, until Done.
func TestAddingSeveralCategoriesInARow(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 192)
	u := userOf(t, st, ctx, p)
	if err := st.MarkOnboarded(ctx, u.ID); err != nil {
		t.Fatal(err)
	}

	p.say("/categories")
	p.tap("➕ Expense")
	p.say("🛒 Groceries")
	p.sees("Send the next name to add another")
	p.say("🚕 Transport")
	p.say("Coffee")
	p.say("coffee") // a duplicate is refused, and the next name still works
	p.sees("already have a category")
	p.say("Pets")
	p.tap("✅ Done")
	p.sees("Expense categories")

	cats, _ := st.ListCategories(ctx, u.ID, storage.KindExpense)
	if len(cats) != 4 {
		t.Fatalf("categories = %d, want 4", len(cats))
	}
	p.say("Not a category") // after Done, text is no longer a name
	if cats, _ := st.ListCategories(ctx, u.ID, storage.KindExpense); len(cats) != 4 {
		t.Errorf("text after Done created a category")
	}
}
