<div align="center">

# 💸 aksha-bitpesin

**A personal finance Telegram bot for people who keep money in more than one currency.**

Tap what happened, type only the number. The bot keeps the books.

![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-14+-4169E1?logo=postgresql&logoColor=white)
![Docker](https://img.shields.io/badge/Docker-ready-2496ED?logo=docker&logoColor=white)
![Telegram](https://img.shields.io/badge/Telegram-Bot-26A5E4?logo=telegram&logoColor=white)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

[Features](#-features) ·
[Quick start](#-quick-start) ·
[Configuration](#%EF%B8%8F-configuration) ·
[Deploying](#%EF%B8%8F-deploying-to-the-cloud) ·
[How it works](#-how-it-works) ·
[Development](#-development)

</div>

---

```
you  ▸ ➖ Expense
bot  ▸ ➖ Expense — paid from which account?      [Cash (KZT)] [Card USD (USD)]
you  ▸ Cash (KZT)
bot  ▸ How much? Send the amount in KZT.
you  ▸ 5000
bot  ▸ Category?                                  [🛒 Groceries] [🚕 Transport] …
you  ▸ 🛒 Groceries        ← your own categories, most-used first
bot  ▸ Who was it paid to?                        [Magnum] [⏭ No source]
you  ▸ Magnum
bot  ▸ ✅ Expense recorded
       💸 −5,000 KZT
       Category: 🛒 Groceries
       Source: Magnum
       Account: 💵 Cash · KZT
       Balance: 95,000 KZT      ← where you stand after it
       [💰 Amount] [📅 Date] [🏷 Source] [📝 Note] [🗑 Delete]
```

### Why this bot

- 🪙 **Money is never a float.** Amounts are integers in minor units (tiyn, cents), so balances don't drift.
- 💱 **Rates are never invented.** When a purchase crosses currencies, the bot asks for the rate your bank actually applied.
- 👆 **Tap first.** Accounts, categories, sources and dates are all buttons. The only thing you type is a number.
- 📬 **Statements arrive on their own.** Last month's statement comes on the 1st at 9:00 in your timezone.
- 🔒 **Private and yours.** It is self-hosted, access is limited to your Telegram IDs, and two taps erase everything.

---

## ✨ Features

### 🚀 First run: three questions

A new user is set up before anything else, starting from their very first message:

1. **Timezone.** Month boundaries and the monthly statement follow it.
2. **Main currency.** Report totals are converted into it.
3. **Where the money is.** Tick every place that applies (Cash, Card, Deposit, Savings, Crypto wallet, or **✏️ Other place** for a bank's name). Then, for each place, pick the currencies it holds and how much is in each right now.

```
👋 Welcome! …   Which timezone are you in?        [Almaty / Astana] [Moscow] [Dubai] …
you  ▸ Dubai
bot  ▸ Which currency do you count in?             [USD] [KZT] [EUR] …
you  ▸ USD
bot  ▸ Where do you keep your money?               [💵 Cash] [💳 Card] [🏦 Deposit] …  [Done ▶️]
you  ▸ 💵 Cash · 💳 Card · Done
bot  ▸ 💵 Cash — which currencies does it hold?    you ▸ KZT · USD · Done
bot  ▸ 💵 Cash · KZT — how much is there?          you ▸ 100000
bot  ▸ 💵 Cash · USD — how much is there?          you ▸ 500
bot  ▸ 💳 Card — which currencies does it hold?    you ▸ KZT · Done   …
bot  ▸ 🎉 You're all set!
```

Only the balances are typed. You can change everything later in **⚙️ Settings**, and you can skip the last step (*Skip for now*, or *Skip this one* for a single place). Someone who already has an account never goes through setup again.

### 👆 Tap first, type only numbers

Everything starts from the bottom menu:

```
➖ Expense   ➕ Income   🔁 Transfer
📊 Reports   💳 Accounts   ⚙️ Settings
          🤝 Debts
```

The first tap says what kind of operation it is, and the rest follows its logic. An expense asks which account it was *paid from*, income asks which account it *landed on*, and a transfer asks for both. If you have only one account, that question is skipped. You type the amount, then tap the category and the source. Sources are suggested from what you've used before.

| To do this | You tap |
|---|---|
| Pick an account, category, source | the suggested buttons |
| Record in another currency | **💱 Another currency**, then the currency |
| Backdate an operation | **📅 Date** on its card: today, yesterday, 2 days ago… |
| Correct anything about an operation | its card: **💰 Amount**, **📅 Date**, **🏷 Category**, **💳 Account**, **🧑 Source**, **📝 Note** |
| Record the same thing again | **🔁 Again** on a card: same category, account and source, only the amount is new |
| Find something | **🔍 Search**: `magnum`, `> 50000`, `coffee 1000-5000` |
| Rename or retire a category | ⚙️ Settings → 🏷 Categories → ✏️ Edit |
| Create an account | the place → the currencies it holds → the balance of each |
| Create a category | **➕ New category**, then its name. Start it with an emoji to give it an icon (`🛒 Groceries`) |
| Set a recurring operation's day | a calendar grid, 1–31 |
| Choose a report period | a month, or this week / today / this year |

When none of the suggestions fit, you can still type a name for an account, category, source or note, but you never have to. Amounts accept `1,500.50`, `1 500,50`, `12k` and `1.5m`, and anything the bot prints can be typed back in.

### 💱 Multi-currency, with your rate and not a guessed one

One place can hold several currencies. Cash in tenge and cash in dollars both sit under **Cash**, each with its own balance, so every balance always reconciles with the bank.

```
💳 Accounts and balances

💵 Cash                    ← one place
   • 100,000 KZT           ← its balances, one per currency
   • 500 USD
💳 Card — 50,000 KZT

Total: 150,000 KZT + 500 USD
```

When an operation crosses currencies, such as a tenge purchase paid with a dollar card or cash changed at a kiosk, **the bot stops and asks you**, because every bank and exchange booth applies its own rate:

```
bot  ▸ 💱 The operation is in 5,000 KZT, but account “Card USD” is in USD.

       How much USD was debited from the account?
       Every bank and exchange has its own rate — so I ask instead of
       working it out myself.
       [Enter the rate instead] [✖️ Cancel]
```

You can answer with the amount that actually left the account, and the bot derives the rate from those two real figures. Or you can enter the rate itself and choose which way you're quoting it (`1 USD = ? KZT` or `1 KZT = ? USD`). Both sides are stored and shown:

```
−10.64 USD
Purchase: 5,000 KZT · your rate: 1 USD = 470 KZT
```

> [!NOTE]
> Market rates (from the National Bank of Kazakhstan, or set by hand with `/rate`) are used for one thing only: the estimated *"≈ in KZT"* line that makes a multi-currency total comparable. They never touch an operation's amount.

### 📊 Statements: monthly, or whenever you ask

- **On request:** tap **📊 Reports** and pick a period: this month, last month, the four months before that, this week, today, this year, or **📅 Custom range** (four taps). `/month` and `/report` are shortcuts.
- **On its own:** last month's statement arrives on the 1st at 9:00 **in your timezone**. No statement is sent for a month with no operations. You can switch this on or off in Settings.

```
📊 Statement: August 2026
💰 Total · ≈ in your main currency · expenses and income by category ·
income sources · where the money goes · budgets · account balances

[🧾 Operations]  [📄 CSV]
```

- **🧾 Operations** lists every operation in the period, oldest first, like a bank statement. Transfers are included and marked 🔁.
- **📄 CSV** sends a file that opens in Excel and Google Sheets.
- **🤖 Prompt for AI analysis** sends the CSV together with a prompt that explains the columns: transfers aren't spending, currencies mustn't be added together, and some figures are exact while others are estimates. Paste both into whatever model you use.

When totalling a period in your main currency, the bot uses exact figures before estimates. Your own rate is exact. An account already in that currency needs no conversion. Only what's left is estimated, at the market rate **on that operation's own date**. Currencies with no known rate are listed instead of being silently dropped.

### 🎯 Budgets and recurring operations

Set a monthly limit per category, and the bot warns you while you enter an expense: 🟡 at 80%, 🔴 once you're over. Salary, rent and subscriptions go in `/recurring`, and the bot records them on the right day of the month. A "31st" falls on the last day of shorter months, and nothing is ever recorded twice.

### 🤝 Debts and loans

- **🤝 I lent money** / **🙏 I borrowed**: to or from a person, through one of your accounts or not
- **🛍 Installment plan**, **🏦 Credit**, **🏠 Mortgage**: owed to a bank or a shop, with a monthly payment and a payment day

```
🛍 Kaspi · iPhone · installment plan
You owe: 550,000 KZT of 600,000
▓░░░░░░░░░ 8% paid
Paid so far: 50,000 KZT
Payment: 50,000 KZT on the 15th · about 11 left
[💳 Make a payment]  [🧾 History]  [✏️ Correct what's owed]  [🗑 Delete]
```

For a bank loan, the bot asks **how much of a payment was interest** instead of computing a schedule that would drift from the bank's. You can also record a payment as a usual **➖ Expense**: after you enter the amount, the category step offers **🏦 It's a loan or debt payment**. On the payment day you get a reminder (from 10:00) with a **💳 Record the payment** button.

What counts as spending depends on where the money went:

| | Spending? |
|---|---|
| Lending, being paid back, borrowing, paying a person back | No. Your money moves, but nothing is spent |
| Repaying the principal of a credit that landed on your account | No. What you bought with it was already recorded |
| An installment or mortgage payment | **Yes**, each payment |
| Interest on any bank loan | **Yes** |

### 🧩 Everything else

- **No preset categories.** You name your own the first time you need one, and they're listed most-used first
- **12 currencies** seeded, including USDT
- **Accounts** can be renamed, archived, or have their balance corrected to match reality
- **🌙 Evening nudge** at 21:00 if nothing was recorded that day (can be switched off)
- **Soft delete** and `/undo` for the last operation
- **🗑 Erase all my data:** two deliberate taps remove every account, operation, category, budget, loan and setting. The bot offers the CSV first
- **Restart-safe:** dialog state lives in Postgres, so a restart mid-conversation loses nothing
- **Double-tap safe:** one person's taps are handled one at a time, and stale buttons say so instead of acting

---

## 🚀 Quick start

**You need:** Docker, Postgres 14+, and a bot token from [@BotFather](https://t.me/BotFather). Go 1.25+ is only needed to run from source.

```bash
cp .env.example .env
$EDITOR .env      # TELEGRAM_BOT_TOKEN, DATABASE_URL, ALLOWED_TELEGRAM_IDS

docker compose up -d --build      # or: make up
docker compose logs -f bot        # or: make logs
```

Then open the bot in Telegram and send `/start`. The schema is created automatically at startup. A fresh database gets the schema and the currency table, with no demo data.

> [!IMPORTANT]
> Set `ALLOWED_TELEGRAM_IDS` to your Telegram ID. If it is empty, **anyone** who finds the bot can use it.

<details>
<summary><b>Docker Compose details</b></summary>

<br>

`.env` is passed to the container at run time, so nothing is baked into the image. After editing `.env`, a plain `docker compose up -d` applies it. Rebuild (`--build`) only after changing the code.

`docker-compose.yml` publishes the HTTP port on `127.0.0.1:8080` only, so it's reachable by nginx on the same host and by `curl 127.0.0.1:8080/healthz`. It also maps `host.docker.internal` to the host, so a Postgres running there is reached as `postgres://user:pass@host.docker.internal:5432/aksha`. That Postgres must accept connections from the docker bridge: `listen_addresses` must include it, and `pg_hba.conf` needs a line such as:

```
host aksha aksha 172.16.0.0/12 scram-sha-256
```

The compose file also expects an external `nginx_proxy-network` for the webhook setup. Remove the `proxy` network if you only use long polling.

The container runs read-only, with every Linux capability dropped, `no-new-privileges`, and limits on memory and processes. The image runs as `nonroot`.

</details>

<details>
<summary><b>The .env file syntax</b></summary>

<br>

The syntax is docker's `--env-file`: one `KEY=VALUE` per line, with the value taken literally. **Don't quote values**, because the quotes become part of the value. `#` starts a comment only at the beginning of a line, so passwords containing `#` or `$` work as is. Compose reads the file the same way (`format: raw`, Compose 2.30+), and so does `make run`.

</details>

<details>
<summary><b>Without docker (systemd)</b></summary>

<br>

The binary reads `/etc/aksha/.env` if it exists (real environment variables win over it):

```ini
# /etc/systemd/system/aksha.service
[Service]
ExecStart=/usr/local/bin/aksha
Restart=always
DynamicUser=yes

[Install]
WantedBy=multi-user.target
```

Put `HTTP_ADDR=127.0.0.1:8080` in that file. Outside the image there is no default, and without it no HTTP server starts.

</details>

<details>
<summary><b>Webhook behind nginx</b></summary>

<br>

Long polling needs nothing inbound. For a webhook, set in `.env`:

```
WEBHOOK_URL=https://bot.example.com/telegram/webhook
WEBHOOK_SECRET=<openssl rand -hex 32>
```

Proxy only that one path to the bot:

```nginx
server {
    listen 443 ssl;
    server_name bot.example.com;
    ssl_certificate     /etc/letsencrypt/live/bot.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/bot.example.com/privkey.pem;

    location = /telegram/webhook {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
    }
    location / { return 404; }
}
```

- nginx must pass the path through unchanged (`proxy_pass` with no path after the port does this).
- Telegram accepts webhooks on ports 443, 80, 88 and 8443.
- The bot registers the webhook itself at startup. To check whether Telegram can reach it, run `curl https://api.telegram.org/bot<TOKEN>/getWebhookInfo` and look at `last_error_message`.
- Set `WEBHOOK_SECRET` explicitly. If it is left empty, a new one is generated at every start.
- To switch back to polling, empty `WEBHOOK_URL`. The bot deletes the webhook at startup.

</details>

---

## ⚙️ Configuration

| Variable | Default | What it does |
|---|---|---|
| `TELEGRAM_BOT_TOKEN` | — | **Required.** From @BotFather |
| `DATABASE_URL` | — | **Required.** `postgres://user:pass@host:5432/db` |
| `ALLOWED_TELEGRAM_IDS` | empty | Comma-separated IDs allowed to use the bot. Empty means everyone |
| `DEFAULT_TIMEZONE` | `Asia/Almaty` | Timezone for new users; sets month boundaries |
| `DEFAULT_CURRENCY` | `KZT` | Main currency for new users |
| `SCHEDULER_INTERVAL` | `15m` | How often statements and recurring operations are checked. `0` disables the internal ticker |
| `FETCH_RATES` | `true` | Pull NBRK rates daily, for the estimated summary line |
| `WEBHOOK_URL` | empty | Empty = long polling. Set to a public HTTPS endpoint to use a webhook |
| `WEBHOOK_SECRET` | generated | Verifies that updates really come from Telegram |
| `HTTP_ADDR` | `:8080` in the image | Where the HTTP server listens. Empty disables it (a webhook won't start without it) |
| `PORT` | — | Overrides `HTTP_ADDR`; set by Cloud Run, Railway and Heroku |
| `SCHEDULER_TOKEN` | empty | Enables `POST /tasks/run` for an external cron |
| `TELEGRAM_API_URL` | Telegram | Another Bot API server (self-hosted, or a fake in tests) |
| `DEBUG` | `false` | Verbose logging |

### 💬 Commands

Everything is reachable from the menu. Commands are shortcuts.

| | |
|---|---|
| `/start` `/help` `/cancel` `/skip` | Basics |
| `/add` `/expense` `/income` `/transfer` | Record an operation |
| `/month` `/report` `/stats` `/last` `/search` `/export` `/undo` | Look at the books |
| `/accounts` `/newaccount` `/categories` | Accounts and categories |
| `/budget` `/recurring` | Limits and scheduled operations |
| `/debts` | Debts and loans |
| `/rate` `/rates` `/currency` `/timezone` `/settings` | Rates and preferences |
| `/erase` | Erase all my data |

---

## ☁️ Deploying to the cloud

The bot receives updates in one of two ways, chosen by `WEBHOOK_URL`:

| | When to use | Needs |
|---|---|---|
| **Long polling** (default) | Always-on host: VPS, Fly.io, Railway, Render, k8s | Nothing inbound: no domain, no open port, works behind NAT |
| **Webhook** | Platforms that sleep the instance: Cloud Run with `min-instances=0`, Lambda | A public HTTPS endpoint |

**Always-on host:** set `TELEGRAM_BOT_TOKEN`, `DATABASE_URL` and `ALLOWED_TELEGRAM_IDS`, then deploy. `GET /healthz` on `:8080` pings the database, so platform health checks work out of the box.

**Scale-to-zero:**

```
WEBHOOK_URL=https://<your-service-url>/telegram/webhook
SCHEDULER_INTERVAL=0            # a sleeping instance cannot tick
SCHEDULER_TOKEN=<random string> # guards the external trigger
```

Then point an external cron (Cloud Scheduler, GitHub Actions, cron-job.org) at the bot so statements and recurring operations still happen:

```bash
curl -X POST https://<your-service-url>/tasks/run \
     -H "Authorization: Bearer $SCHEDULER_TOKEN"
```

Hourly is plenty. Each step records what it has already done, so extra calls are harmless. The endpoint returns `202` immediately and runs the pass in the background.

### 🗄 Database

Any Postgres 14+ works. For a managed one (Neon, Supabase, Cloud SQL), keep the provider's SSL parameters in the URL: `?sslmode=require`.

Every file in `migrations/` runs at every start, in filename order, in one transaction under an advisory lock. Every statement is idempotent (`CREATE TABLE IF NOT EXISTS`, `ADD COLUMN IF NOT EXISTS`, …), so a database that is behind gets only what it's missing. To add a schema change, write a new idempotent file (`003_….sql`). Data rewrites are done by hand, not in these files.

---

## 🔧 How it works

```
          ┌─ getUpdates (long poll) ──┐
Telegram ─┤     or POST /webhook      ├─ worker ─┬─ messages  ─┬─ commands, quick entry
          └─ sendMessage / answerCB ──┘          └─ callbacks ─┴─ dialog state → Postgres
                       ▲
                       └──── scheduler (ticker or POST /tasks/run) ── statements, salary
```

Inline buttons arrive as `callback_query` with a compact `action:arg` payload. The bottom menu is plain text. A multi-step wizard has no session of its own, so its state lives in `user_states` as JSON, which is why restarting mid-dialog is harmless.

The image is a static binary on `distroless/static`: no shell, no package manager, non-root, with timezone data compiled in. It is its own health probe: `HEALTHCHECK` runs `bot -health`, which calls `/healthz` on the running instance.

<details>
<summary><b>Project layout</b></summary>

<br>

```
cmd/bot             entry point
internal/bot        Telegram interface: commands, buttons, dialogs
internal/config     settings from the environment and .env
internal/storage    Postgres: models and queries
internal/report     builds the statement text and the AI prompt
internal/parser     parses the periods and dates reports are asked for
internal/money      minor units: parsing, formatting, conversion
internal/rates      official NBRK exchange rates
internal/scheduler  statements, recurring operations, reminders
internal/web        webhook endpoint, health probe, scheduler trigger
migrations          idempotent SQL, run at every start, embedded in the binary
resource/schema.sql the first draft of the schema, kept for reference only
```

</details>

<details>
<summary><b>Data model</b></summary>

<br>

- `users`: timezone, main currency, monthly statement flag, `onboarded_at`
- `currencies`: reference table with decimal places
- `accounts`: one row per place and currency (`UNIQUE (user_id, name, currency)`), each with its own starting balance
- `categories`: only the user's own; nothing is seeded
- `transactions`: income, expense, transfers, and `debt_in` / `debt_out`. `original_amount` / `original_currency` / `rate` hold what a converted operation actually cost. `loan_id` / `interest` tie a payment to a loan
- `loans`: kind, name, currency, starting amount, account, monthly payment and day. What is owed now is never stored; it's worked out from linked operations
- `exchange_rates`: market rates by date, used only for the estimated summary line
- `budgets`: monthly limits per category
- `recurring`: scheduled operations
- `user_states`: dialog state, survives restarts

Everything except `exchange_rates` is keyed by user, so erasing a person deletes all of their data in one transaction.

</details>

---

## 🧪 Development

```bash
make run         # reads .env by docker's rules and runs ./cmd/bot
make test        # unit tests: money, parser, conversion, config, HTTP layer
make test-db     # plus end-to-end tests against a real Postgres
make fmt vet     # formatting and static checks
```

`make test-db` uses a separate database (`TEST_DB=...`), never the one you actually use.

**Language:** the bot speaks English. Amounts print as `1,500.50` and are read from either `1,500.50` or `1 500,50`. Report periods accept English words and month names (`last`, `september`, `2025`, `01.09-15.09`). Russian ones work too.

---

## 📄 License

[MIT](LICENSE) © 2026 Amir Dikhanbayev
