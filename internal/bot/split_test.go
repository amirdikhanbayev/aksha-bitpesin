package bot

import (
	"testing"
	"time"

	"aksha-bitpesin/internal/storage"
)

// A transfer ends with the fee: a percent is worked out from the amount, comes
// out of the source account on top, and /undo takes it back with the transfer.
func TestTransferWithAPercentFee(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 401)
	u := userOf(t, st, ctx, p)
	card, _ := st.CreateAccount(ctx, u.ID, "Card", "KZT", 100_000_00)
	cash, _ := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 0)

	p.say("🔁 Transfer")
	p.tap("💳 Card · KZT")
	p.say("50000")
	p.tap("💵 Cash · KZT")
	p.sees("Was there a fee for the transfer?")
	p.tap("％ In percent")
	p.say("1.5")
	p.sees("Transfer recorded")
	p.sees("Fee: 750 KZT · 1.5%")
	p.sees("Debited in all: 50,750 KZT")
	if b, _ := st.AccountBalance(ctx, u.ID, card.ID); b != 49_250_00 {
		t.Errorf("card = %d, want 49,250", b)
	}
	if b, _ := st.AccountBalance(ctx, u.ID, cash.ID); b != 50_000_00 {
		t.Errorf("cash = %d, want 50,000", b)
	}

	p.tap("💸 The fee")
	p.sees("Transfer fee")
	p.sees("Category: 💸 Transfer fee")

	p.say("/undo")
	if b, _ := st.AccountBalance(ctx, u.ID, card.ID); b != 100_000_00 {
		t.Errorf("card after /undo = %d, want 100,000: the transfer and its fee are gone", b)
	}
}

// A fee forgotten during the transfer is added from its card.
func TestFeeAddedToARecordedTransfer(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 402)
	u := userOf(t, st, ctx, p)
	card, _ := st.CreateAccount(ctx, u.ID, "Card", "KZT", 100_000_00)
	st.CreateAccount(ctx, u.ID, "Cash", "KZT", 0)

	p.say("🔁 Transfer")
	p.tap("💳 Card · KZT")
	p.say("20000")
	p.tap("💵 Cash · KZT")
	p.tap("✅ No fee")
	p.sees("Transfer recorded")

	p.tap("💸 Add a fee")
	p.tap("💵 Fixed amount")
	p.say("200")
	p.sees("Fee: 200 KZT · 1%")
	if b, _ := st.AccountBalance(ctx, u.ID, card.ID); b != 79_800_00 {
		t.Errorf("card = %d, want 79,800", b)
	}
}

// Paid for three: the bill is split equally, one share comes back to another
// account, the other is forgiven and counted as spending under the bill's category.
func TestSplitTheBillThenPayBackAndForgive(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 403)
	u := userOf(t, st, ctx, p)
	card, _ := st.CreateAccount(ctx, u.ID, "Card", "KZT", 100_000_00)
	cash, _ := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 0)
	withCategories(t, st, ctx, u, storage.KindExpense, "Groceries")

	p.say("➖ Expense")
	p.tap("💳 Card · KZT")
	p.say("30000")
	p.tap("🛒 Groceries")
	p.tap("⏭ No source")
	p.sees("Expense recorded")

	p.tap("👥 Split the bill")
	p.sees("Who shares it with you?")
	p.say("Aidar, Dana")
	p.sees("With you: Aidar, Dana")
	p.tap("Done ▶️")
	p.tap("➗ Equally — 10,000 each")
	p.sees("Your share: 10,000 KZT")
	p.sees("Aidar owes you 10,000 KZT")
	p.sees("Dana owes you 10,000 KZT")

	// Aidar sends his share to the cash account.
	p.tap("🤝 Aidar")
	p.sees("Owes you: 10,000 KZT")
	p.tap("💰 Got money back")
	p.tap("💵 Cash · KZT")
	p.tap("All: 10,000")
	p.sees("All paid back")

	// Dana's is forgiven.
	p.say("/debts")
	p.tap("🤝 Dana · Groceries")
	p.tap("🕊 Forgive")
	p.sees("counted as spending under 🛒 Groceries")
	p.tap("All: 10,000")
	p.sees("Forgiven: 10,000 KZT")
	p.sees("Settled")

	if b, _ := st.AccountBalance(ctx, u.ID, card.ID); b != 70_000_00 {
		t.Errorf("card = %d, want 70,000: the whole bill left it", b)
	}
	if b, _ := st.AccountBalance(ctx, u.ID, cash.ID); b != 10_000_00 {
		t.Errorf("cash = %d, want Aidar's 10,000", b)
	}
	now := p.b.clock()
	totals, _ := st.Totals(ctx, u.ID, now.AddDate(0, 0, -1), now.Add(time.Hour))
	if len(totals) != 1 || totals[0].Expense != 20_000_00 {
		t.Errorf("totals = %+v, want 20,000 spent: the user's share and Dana's", totals)
	}

	// The bill's card says how it was split.
	p.say("/debts")
	p.tap("✅ Paid off (2)")
	p.tap("🤝 Aidar · Groceries")
	p.tap("🧾 The bill")
	p.sees("your share")
	p.sees("Bill: 30,000 KZT, split")
	p.sees("Dana — 10,000 · 🕊 forgiven")
	for _, row := range p.tg.visible()[0].buttons {
		for _, bt := range row {
			if bt.Text == "👥 Split the bill" {
				t.Error("a split bill offers to be split again")
			}
		}
	}
	// Its old card still has the button, which now only says so.
	p.tap("👥 Split the bill")
	p.sees("already split")
}

// Shares can be set one by one, and the last one can take the rest.
func TestSplitShareByShare(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 404)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Card", "KZT", 100_000_00); err != nil {
		t.Fatal(err)
	}
	withCategories(t, st, ctx, u, storage.KindExpense, "Groceries")

	p.say("➖ Expense") // one account: not asked which
	p.say("25000")
	p.tap("🛒 Groceries")
	p.tap("⏭ No source")
	p.tap("👥 Split the bill")
	p.say("Aidar")
	p.say("Dana")
	p.tap("Done ▶️")
	p.tap("✏️ Set each share")
	p.sees("How much is Aidar's share?")
	p.say("30000")
	p.sees("no more than the 25,000 KZT left")
	p.say("5000")
	p.tap("All the rest: 20,000")
	p.sees("Your share: 0 KZT")

	loans, _ := st.Loans(ctx, u.ID)
	if len(loans) != 2 || loans[1].Outstanding() != 20_000_00 {
		t.Fatalf("loans = %+v, want Aidar 5,000 and Dana 20,000", loans)
	}
}
