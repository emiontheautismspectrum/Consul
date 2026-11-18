#!/bin/sh
# End-to-end scenario through nginx: health → register → consent → calculators → history →
# stats → AI plan (if LLM configured) → refresh rotation → logout.
set -eu
URL=${1:-https://localhost}
JAR=$(mktemp)
trap 'rm -f "$JAR"' EXIT
C="curl -sk -b $JAR -c $JAR -H Content-Type:application/json"
EMAIL="smoke$(date +%s)@example.uz"
pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; }
fail() { printf '  \033[31m✗\033[0m %s\n%s\n' "$1" "${2:-}"; exit 1; }
expect() { # expect <name> <expected-status> <curl args...>
  name=$1 want=$2; shift 2
  body=$($C -o /tmp/smoke_body.$$ -w '%{http_code}' "$@") || true
  [ "$body" = "$want" ] && pass "$name ($body)" || fail "$name: got $body, want $want" "$(cat /tmp/smoke_body.$$)"
}
out() { cat /tmp/smoke_body.$$; }
expect_upload() { # like expect, but multipart/form-data (no JSON content type)
  name=$1 want=$2; shift 2
  body=$(curl -sk -b "$JAR" -c "$JAR" -o /tmp/smoke_body.$$ -w '%{http_code}' "$@") || true
  [ "$body" = "$want" ] && pass "$name ($body)" || fail "$name: got $body, want $want" "$(cat /tmp/smoke_body.$$)"
}

echo "Smoke test against $URL"
expect "readyz" 200 "$URL/api/v1/readyz"
expect "docs" 200 "$URL/api/v1/docs"
expect "unknown v1 route → 404" 404 "$URL/api/v1/nope"
expect "register" 201 -X POST "$URL/api/v1/auth/register" -d "{\"email\":\"$EMAIL\",\"password\":\"password123\",\"lang\":\"uz\"}"
grep -q access_token "$JAR" && grep -q refresh_token "$JAR" && pass "HttpOnly cookies set" || fail "cookies missing"
expect "me" 200 "$URL/api/v1/me"
expect "generate without consent → 403" 403 -X POST "$URL/api/v1/advisor/generate" -d '{"niche":"cafe","budget":100000000}'
expect "consent" 200 -X POST "$URL/api/v1/me/consent"
expect "loan (annuity)" 200 -X POST "$URL/api/v1/calculator/calculate" -d '{"amount":100000000,"annual_rate":24,"term_months":12,"method":"annuity"}'
out | grep -q '"monthly_payment":9455959.66' && pass "annuity payment = 9 455 959.66" || fail "wrong annuity" "$(out)"
out | grep -q 'lex.uz' && pass "sources include lex.uz" || fail "no sources"
expect "tax" 200 -X POST "$URL/api/v1/calculator/tax" -d '{"entity_type":"llc","annual_revenue":600000000,"annual_expenses":300000000,"annual_payroll":120000000}'
expect "breakeven" 200 -X POST "$URL/api/v1/calculator/breakeven" -d '{"initial_investment":60000000,"fixed_costs_monthly":30000000,"price_per_unit":25000,"variable_cost_per_unit":10000,"expected_units_monthly":3000}'
expect "validation error → 422" 422 -X POST "$URL/api/v1/calculator/calculate" -d '{"amount":-1,"annual_rate":24,"term_months":12,"method":"annuity"}'
expect "history" 200 "$URL/api/v1/calculator/history?limit=10"
[ "$(out | grep -o '"kind"' | wc -l)" -eq 3 ] && pass "3 calculations in history" || fail "history count" "$(out)"
FIX=$(dirname "$0")/fixtures
expect_upload "photo: non-image → 415" 415 -X POST "$URL/api/v1/photo" -F "file=@$0;type=image/png"
expect "stats" 200 "$URL/api/v1/me/stats"
out | grep -q '"large_loans_calculated":1' && pass "stats: 1 large loan" || fail "stats" "$(out)"

if [ "${SKIP_AI:-0}" != 1 ]; then
  code=$($C -o /tmp/smoke_body.$$ -w '%{http_code}' --max-time 300 -X POST "$URL/api/v1/advisor/generate" -d '{
    "lang":"uz","niche":"Kofe nuqtasi","region":"Toshkent","entity_type":"ie","budget":150000000,"own_funds":50000000,
    "loan":{"amount":100000000,"annual_rate":24,"term_months":24,"method":"annuity"},
    "price_per_unit":25000,"variable_cost_per_unit":9000,"expected_units_monthly":2500,"fixed_costs_monthly":25000000,"employees":3,"avg_salary":4000000}')
  if [ "$code" = 201 ]; then
    pass "AI plan generated"
    out | grep -q '"name":"calculate_' && pass "LLM called deterministic calculators" || fail "no tool calls" "$(out)"
    expect "plans list" 200 "$URL/api/v1/advisor/plans?limit=10&offset=0"
    expect_upload "photo: recognize invoice" 200 --max-time 200 -X POST "$URL/api/v1/photo" -F "file=@$FIX/invoice.png" -F lang=ru
    out | grep -q '"document_type":"invoice"' && pass "photo: detected as invoice" || fail "photo type" "$(out)"
    out | grep -q '14 000 000' && pass "photo: total amount transcribed" || fail "photo amount" "$(out)"
  elif [ "$code" = 503 ]; then
    printf '  \033[33m!\033[0m AI skipped: LLM not configured (set BASE_URL/AI_API_KEY)\n'
  else
    fail "generate: $code" "$(out)"
  fi
fi

OLD_REFRESH=$(awk '$6=="refresh_token"{print $7}' "$JAR")
expect "refresh" 200 -X POST "$URL/api/v1/auth/refresh"
code=$(curl -sk -o /dev/null -w '%{http_code}' -X POST "$URL/api/v1/auth/refresh" -H "Cookie: refresh_token=$OLD_REFRESH")
[ "$code" = 401 ] && pass "reuse of rotated refresh token rejected (401)" || fail "rotated refresh reuse: got $code"
expect "logout" 204 -X POST "$URL/api/v1/auth/logout"
expect "me after logout → 401" 401 "$URL/api/v1/me"

# Redis rate limiting: headers on API responses and per-account login lockout.
curl -sk -D /tmp/smoke_hdr.$$ -o /dev/null "$URL/api/v1/calculator/rules"
grep -qi '^x-ratelimit-remaining:' /tmp/smoke_hdr.$$ && pass "X-RateLimit headers present" || fail "no X-RateLimit headers" "$(cat /tmp/smoke_hdr.$$)"
rm -f /tmp/smoke_hdr.$$
for i in 1 2 3 4 5; do $C -o /dev/null -X POST "$URL/api/v1/auth/login" -d "{\"email\":\"$EMAIL\",\"password\":\"wrong-password\"}"; done
expect "account locked after 5 failed logins → 429" 429 -X POST "$URL/api/v1/auth/login" -d "{\"email\":\"$EMAIL\",\"password\":\"password123\"}"
out | grep -q too_many_attempts && pass "lockout error code" || fail "lockout code" "$(out)"
rm -f /tmp/smoke_body.$$
echo "All checks passed"
