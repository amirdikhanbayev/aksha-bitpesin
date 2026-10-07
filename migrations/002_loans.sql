-- Debts and loans: money lent to or borrowed from people, and what is owed to a
-- bank — an installment plan, a credit, a mortgage.
--
-- Like every file here, this runs on every start and must stay idempotent.

CREATE TABLE IF NOT EXISTS loans (
    id              BIGSERIAL PRIMARY KEY,
    user_id         BIGINT NOT NULL REFERENCES users(id),
    -- lent: someone owes me. borrowed: I owe a person.
    -- installment / credit / mortgage: I owe a bank or a shop.
    kind            TEXT NOT NULL
                    CHECK (kind IN ('lent','borrowed','installment','credit','mortgage')),
    name            TEXT NOT NULL,              -- who, or what for: "Aidar", "Kaspi · iPhone"
    currency        VARCHAR(10) NOT NULL REFERENCES currencies(code),
    -- How much was owed at the start, in minor units. What is owed now is this
    -- minus the repayments, worked out from the operations linked to the loan.
    principal       BIGINT NOT NULL CHECK (principal > 0),
    -- The account the money came into or went out of when the loan was opened.
    -- NULL when it never passed through an account: an installment paid straight
    -- to the shop, a mortgage paid to the seller. That decides how a repayment is
    -- counted — see transactions.loan_id below.
    account_id      BIGINT REFERENCES accounts(id),
    monthly_payment BIGINT,                     -- the agreed payment, if there is one
    payment_day     SMALLINT CHECK (payment_day BETWEEN 1 AND 31),
    note            TEXT,
    opened_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_reminded   DATE,                       -- the month the payment reminder last went out
    deleted_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS loans_user_idx ON loans (user_id) WHERE deleted_at IS NULL;

-- An operation that belongs to a loan points at it. Which operations a loan has:
--
--   * debt_out / debt_in — money leaving or entering an account that is neither
--     an expense nor income: lending, being paid back, borrowing, repaying a
--     person, repaying the principal of a credit that landed on an account;
--   * expense — a payment that is spending: an installment or mortgage payment
--     (the purchase itself never went through an account, so the payments are
--     the spending), and the interest part of any bank payment.
--
-- interest is how much of an expense was interest, as the user stated it.
ALTER TABLE transactions ADD COLUMN IF NOT EXISTS loan_id BIGINT REFERENCES loans(id);
ALTER TABLE transactions ADD COLUMN IF NOT EXISTS interest BIGINT;

ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_kind_check;
ALTER TABLE transactions ADD CONSTRAINT transactions_kind_check
    CHECK (kind IN ('income','expense','transfer','debt_in','debt_out'));

ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_interest_check;
ALTER TABLE transactions ADD CONSTRAINT transactions_interest_check
    CHECK (interest IS NULL OR (interest >= 0 AND interest <= amount));

CREATE INDEX IF NOT EXISTS transactions_loan_idx
    ON transactions (loan_id) WHERE deleted_at IS NULL AND loan_id IS NOT NULL;
