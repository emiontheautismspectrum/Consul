# Consul — an AI money assistant for small businesses in Uzbekistan

**English** · [Русский](README.ru.md)

Consul is a pocket accountant for small traders. The owner writes or says *"sold 1 200 000 cash"*, and Consul keeps the books, answers **"how much can I spend today?"**, calculates taxes and loans, reads photos of receipts and invoices, compares prices with the market, and explains it all in Uzbek or Russian.

Built for the Umummilliy AI Hackathon (Tashkent, 17–19 Nov 2026), Chamber of Commerce problems 20 and 21. Demo persona: *Dilnoza, dried fruit, Chorsu bazaar, sole proprietor (YaTT)*.

> **The rule behind everything: code computes every number, the LLM only understands language and explains.** A number the assistant shows must come from a tool result; otherwise the answer is rejected and a template answer is returned instead.

> ⚠️ Informational only — **not tax, legal or credit advice.** Tax rules come from the Tax Code ([lex.uz](https://lex.uz/docs/4674902), [soliq.uz](https://soliq.uz)) and must be checked against the current edition.

📊 [Pitch Day deck (PDF, Russian)](Consul-Pitch-Day.pdf)

## What it does

| Area | What you get |
|---|---|
| **Ledger** | One source of truth (`ops` table): sales, purchases, expenses, credit sales (nasiya), repayments. Cash/card balances, debtors, profit, "free to spend" after tax and reserve. Money is integer so'm, days follow Asia/Tashkent. Undo, idempotent writes. |
| **Consul chat** | Text parser for operations (no LLM) → a draft the user confirms; an LLM agent with tools for questions. Streaming (SSE), saved chats, memory of facts the user told it, Uzbek and Russian. |
| **Grounding** | Tools: `get_today`, `get_months`, `list_debtors`, `get_obligations`, `compute_tax`, `compute_loan`, `query_ops` (safe read-only search of the user's own operations), `market_prices`, `remember_fact`. Numbers in the reply are checked against tool output; customer names reach the model only as `D1`, `D2`… |
| **Knowledge base** | Curated entries (margin, turnover, tax regimes, state support…) with sources; lexical retrieval (not vector search). Tax and support rules are marked "check soliq.uz". |
| **Photos** | `POST /photo` — a vision model (kimi-k3 through the gateway) reads receipts, invoices and documents, returns fields, line items and draft operations. |
| **Voice** | ElevenLabs speech-to-text (Scribe) and text-to-speech (Eleven v3, which speaks Uzbek). Answers are read aloud with amounts spoken the way people say them ("4,2 mln"). |
| **Calculators** | Loan (annuity / differentiated), Uzbek taxes, break-even — pure Go functions, public endpoints. |
| **What-if, plan, bank report** | "What if I buy 15 mln of goods / take a 34 mln loan?", a start-up plan, a one-page financial report for a bank. |
| **Market prices** | Official weekly averages from [narx.stat.uz](https://narx.stat.uz), anonymous bazaar medians from users' receipts (only when ≥ 5 different users bought the product), and the user's own price. |
| **Chamber report** | Aggregates for the Chamber of Commerce: only users who consented, groups of fewer than 5 are never returned, bootstrap confidence intervals. The demo population is synthetic and flagged as such. |
| **Privacy** | Consent screens, data export (CSV), account deletion, PII masking before any LLM call. |

## Architecture

```
 browser ──► nginx (TLS, canonical host, rate limits) ─┬─► Next.js 16 (SSR, standalone)   /
                                                       └─► Go API (net/http)              /api/v1/*
 Go modular monolith — modules talk through interfaces wired in backend/cmd/api/main.go
   auth · user · ledger · consul (chat agent) · kb · vision · speech · calculator
   advisor · palata · prices · infrastructure (config, db, cache, llm, httpx, pii, ratelimit)
        │                         │
   PgBouncer ► PostgreSQL 17     Redis 7 (JWT blacklist, cache, rate limits, OTP hashes)
        │
   LLM gateway (OpenAI-compatible) · ElevenLabs · narx.stat.uz
```

Why this stack: Go gives one small static binary and fast tests for the money math; Postgres with goose migrations keeps the ledger transactional; PgBouncer lets many connections share a small pool (transaction mode, so pgx runs in simple protocol); Redis holds short-lived state; Next.js SSR gives fast first paint on cheap phones; nginx terminates TLS and rate-limits before Go sees traffic.

## Quick start

Requirements: Docker with compose v2. Go ≥ 1.27 and Node ≥ 20 only for running tests outside Docker.

```bash
make up          # creates .env with random secrets, a self-signed certificate, starts the whole stack
make smoke       # end-to-end scenario against https://localhost
```

- API docs (Swagger UI): `https://localhost/api/v1/docs` (self-signed certificate, the browser will warn).
- Put `BASE_URL`, `AI_API_KEY` (and optionally `AI_MODEL`) into `.env` for the assistant, then `docker compose up -d api`. Without an LLM the chat still answers from templates.
- **Demo login:** set `APP_ENV=demo` and `DEMO_OTP=123456` in `.env` (and `docker compose up -d api`), then log in with phone `+998901234567` and code **`123456`**. In this mode every phone accepts the demo code, so never enable it on a public production instance. `make demo-reset` restores the reference ledger (free to spend = 4 200 000 so'm).

```bash
make test        # Go tests (+ vet)
cd frontend && npm install && npm test && npm run typecheck && npm run dev
```

PostgreSQL integration tests run when `LEDGER_IT_DSN` is set (see `backend/internal/ledger/repo_integration_test.go`).

## API

All routes live under `/api/v1`; the contract is [`backend/api/openapi.yaml`](backend/api/openapi.yaml). Errors always look like `{"error":{"code","field","message"}}`. Writes need an `Idempotency-Key`.

| Group | Endpoints |
|---|---|
| Auth | `/auth/otp/send`, `/auth/otp/verify`, `/auth/register`, `/auth/login`, `/auth/refresh`, `/auth/logout`, `/auth/demo` |
| Ledger | `/ledger/today`, `/ops`, `/ops/parse`, `/ops/{id}`, `/debtors`, `/obligations`, `/forecast/whatif`, `/reports/bank` |
| Assistant | `/consul/ask` (JSON or SSE), `/chats`, `/memory`, `/photo`, `/voice/stt`, `/voice/tts` |
| Calculators | `/calculator/calculate`, `/calculator/tax`, `/calculator/breakeven`, `/calculator/rules` |
| Market & Chamber | `/prices`, `/palata/report`, `/palata/report.csv` |
| Account | `/me`, `/me/business`, `/me/consent`, `/me/export.csv`, `DELETE /me` |

## Assistant quality

The assistant has a built-in test harness: `api eval-chat` asks 54 questions (Uzbek and Russian) on the reference ledger and checks the numbers, refusals, topics, sources, latency and fallbacks.

```bash
docker compose exec -T api /api eval-chat        # locally
make eval-chat-vps MODEL=deepseek-v4.1-flash     # on the server
```

Latest run with `deepseek-v4.1-flash`: **40 / 54 (74 %)**, 43 answered by the model itself, median 2.8 s, p95 5.8 s. The rest fall back to templates, mostly because the number check rejected a figure the model produced without a tool (tax-rule questions, "how to grow profit"). We keep that strictness on purpose: a wrong number is worse than a template. Other models tried on this gateway did worse (gemini-2.5-flash 7/50).

## Deploy (VPS)

Only configuration files go to the server; code ships as Docker Hub images.

```bash
make publish publish-web   # build and push xsenos/consul-api and xsenos/consul-web
make deploy                # copy compose + nginx templates, docker compose pull && up -d
```

Settings (`DOCKERHUB_USER`, `DEPLOY_HOST`, `DEPLOY_USER`, `DEPLOY_PATH`, `DEPLOY_DOMAIN`, …) live in `.env`. With a domain, nginx serves a canonical host and Let's Encrypt certificates (`deploy/vps/letsencrypt.sh`). Host hardening (key-only SSH, nftables firewall with rate limits, fail2ban, journald cap) is applied separately by `make vps-host` from `deploy/vps/host`.

Useful targets: `vps-ps`, `vps-logs`, `demo-reset-vps`, `palata-seed-vps`, `palata-refresh-vps`, `prices-seed-vps`, `prices-push-vps`, `eval-chat-vps`.

## Configuration

Everything is read from `.env` (see [`.env.example`](.env.example)); secrets are never committed.

| Variable | Meaning |
|---|---|
| `BASE_URL`, `AI_API_KEY`, `AI_MODEL` | OpenAI-compatible LLM gateway and default model |
| `AI_CHAT_MODEL`, `AI_CHAT_DEADLINE` | optional chat model override and answer deadline (default 20 s) |
| `APP_ENV`, `DEMO_OTP`, `DEMO_PHONE` | `APP_ENV=demo` enables the fixed demo code (`123456`) and `POST /auth/demo` |
| `POSTGRES_*`, `REDIS_PASSWORD` | database and cache |
| `PEPPER`, `JWT_ACCESS_SECRET`, `JWT_REFRESH_SECRET` | auth secrets (generated by `make env`) |
| `ALLOWED_ORIGINS`, `COOKIE_SECURE`, `COOKIE_DOMAIN` | CORS and cookies |
| `DOMAIN`, `LETSENCRYPT_EMAIL` | TLS for production |
| `ELEVENLABS_API_KEY` | speech provider key; without it `/voice/*` answers 503 and the UI falls back to typing |
| `ELEVENLABS_VOICE_UZ/RU`, `ELEVENLABS_TTS_MODEL`, `ELEVENLABS_STT_MODEL` | optional voice ids and models (defaults: a premade voice, `eleven_v3`, `scribe_v2`) |
| `DOCKERHUB_USER`, `DEPLOY_*` | publishing and deploying |

## Security and data

- Passwords/OTP: Argon2id with salt and pepper (memory-bounded), JWT access + refresh with rotation and a Redis blacklist.
- Every handler takes the user from the JWT, never from the body.
- Rate limits at three levels: nginx zones, Redis per user/IP, host nftables. fail2ban bans repeated abusers.
- No personal data in logs or LLM prompts: names become `D1`…, phones, IDs and card numbers are masked.
- Chamber statistics: consent only, k-anonymity with k = 5.

## Known limitations

- `narx.stat.uz` does not answer from our VPS (the IP range is blocked). Prices are fetched on a developer machine and imported: `make prices-push-vps`. Korzinka has no public price API, so it is not connected.
- Voice needs an `ELEVENLABS_API_KEY`, and ElevenLabs refuses some countries (our VPS egress is in one): set `ELEVENLABS_PROXY_URL` to a proxy in a supported country (or run `make voice-tunnel-install` on a machine in one: it opens a reverse SOCKS5 tunnel as a systemd user service, see `deploy/tunnel/`), otherwise `/voice/*` answers 503 and the UI falls back to typing. SMS delivery is not connected (OTP is demo-only).
- Tax and state-support entries are curated, not automatically synchronised with lex.uz.
- The frontend still ships a few static screens (Learn, the state-support list in Kredit, plan presets).

## Repository layout

```
backend/   Go API: cmd/api, internal/<module>, migrations (goose), api/openapi.yaml
frontend/  Next.js 16 app (see frontend/README.md)
deploy/    nginx templates, VPS compose, host hardening (nftables, fail2ban), scripts
scripts/   smoke tests
docker-compose.yml   local stack          Makefile   all commands
```
