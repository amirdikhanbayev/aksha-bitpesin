-- The whole schema, in one migration. A fresh database gets exactly this; the
-- bot starts with no data of its own beyond the currency reference table, which
-- the foreign keys below require.
--
-- Every statement is idempotent, so running this against a database that already
-- has the schema changes nothing and fails nothing.

-- Bot users
CREATE TABLE IF NOT EXISTS users (
    id                BIGSERIAL PRIMARY KEY,
    telegram_id       BIGINT UNIQUE NOT NULL,
    username          TEXT,
    timezone          TEXT NOT NULL DEFAULT 'Asia/Almaty',  -- month boundaries are computed in it
    main_currency     VARCHAR(10) NOT NULL DEFAULT 'KZT',    -- report totals are converted into it
    monthly_report    BOOLEAN NOT NULL DEFAULT true,         -- send the statement on the 1st
    last_report_month DATE,                                  -- which month's statement went out already
    onboarded_at      TIMESTAMPTZ,                           -- when the first-run setup was done or skipped
    daily_reminder    BOOLEAN NOT NULL DEFAULT true,         -- the evening nudge to record the day
    last_reminder     DATE,                                  -- the day it was last sent
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Currency reference table. VARCHAR(10) rather than CHAR(3): USDT and other
-- crypto codes are longer than three characters.
CREATE TABLE IF NOT EXISTS currencies (
    code     VARCHAR(10) PRIMARY KEY,
    name     TEXT NOT NULL,
    decimals SMALLINT NOT NULL DEFAULT 2  -- how many minor units make one unit
);

-- Market rates by date — only for the report's estimated "≈ in your currency"
-- line. An operation's own rate is the one the user entered, in transactions.rate.
CREATE TABLE IF NOT EXISTS exchange_rates (
    base_code  VARCHAR(10) NOT NULL REFERENCES currencies(code),
    quote_code VARCHAR(10) NOT NULL REFERENCES currencies(code),
    rate_date  DATE NOT NULL,
    rate       NUMERIC(18,8) NOT NULL,  -- how much base per 1 quote
    PRIMARY KEY (base_code, quote_code, rate_date)
);

-- Accounts / wallets. One place where money is kept — cash, a card, a deposit —
-- can hold several currencies, and each currency is its own row with its own
-- balance: name is the place, currency is what it holds. Cash in tenge and cash
-- in dollars are two rows both named "Cash", grouped under it in the interface.
CREATE TABLE IF NOT EXISTS accounts (
    id              BIGSERIAL PRIMARY KEY,
    user_id         BIGINT NOT NULL REFERENCES users(id),
    name            TEXT NOT NULL,              -- the place: "Cash", "Card", "Kaspi Gold"
    currency        VARCHAR(10) NOT NULL REFERENCES currencies(code),
    initial_balance BIGINT NOT NULL DEFAULT 0,  -- in minor units (cents/tiyn)
    archived        BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, name, currency)
);

-- A place used to be unique on its own; now it is a place-and-currency pair.
-- Stated separately so a database created before this still gets the change.
ALTER TABLE accounts DROP CONSTRAINT IF EXISTS accounts_user_id_name_key;
ALTER TABLE accounts DROP CONSTRAINT IF EXISTS accounts_user_id_name_currency_key;
ALTER TABLE accounts ADD CONSTRAINT accounts_user_id_name_currency_key
    UNIQUE (user_id, name, currency);

-- Categories. Nothing is created up front: a category exists once its owner
-- names it, in their own words.
CREATE TABLE IF NOT EXISTS categories (
    id       BIGSERIAL PRIMARY KEY,
    user_id  BIGINT NOT NULL REFERENCES users(id),
    name     TEXT NOT NULL,
    kind     TEXT NOT NULL CHECK (kind IN ('income','expense')),
    emoji    TEXT,
    archived BOOLEAN NOT NULL DEFAULT false
);

CREATE UNIQUE INDEX IF NOT EXISTS categories_user_uniq
    ON categories (user_id, lower(name), kind);
CREATE INDEX IF NOT EXISTS categories_lookup_idx
    ON categories (user_id, kind) WHERE archived = false;

-- Subcategories and categories shared between users were never built; drop the
-- leftovers so a database created earlier matches this schema.
DROP INDEX IF EXISTS categories_default_uniq;
ALTER TABLE categories DROP COLUMN IF EXISTS parent_id;

-- The main table: every operation.
--
-- amount is always in the account's currency, so balances add up. When the
-- operation itself happened in another currency — a purchase priced in KZT paid
-- from a USD card — original_amount/original_currency hold what it actually cost
-- and rate holds the rate the user entered for it.
CREATE TABLE IF NOT EXISTS transactions (
    id                BIGSERIAL PRIMARY KEY,
    user_id           BIGINT NOT NULL REFERENCES users(id),
    account_id        BIGINT NOT NULL REFERENCES accounts(id),
    category_id       BIGINT REFERENCES categories(id),  -- NULL for transfers
    kind              TEXT NOT NULL CHECK (kind IN ('income','expense','transfer')),
    amount            BIGINT NOT NULL CHECK (amount > 0),
    to_account_id     BIGINT REFERENCES accounts(id),    -- transfers only
    to_amount         BIGINT,                            -- credited amount, when the currencies differ
    rate              NUMERIC(18,8),                     -- the rate the user entered, never a market one
    original_amount   BIGINT,                            -- what the operation cost in its own currency
    original_currency VARCHAR(10) REFERENCES currencies(code),
    source            TEXT,                              -- who the money came from / went to
    note              TEXT,
    occurred_at       TIMESTAMPTZ NOT NULL,              -- when it actually happened
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),-- when it was entered
    deleted_at        TIMESTAMPTZ,                       -- soft delete
    CONSTRAINT transactions_transfer_pair
        CHECK ((kind = 'transfer') = (to_account_id IS NOT NULL)),
    -- A converted amount needs the rate it was converted at.
    CONSTRAINT transactions_to_amount_needs_rate
        CHECK (to_amount IS NULL OR rate IS NOT NULL),
    -- Both original columns are set together, or neither is.
    CONSTRAINT transactions_original_pair
        CHECK ((original_amount IS NULL) = (original_currency IS NULL)),
    CONSTRAINT transactions_original_needs_rate
        CHECK (original_currency IS NULL OR rate IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS transactions_user_occurred_idx
    ON transactions (user_id, occurred_at) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS transactions_category_idx
    ON transactions (user_id, category_id, occurred_at) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS transactions_source_idx
    ON transactions (user_id, lower(source)) WHERE deleted_at IS NULL AND source IS NOT NULL;
CREATE INDEX IF NOT EXISTS transactions_account_idx
    ON transactions (account_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS transactions_to_account_idx
    ON transactions (to_account_id) WHERE deleted_at IS NULL AND to_account_id IS NOT NULL;

-- Monthly limits per category.
CREATE TABLE IF NOT EXISTS budgets (
    user_id      BIGINT NOT NULL REFERENCES users(id),
    category_id  BIGINT NOT NULL REFERENCES categories(id),
    month        DATE NOT NULL,  -- first day of the month
    currency     VARCHAR(10) NOT NULL REFERENCES currencies(code),
    limit_amount BIGINT NOT NULL,
    PRIMARY KEY (user_id, category_id, month)
);

-- Recurring operations: salary, rent, subscriptions.
CREATE TABLE IF NOT EXISTS recurring (
    id                 BIGSERIAL PRIMARY KEY,
    user_id            BIGINT NOT NULL REFERENCES users(id),
    account_id         BIGINT NOT NULL REFERENCES accounts(id),
    category_id        BIGINT NOT NULL REFERENCES categories(id),
    kind               TEXT NOT NULL CHECK (kind IN ('income','expense')),
    amount             BIGINT NOT NULL,
    day_of_month       SMALLINT NOT NULL CHECK (day_of_month BETWEEN 1 AND 31),
    note               TEXT,
    active             BOOLEAN NOT NULL DEFAULT true,
    last_created_month DATE  -- so one month's operation is never created twice
);

-- Dialog state of the button wizards. Kept in the database so a restart
-- mid-conversation loses nothing.
CREATE TABLE IF NOT EXISTS user_states (
    user_id    BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    state      TEXT NOT NULL,
    data       JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The one thing that must exist before the bot can do anything: the currencies
-- the foreign keys above point at, including the default 'KZT' of users.
-- Adding a currency later is just another INSERT here.
INSERT INTO currencies (code, name, decimals) VALUES
    ('KZT',  'Kazakhstani tenge', 2),
    ('USD',  'US dollar',         2),
    ('EUR',  'Euro',              2),
    ('RUB',  'Russian ruble',     2),
    ('GBP',  'Pound sterling',    2),
    ('TRY',  'Turkish lira',      2),
    ('CNY',  'Chinese yuan',      2),
    ('AED',  'UAE dirham',        2),
    ('GEL',  'Georgian lari',     2),
    ('KGS',  'Kyrgyzstani som',   2),
    ('UZS',  'Uzbekistani som',   2),
    ('USDT', 'Tether',            2)
ON CONFLICT (code) DO NOTHING;

-- Added later than the rest of the columns above; stated separately so a database
-- created before the evening reminder existed gets them too.
ALTER TABLE users ADD COLUMN IF NOT EXISTS daily_reminder BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE users ADD COLUMN IF NOT EXISTS last_reminder DATE;

-- users.main_currency points at this table, so the constraint can only be added
-- once the rows above exist.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_main_currency_fkey;
ALTER TABLE users ADD CONSTRAINT users_main_currency_fkey
    FOREIGN KEY (main_currency) REFERENCES currencies(code);
