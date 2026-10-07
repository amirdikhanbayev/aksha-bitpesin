package bot

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"log/slog"
	"strconv"

	tg "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"aksha-bitpesin/internal/money"
	"aksha-bitpesin/internal/parser"
	"aksha-bitpesin/internal/storage"
)

// cmdExport exports the period's operations to CSV — opens in Excel and Google Sheets.
func (b *Bot) cmdExport(ctx context.Context, u storage.User, chatID int64, args string) {
	p, err := parser.ParsePeriod(args, b.clock(), u.Location())
	if err != nil {
		b.send(ctx, chatID, "Couldn't read the period. For example: <code>/export september</code>", nil)
		return
	}
	txs, err := b.st.ListTransactions(ctx, u.ID, p.From, p.To, 10000)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	if len(txs) == 0 {
		b.send(ctx, chatID, "No operations in “"+esc(p.Label)+"”.", nil)
		return
	}

	var buf bytes.Buffer
	buf.WriteString("\ufeff") // BOM so Excel doesn't mangle Cyrillic
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"Date", "Type", "Account amount", "Account currency",
		"Operation amount", "Operation currency", "Rate",
		"Category", "Source", "Account", "To account", "Note", "Debt / loan", "Interest"})

	loc := u.Location()
	for _, t := range txs {
		kind := map[string]string{
			storage.KindIncome:   "income",
			storage.KindExpense:  "expense",
			storage.KindTransfer: "transfer",
			storage.KindDebtIn:   "debt in",
			storage.KindDebtOut:  "debt out",
		}[t.Kind]
		loan, interest := "", ""
		if t.LoanID != nil {
			loan = t.LoanName + " (" + loanKindTitle(t.LoanKind) + ")"
		}
		if t.Interest != nil && *t.Interest > 0 {
			interest = money.FormatPlain(*t.Interest, t.Decimals)
		}
		rate := ""
		if t.Rate != nil {
			rate = strconv.FormatFloat(*t.Rate, 'f', -1, 64)
		}
		// What the operation itself cost, when it was in another currency.
		origAmount, origCurrency := "", ""
		if t.Converted() {
			origAmount = money.FormatPlain(*t.OriginalAmount, t.OriginalDecimals)
			origCurrency = t.OriginalCurrency
		}
		_ = w.Write([]string{
			t.OccurredAt.In(loc).Format("02.01.2006 15:04"),
			kind,
			money.FormatPlain(t.Amount, t.Decimals),
			t.AccountCurr,
			origAmount,
			origCurrency,
			rate,
			t.CategoryName,
			t.Source,
			t.AccountName,
			t.ToAccountName,
			t.Note,
			loan,
			interest,
		})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		b.fail(ctx, chatID, err)
		return
	}

	name := fmt.Sprintf("operations_%s_%s.csv",
		p.From.Format("2006-01-02"), p.To.AddDate(0, 0, -1).Format("2006-01-02"))
	_, err = b.api.SendDocument(ctx, &tg.SendDocumentParams{
		ChatID:   chatID,
		Document: &models.InputFileUpload{Filename: name, Data: bytes.NewReader(buf.Bytes())},
		Caption:  fmt.Sprintf("Operations for %s — %d", p.Label, len(txs)),
	})
	if err != nil {
		slog.Error("SendDocument", "err", err)
		b.send(ctx, chatID, "Couldn't send the file: "+esc(err.Error()), nil)
	}
}
