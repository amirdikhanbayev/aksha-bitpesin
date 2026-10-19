-- Transfer fees, split bills and forgiven debts.
--
-- Like every file here, this runs on every start and must stay idempotent.

-- A fee charged for a transfer is an expense of its own, on the account the
-- transfer left, pointing at the transfer it was charged for. The fee is
-- spending; the transfer itself still isn't. Deleting the transfer takes its fee
-- with it.
ALTER TABLE transactions ADD COLUMN IF NOT EXISTS fee_of BIGINT REFERENCES transactions(id);

ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_fee_is_expense;
ALTER TABLE transactions ADD CONSTRAINT transactions_fee_is_expense
    CHECK (fee_of IS NULL OR kind = 'expense');

CREATE INDEX IF NOT EXISTS transactions_fee_of_idx
    ON transactions (fee_of) WHERE deleted_at IS NULL AND fee_of IS NOT NULL;

-- A forgiven debt between people. No money moves, so the operation leaves every
-- balance as it was; it only settles the debt. What it is for the books follows
-- from who forgave whom: forgiving money lent is an expense — it is not coming
-- back — and being let off a debt is income. account_id only says which currency
-- it is counted in.
ALTER TABLE transactions ADD COLUMN IF NOT EXISTS forgiven BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_forgiven_needs_loan;
ALTER TABLE transactions ADD CONSTRAINT transactions_forgiven_needs_loan
    CHECK (NOT forgiven OR (loan_id IS NOT NULL AND kind IN ('income','expense')));

-- A split bill: the user paid the whole of it, and the others' shares are owed
-- back. The expense keeps only the user's own share, and each other share is a
-- debt of its own (kind 'lent'), opened from the same account and pointing at
-- the bill. Each is paid back, or forgiven, on its own.
--
-- ON DELETE SET NULL: operations are only ever hard-deleted when a user erases
-- everything, and then the loans go too.
ALTER TABLE loans ADD COLUMN IF NOT EXISTS split_of BIGINT REFERENCES transactions(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS loans_split_of_idx
    ON loans (split_of) WHERE deleted_at IS NULL AND split_of IS NOT NULL;
