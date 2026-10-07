-- NOTE: this is the first draft of the schema, kept only as a record of where the
-- project started. The schema the bot actually uses is migrations/001_init.sql —
-- it has since gained the user's own exchange rate on an operation, income
-- sources, onboarding state and more. Don't apply this file.

-- Bot users
CREATE TABLE users (
                       id           BIGSERIAL PRIMARY KEY,
                       telegram_id  BIGINT UNIQUE NOT NULL,
                       username     TEXT,
                       timezone     TEXT NOT NULL DEFAULT 'Asia/Almaty',  -- matters for month boundaries
                       created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Currency reference table
CREATE TABLE currencies (
                            code      CHAR(3) PRIMARY KEY,   -- KZT, USD, EUR, RUB...
                            name      TEXT NOT NULL,
                            decimals  SMALLINT NOT NULL DEFAULT 2
);

-- Market rates by date — only for summary reports (estimating "how much is this in KZT"),
-- NOT for the user's actual operations — there the rate is entered manually in transactions.rate
CREATE TABLE exchange_rates (
                                base_code  CHAR(3) NOT NULL REFERENCES currencies(code),
                                quote_code CHAR(3) NOT NULL REFERENCES currencies(code),
                                rate_date  DATE NOT NULL,
                                rate       NUMERIC(18,8) NOT NULL,   -- how much base per 1 quote
                                PRIMARY KEY (base_code, quote_code, rate_date)
);

-- Accounts / wallets: "where from" (cash, Kaspi Gold, deposit, another bank's card)
-- each account has its own currency
CREATE TABLE accounts (
                          id               BIGSERIAL PRIMARY KEY,
                          user_id          BIGINT NOT NULL REFERENCES users(id),
                          name             TEXT NOT NULL,
                          currency         CHAR(3) NOT NULL REFERENCES currencies(code),
                          initial_balance  BIGINT NOT NULL DEFAULT 0,   -- in minor units (cents/tiyn)
                          archived         BOOLEAN NOT NULL DEFAULT false,
                          created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
                          UNIQUE (user_id, name)
);

-- Categories: user_id NULL = default for everyone
CREATE TABLE categories (
                            id         BIGSERIAL PRIMARY KEY,
                            user_id    BIGINT REFERENCES users(id),
                            parent_id  BIGINT REFERENCES categories(id),  -- "Food" → "Restaurants", "Groceries"
                            name       TEXT NOT NULL,
                            kind       TEXT NOT NULL CHECK (kind IN ('income','expense')),
                            emoji      TEXT,
                            archived   BOOLEAN NOT NULL DEFAULT false
);

-- The main table — every operation
CREATE TABLE transactions (
                              id                BIGSERIAL PRIMARY KEY,
                              user_id           BIGINT NOT NULL REFERENCES users(id),
                              account_id        BIGINT NOT NULL REFERENCES accounts(id),
                              category_id       BIGINT REFERENCES categories(id),       -- NULL for transfers
                              kind              TEXT NOT NULL CHECK (kind IN ('income','expense','transfer')),
                              amount            BIGINT NOT NULL CHECK (amount > 0),     -- debit/credit on account_id
                              to_account_id     BIGINT REFERENCES accounts(id),         -- only for transfer
                              to_amount         BIGINT,                                 -- credited amount, if to_account_id's currency differs
                              rate              NUMERIC(18,8),                           -- rate the user actually bought/exchanged at (entered manually)
                              note              TEXT,                                   -- "from whom / for what"
                              occurred_at       TIMESTAMPTZ NOT NULL,                   -- when it actually happened
                              created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),     -- when it was entered into the bot
                              deleted_at        TIMESTAMPTZ,                            -- soft delete
                              CHECK ((kind = 'transfer') = (to_account_id IS NOT NULL)),
    -- if to_account_id's currency differs from account_id's, the user must enter the rate
    -- they actually bought/exchanged at (this is NOT the market rate from exchange_rates)
                              CHECK (to_amount IS NULL OR rate IS NOT NULL)
);

CREATE INDEX ON transactions (user_id, occurred_at) WHERE deleted_at IS NULL;

-- ============================================================
-- Optional
-- ============================================================

-- Monthly limits by category (in the category/account's currency — your call)
CREATE TABLE budgets (
                         user_id      BIGINT NOT NULL REFERENCES users(id),
                         category_id  BIGINT NOT NULL REFERENCES categories(id),
                         month        DATE NOT NULL,          -- first day of the month
                         currency     CHAR(3) NOT NULL REFERENCES currencies(code),
                         limit_amount BIGINT NOT NULL,
                         PRIMARY KEY (user_id, category_id, month)
);

-- Recurring operations: salary, subscriptions, rent
CREATE TABLE recurring (
                           id           BIGSERIAL PRIMARY KEY,
                           user_id      BIGINT NOT NULL REFERENCES users(id),
                           account_id   BIGINT NOT NULL REFERENCES accounts(id),
                           category_id  BIGINT NOT NULL REFERENCES categories(id),
                           kind         TEXT NOT NULL CHECK (kind IN ('income','expense')),
                           amount       BIGINT NOT NULL,
                           day_of_month SMALLINT NOT NULL CHECK (day_of_month BETWEEN 1 AND 31),
                           note         TEXT,
                           active       BOOLEAN NOT NULL DEFAULT true
);

-- ============================================================
-- Example monthly report by category (broken out per currency)
-- ============================================================
-- SELECT a.currency, t.kind, c.name AS category,
--        SUM(t.amount) / 100.0 AS total, COUNT(*) AS cnt
-- FROM transactions t
-- JOIN accounts a ON a.id = t.account_id
-- LEFT JOIN categories c ON c.id = t.category_id
-- JOIN users u ON u.id = t.user_id
-- WHERE t.user_id = :uid
--   AND t.deleted_at IS NULL
--   AND t.kind <> 'transfer'
--   AND t.occurred_at >= (:month::timestamp AT TIME ZONE u.timezone)
--   AND t.occurred_at <  ((:month::date + INTERVAL '1 month')::timestamp AT TIME ZONE u.timezone)
-- GROUP BY a.currency, t.kind, c.name
-- ORDER BY a.currency, t.kind, total DESC;
