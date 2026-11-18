#!/bin/sh
# End-to-end ledger scenario: register → ops with idempotency → today → debtors →
# obligations → undo → cursor pagination → foreign-user isolation on DELETE.
set -eu
URL=${1:-http://localhost:8080}
JAR=$(mktemp); JAR2=$(mktemp)
trap 'rm -f "$JAR" "$JAR2"' EXIT
C="curl -sk -b $JAR -c $JAR -H Content-Type:application/json"
EMAIL="ledger$(date +%s)@example.uz"
EMAIL2="stranger$(date +%s)@example.uz"
pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; }
fail() { printf '  \033[31m✗\033[0m %s\n%s\n' "$1" "${2:-}"; exit 1; }
expect() { # expect <name> <expected-status> <curl args...>
  name=$1 want=$2; shift 2
  body=$($C -o /tmp/ledger_smoke.$$ -w '%{http_code}' "$@") || true
  [ "$body" = "$want" ] && pass "$name ($body)" || fail "$name: got $body, want $want" "$(cat /tmp/ledger_smoke.$$)"
}
out() { cat /tmp/ledger_smoke.$$; }

echo "Ledger smoke test against $URL"
expect "readyz" 200 "$URL/api/v1/readyz"
expect "register" 201 -X POST "$URL/api/v1/auth/register" -d "{\"email\":\"$EMAIL\",\"password\":\"password123\",\"lang\":\"uz\"}"

expect "today on empty ledger" 200 "$URL/api/v1/ledger/today"
out | grep -q '"free":0' && pass "free = 0" || fail "empty free" "$(out)"

expect "POST /ops without key → 400" 400 -X POST "$URL/api/v1/ops" -d '{"kind":"sale","amount":1200000,"method":"naqd"}'
out | grep -q idempotency_key_required && pass "code idempotency_key_required" || fail "wrong error code" "$(out)"

expect "sale 1 200 000" 201 -X POST "$URL/api/v1/ops" -H 'Idempotency-Key: smoke-1' -d '{"kind":"sale","amount":1200000,"method":"naqd"}'
OP_ID=$(out | jq -r .op.id); FREE1=$(out | jq .free_after)
# Default business is YaTT turnover: tax = 1% of the month's income.
[ "$FREE1" = 1188000 ] && pass "free_after = 1 188 000 (−1% tax)" || fail "free_after" "$(out)"

expect "replay same key → 200" 200 -X POST "$URL/api/v1/ops" -H 'Idempotency-Key: smoke-1' -d '{"kind":"sale","amount":1200000,"method":"naqd"}'
[ "$(out | jq -r .op.id)" = "$OP_ID" ] && pass "replay returns the original op" || fail "replay op id" "$(out)"
expect "today after replay" 200 "$URL/api/v1/ledger/today"
[ "$(out | jq .view.naqd)" = 1200000 ] && pass "naqd still 1 200 000 (no double insert)" || fail "double insert" "$(out)"

expect "nasiya to Bekzod" 201 -X POST "$URL/api/v1/ops" -H 'Idempotency-Key: smoke-2' -d '{"kind":"nas","amount":500000,"counterparty":"Бекзод"}'
expect "debtors" 200 "$URL/api/v1/debtors"
DEBTOR_ID=$(out | jq -r '.items[0].id')
[ "$(out | jq .total)" = 500000 ] && pass "total owed = 500 000" || fail "debtors total" "$(out)"
expect "reminder (user lang uz)" 200 "$URL/api/v1/debtors/$DEBTOR_ID/reminder"
out | grep -q 'Бекзод' && pass "reminder names the debtor" || fail "reminder text" "$(out)"
expect "repaid full" 201 -X POST "$URL/api/v1/debtors/$DEBTOR_ID/repaid" -H 'Idempotency-Key: smoke-3' -d '{}'
expect "debtors after repaid → empty" 200 "$URL/api/v1/debtors"
[ "$(out | jq .total)" = 0 ] && pass "debt cleared" || fail "debt still there" "$(out)"

expect "obligation: rent day 5" 201 -X POST "$URL/api/v1/obligations" -d '{"kind":"rent","label":"ijara","amount":3000000,"due_day":5,"recurring":true}'
OBL_ID=$(out | jq -r .id)
out | grep -q next_due && pass "next_due present" || fail "no next_due" "$(out)"
expect "obligations list" 200 "$URL/api/v1/obligations"
expect "obligation patch" 200 -X PATCH "$URL/api/v1/obligations/$OBL_ID" -d '{"kind":"rent","label":"ijara","amount":3100000,"due_day":5,"recurring":true}'
expect "obligation delete" 204 -X DELETE "$URL/api/v1/obligations/$OBL_ID"

expect "undo sale" 200 -X DELETE "$URL/api/v1/ops/$OP_ID"
# After undo: income 500 000 (nas) → tax 5 000; cash 500 000 (repaid) → free 495 000.
out | grep -q '"free_after":495000' && pass "undo: free 1 683 000 → 495 000 (repaid stays)" || fail "undo math" "$(out)"

expect "ops list" 200 "$URL/api/v1/ops?limit=1"
CUR=$(out | jq -r .next_cursor)
[ "$CUR" != null ] && pass "next_cursor present" || fail "no cursor" "$(out)"
expect "ops page 2" 200 "$URL/api/v1/ops?limit=1&cursor=$CUR"

# P1: bank report, what-if, plan, memory, export, survey.
expect "bank report" 200 "$URL/api/v1/reports/bank?months=3"
[ "$(out | jq '.months | length')" = 4 ] && pass "bank report: 3 closed months + the current one" || fail "bank months" "$(out)"
expect "bank report: months out of range → 400" 400 "$URL/api/v1/reports/bank?months=99"
expect "what-if loan" 200 -X POST "$URL/api/v1/forecast/whatif" -d '{"kind":"loan","amount":20000000,"annual_rate":24,"term_months":24}'
[ "$(out | jq '.scenario | length')" = 30 ] && pass "what-if: 30-day series" || fail "what-if" "$(out)"
expect "what-if: bad kind → 422" 422 -X POST "$URL/api/v1/forecast/whatif" -d '{"kind":"crypto"}'
expect "plan" 200 -X POST "$URL/api/v1/advisor/plan" -d '{"capital":30000000,"sales":25000000,"rent":2700000,"helper":2000000}'
[ "$(out | jq .estimated)" = true ] && pass "plan: no history yet → 64% flagged as an assumption" || fail "plan estimated" "$(out)"
expect "memory: add" 200 -X PUT "$URL/api/v1/memory" -d '{"key":"Maqsad","value":"ikkinchi rasta"}'
expect "memory: forget" 204 -X DELETE "$URL/api/v1/memory/Maqsad"
expect "export csv" 200 "$URL/api/v1/me/export.csv"
head -c 3 /tmp/ledger_smoke.$$ | od -An -tx1 | grep -q "ef bb bf" && pass "CSV starts with a UTF-8 BOM" || fail "no BOM"
expect "survey" 204 -X POST "$URL/api/v1/me/survey" -d '{"question":"why_not_registered","answer":"fear_tax"}'
expect "chats list" 200 "$URL/api/v1/chats"

# A second user must not see or delete the first user's data.
curl -sk -b "$JAR2" -c "$JAR2" -H Content-Type:application/json -o /dev/null -X POST "$URL/api/v1/auth/register" -d "{\"email\":\"$EMAIL2\",\"password\":\"password123\"}"
code=$(curl -sk -b "$JAR2" -o /tmp/ledger_smoke.$$ -w '%{http_code}' -X DELETE "$URL/api/v1/ops/$OP_ID" || true)
[ "$code" = 404 ] && pass "foreign DELETE → 404" || fail "foreign DELETE: got $code, want 404"
code=$(curl -sk -b "$JAR2" -o /tmp/ledger_smoke.$$ -w '%{http_code}' "$URL/api/v1/ledger/today" || true)
[ "$code" = 200 ] && grep -q '"cash":0' /tmp/ledger_smoke.$$ && pass "foreign today is empty" || fail "foreign today" "$(cat /tmp/ledger_smoke.$$)"

# Account deletion: needs ?confirm=delete, erases everything, the session stops working.
code=$(curl -sk -b "$JAR2" -c "$JAR2" -o /dev/null -w '%{http_code}' -X DELETE "$URL/api/v1/me" || true)
[ "$code" = 400 ] && pass "DELETE /me without confirm → 400" || fail "delete without confirm: $code"
code=$(curl -sk -b "$JAR2" -c "$JAR2" -o /dev/null -w '%{http_code}' -X DELETE "$URL/api/v1/me?confirm=delete" || true)
[ "$code" = 204 ] && pass "DELETE /me?confirm=delete → 204" || fail "delete account: $code"
code=$(curl -sk -b "$JAR2" -o /dev/null -w '%{http_code}' "$URL/api/v1/me" || true)
[ "$code" = 401 ] && pass "the deleted account's session is gone (401)" || fail "me after delete: $code"

rm -f /tmp/ledger_smoke.$$
echo "All ledger smoke checks passed."
