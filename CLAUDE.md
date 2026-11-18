# Consul API

Consul is a financial assistant for small traders in Uzbekistan (Umummilliy AI Hackathon, Tashkent, 17-19 Nov 2026, Chamber of Commerce problems 20 and 21). Persona for the demo: Dilnoza, dried fruits, Chorsu bazaar, individual entrepreneur (YaTT).

Product rules:
- Code computes every number. The LLM only understands language and explains results. A number the LLM shows must come from a tool result.
- The ledger (table `ops`) is the single source of truth. Balances, debts, profit and reports are computed from it.
- Money is int64 so'm at the API and BIGINT in the DB. No float64 for stored money. Day and month boundaries use Asia/Tashkent.
- Languages: uz (default, Latin script) and ru. Replies use the language of the user's message, else the profile language.
- The AI never writes to the ledger by itself. It returns `pending_op`; the frontend calls POST /ops after the user confirms.
- Privacy: no personal data in logs or in LLM prompts (anonymize names, phones, IDs). Chamber statistics only from users with stats consent, groups smaller than 5 are never stored or returned.
- Demo must not break: if the LLM or VoiceLab fails or times out, return template answers or a clear error code. Never a 500.

Engineering rules:
- Go, net/http ServeMux, modular monolith. Modules talk through interfaces wired in backend/cmd/api/main.go. No ORM, parameterized SQL only, migrations with goose (backend/migrations, never edit an applied one).
- All routes live under /api/v1: modules register relative paths, main mounts them once (httpx.Mount).
- Contract first: change backend/api/openapi.yaml before handlers. Error format: {"error":{"code":"snake_case","field":"...","message":"..."}}.
- Every handler reads user_id from the JWT only, never from the body or query.
- Table-driven tests. Reference numbers from the plan must be reproduced exactly.
- All code, comments, identifiers, commits and docs in English. Uzbek and Russian text only in template/prompt files under internal/*/locales/.
- Secrets only in .env (never committed). Keys never appear in logs or responses.
- Run `make test` and `go vet ./...` before finishing. Report what you could not verify.

Deploy: `make publish deploy` (image xsenos/consul-api on Docker Hub; only config files go to the VPS).
