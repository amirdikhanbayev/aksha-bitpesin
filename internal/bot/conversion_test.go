package bot

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	tg "github.com/go-telegram/bot"

	"aksha-bitpesin/internal/money"
)

// A 5 000 KZT purchase paid from a USD account at the bank's 1 USD = 470 KZT.
func TestConversionArithmetic(t *testing.T) {
	const (
		kztDecimals = 2
		usdDecimals = 2
		purchase    = 5_000_00 // 5 000,00 KZT
		debited     = 10_64    // 10,64 USD
	)

	// The user typed the rate as "1 USD = 470 KZT", i.e. in the account's direction.
	rate := normalizeRate(470, rateInverse)
	if got := money.Convert(purchase, kztDecimals, usdDecimals, rate); got != debited {
		t.Errorf("debited = %d, want %d", got, debited)
	}

	// The same purchase, but the user typed the rate the other way round.
	rateOp := normalizeRate(1.0/470.0, rateDirect)
	if got := money.Convert(purchase, kztDecimals, usdDecimals, rateOp); got != debited {
		t.Errorf("debited (op direction) = %d, want %d", got, debited)
	}

	// The reverse case: a 20 USD purchase paid from a tenge account.
	rateKzt := normalizeRate(470, rateDirect) // 1 USD = 470 KZT, USD being the operation currency
	if got := money.Convert(20_00, usdDecimals, kztDecimals, rateKzt); got != 9_400_00 {
		t.Errorf("KZT debited = %d, want 940000", got)
	}
}

func TestRateLineReadsNaturally(t *testing.T) {
	cases := []struct {
		op, acc string
		rate    float64
		want    string
	}{
		// 0,00213 USD per tenge should read as the quote people actually use.
		{"KZT", "USD", 1.0 / 470.0, "1 USD = 470 KZT"},
		{"USD", "KZT", 470, "1 USD = 470 KZT"},
		{"EUR", "KZT", 505.5, "1 EUR = 505.5 KZT"}, // rates are quoted with a dot, amounts with a comma
	}
	for _, c := range cases {
		if got := rateLine(c.op, c.acc, c.rate); got != c.want {
			t.Errorf("rateLine(%s, %s, %v) = %q, want %q", c.op, c.acc, c.rate, got, c.want)
		}
	}
}

// A transfer between accounts in different currencies: 54 000 KZT exchanged at
// the kiosk's 1 USD = 470 KZT lands as 114.89 USD, whichever way round the user
// types that rate.
func TestTransferRateDirections(t *testing.T) {
	const (
		kztDecimals = 2
		usdDecimals = 2
		debited     = 54_000_00
	)

	// "1 USD = 470 KZT" — the target unit first, so the inverse direction.
	rate := normalizeRate(470, rateInverse)
	credited := money.Convert(debited, kztDecimals, usdDecimals, rate)
	if credited != 114_89 {
		t.Errorf("credited = %d, want 11489", credited)
	}

	// The same exchange typed as "1 KZT = 0,00212766 USD".
	rateDir := normalizeRate(1.0/470.0, rateDirect)
	if got := money.Convert(debited, kztDecimals, usdDecimals, rateDir); got != credited {
		t.Errorf("credited (direct direction) = %d, want %d", got, credited)
	}

	// However it was entered, it is shown back the way people quote it.
	if got := rateLine("KZT", "USD", rate); got != "1 USD = 470 KZT" {
		t.Errorf("rateLine = %q, want \"1 USD = 470 KZT\"", got)
	}
}

// A failed send is retried once, and only when a second attempt could work.
func TestRetryDelay(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		retry bool
		wait  time.Duration
	}{
		{"rate limited", &tg.TooManyRequestsError{RetryAfter: 3}, true, 3 * time.Second},
		{"rate limited without a delay", &tg.TooManyRequestsError{}, true, time.Second},
		{"asked to wait too long", &tg.TooManyRequestsError{RetryAfter: 120}, false, 0},
		{"bad markup", fmt.Errorf("sendMessage: %w", tg.ErrorBadRequest), false, 0},
		{"bot blocked", fmt.Errorf("sendMessage: %w", tg.ErrorForbidden), false, 0},
		{"shutting down", context.Canceled, false, 0},
		{"network hiccup", errors.New("dial tcp: i/o timeout"), true, 2 * time.Second},
	}
	for _, c := range cases {
		wait, retry := retryDelay(c.err)
		if retry != c.retry || (retry && wait != c.wait) {
			t.Errorf("%s: retry=%v wait=%v, want retry=%v wait=%v", c.name, retry, wait, c.retry, c.wait)
		}
	}
}
