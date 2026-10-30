#!/usr/bin/env bash
# Exercise the deployed API's auth and accounting paths through its public URL.

set -euo pipefail

usage() {
	cat <<'EOF'
Usage: scripts/smoke-test.sh [BASE_URL] [--verbose]

Run a throwaway end-to-end check against a deployed Family Budget API. The
default base URL is http://127.0.0.1:8080. Every run creates a user, household,
account, envelope and financial transactions in the target database.

Options:
  --verbose    Print response bodies (normally hidden).
  -h, --help   Show this help without making any network requests.

Examples:
  scripts/smoke-test.sh
  scripts/smoke-test.sh https://<your-production-host>
  scripts/smoke-test.sh http://127.0.0.1:8080 --verbose
EOF
}

BASE_URL="http://127.0.0.1:8080"
VERBOSE=0
BASE_URL_SET=0
while [[ $# -gt 0 ]]; do
	case "$1" in
	-h | --help)
		usage
		exit 0
		;;
	--verbose) VERBOSE=1 ;;
	-*)
		printf '[smoke] ERROR: unknown option: %s (see --help)\n' "$1" >&2
		exit 2
		;;
	*)
		if (( BASE_URL_SET )); then
			printf '[smoke] ERROR: only one BASE_URL may be supplied (see --help)\n' >&2
			exit 2
		fi
		BASE_URL="$1"
		BASE_URL_SET=1
		;;
	esac
	shift
done
BASE_URL="${BASE_URL%/}"
if [[ ! "$BASE_URL" =~ ^https?://[^/]+$ ]]; then
	printf '[smoke] ERROR: BASE_URL must be an http(s) origin without a path\n' >&2
	exit 2
fi

if command -v python3 >/dev/null 2>&1; then
	JSON_EXTRACTOR="python3"
elif command -v sed >/dev/null 2>&1 && command -v grep >/dev/null 2>&1; then
	JSON_EXTRACTOR="sed/grep fallback"
else
	printf '[smoke] ERROR: JSON extraction requires python3 or both sed and grep\n' >&2
	exit 2
fi
printf '[smoke] JSON field extractor: %s\n' "$JSON_EXTRACTOR"

json_field() {
	local path="$1" field
	if [[ "$JSON_EXTRACTOR" == python3 ]]; then
		printf '%s' "$HTTP_BODY" | python3 -c '
import json, sys
value = json.load(sys.stdin)
for part in sys.argv[1].split("."):
    value = value[int(part)] if isinstance(value, list) else value[part]
if isinstance(value, (dict, list)):
    print(json.dumps(value, separators=(",", ":")))
elif value is None:
    print("null")
elif isinstance(value, bool):
    print(str(value).lower())
else:
    print(value)
' "$path"
		return
	fi
	field="${path##*.}"
	printf '%s' "$HTTP_BODY" |
		grep -oE "\"${field}\"[[:space:]]*:[[:space:]]*(\"[^\"\\]*(\\.[^\"\\]*)*\"|-?[0-9]+|true|false|null)" |
		sed -nE 's/^[^:]*:[[:space:]]*//; s/^"//; s/"$//; 1p'
}

HTTP_STATUS=""
HTTP_BODY=""
STEP_COUNT=0
PASS_COUNT=0
VALIDATION_ERROR=""
STEP_NOTE=""

show_error_details() {
	local code message
	if code="$(json_field error.code 2>/dev/null)" && [[ -n "$code" && "$code" != null ]]; then
		message="$(json_field error.message 2>/dev/null || true)"
		printf '[smoke] Error envelope: code=%s message=%s\n' "$code" "$message" >&2
	fi
	if (( VERBOSE )) && [[ -n "$HTTP_BODY" ]]; then
		printf '[smoke] Response body: %s\n' "$HTTP_BODY" >&2
	fi
}

fail_step() {
	local label="$1" detail="$2"
	printf '[FAIL] %s (HTTP %s): %s\n' "$label" "${HTTP_STATUS:-000}" "$detail" >&2
	show_error_details
	exit 1
}

request_step() {
	local label="$1" method="$2" path="$3" expected="$4" payload="$5" auth="$6" key="$7" validator="${8:-}"
	shift 8 || true
	local -a args
	args=(--silent --show-error --connect-timeout 10 --max-time 30 -X "$method" -H 'Accept: application/json')
	if [[ -n "$payload" ]]; then
		args+=(-H 'Content-Type: application/json' --data "$payload")
	fi
	if [[ "$auth" == 1 ]]; then
		args+=(-H "Authorization: Bearer $TOKEN")
	fi
	if [[ -n "$key" ]]; then
		args+=(-H "Idempotency-Key: $key")
	fi
	local response
	if ! response="$(curl "${args[@]}" -w $'\n%{http_code}' "$BASE_URL$path")"; then
		HTTP_STATUS="000"
		HTTP_BODY="$response"
		fail_step "$label" 'curl could not complete the request'
	fi
	HTTP_STATUS="${response##*$'\n'}"
	HTTP_BODY="${response%$'\n'*}"
	STEP_COUNT=$((STEP_COUNT + 1))
	case "|$expected|" in
	*"|$HTTP_STATUS|"*) ;;
	*) fail_step "$label" "expected HTTP $expected" ;;
	esac
	VALIDATION_ERROR=""
	STEP_NOTE=""
	if [[ -n "$validator" ]] && ! "$validator" "$@"; then
		fail_step "$label" "$VALIDATION_ERROR"
	fi
	PASS_COUNT=$((PASS_COUNT + 1))
	local note=""
	if [[ -n "$STEP_NOTE" ]]; then
		note="; $STEP_NOTE"
	fi
	printf '[PASS] %s (HTTP %s)%s\n' "$label" "$HTTP_STATUS" "$note"
	if (( VERBOSE )) && [[ -n "$HTTP_BODY" ]]; then
		printf '[smoke] Response body: %s\n' "$HTTP_BODY"
	fi
}

expect_field() {
	local path="$1" expected="$2" actual
	if ! actual="$(json_field "$path" 2>/dev/null)"; then
		VALIDATION_ERROR="missing or invalid JSON field $path"
		return 1
	fi
	if [[ "$actual" != "$expected" ]]; then
		VALIDATION_ERROR="$path=$actual, expected $expected"
		return 1
	fi
	return 0
}

validate_registration() {
	if [[ "$HTTP_STATUS" == 409 ]]; then
		if ! expect_field error.code EMAIL_ALREADY_REGISTERED; then
			return 1
		fi
		STEP_NOTE='email already registered; continuing to login'
		return 0
	fi
	expect_field user.email "$EMAIL"
}

validate_token() {
	if ! TOKEN="$(json_field token 2>/dev/null)" || [[ -z "$TOKEN" || "$TOKEN" == null ]]; then
		VALIDATION_ERROR='missing or empty token field'
		return 1
	fi
	return 0
}

validate_value() {
	expect_field "$1" "$2"
}

validate_household() {
	if ! HOUSEHOLD_ID="$(json_field data.id 2>/dev/null)" || [[ -z "$HOUSEHOLD_ID" || "$HOUSEHOLD_ID" == null ]]; then
		VALIDATION_ERROR='missing or empty data.id household identifier'
		return 1
	fi
	expect_field data.name "$HOUSEHOLD_NAME"
}

validate_account() {
	if ! ACCOUNT_ID="$(json_field data.id 2>/dev/null)" || [[ -z "$ACCOUNT_ID" || "$ACCOUNT_ID" == null ]]; then
		VALIDATION_ERROR='missing or empty data.id account identifier'
		return 1
	fi
	if ! expect_field data.kind CHECKING; then return 1; fi
	expect_field data.budget_eligible true
}

validate_envelope() {
	if ! ENVELOPE_ID="$(json_field data.id 2>/dev/null)" || [[ -z "$ENVELOPE_ID" || "$ENVELOPE_ID" == null ]]; then
		VALIDATION_ERROR='missing or empty data.id envelope identifier'
		return 1
	fi
	return 0
}

validate_allocation() {
	if ! ALLOCATION_ID="$(json_field data.id 2>/dev/null)" || [[ -z "$ALLOCATION_ID" || "$ALLOCATION_ID" == null ]]; then
		VALIDATION_ERROR='missing or empty data.id allocation operation identifier'
		return 1
	fi
	if ! expect_field data.type ALLOCATE; then return 1; fi
	expect_field data.amount_minor 40000
}

validate_replay() {
	local first_id="$1"
	if ! expect_field data.id "$first_id"; then return 1; fi
	if ! expect_field data.type ALLOCATE; then return 1; fi
	if ! expect_field data.amount_minor 40000; then return 1; fi
}

validate_error_code() {
	expect_field error.code IDEMPOTENCY_KEY_PAYLOAD_MISMATCH
}

validate_balances() {
	local ready="$1" envelope="$2" account="$3" cash sum_envelopes
	if ! expect_field data.ready_to_assign_minor "$ready"; then return 1; fi
	if ! expect_field data.envelopes.0.balance_minor "$envelope"; then return 1; fi
	if ! expect_field data.accounts.0.posted_minor "$account"; then return 1; fi
	if ! cash="$(json_field data.budgetable_cash_minor 2>/dev/null)"; then
		VALIDATION_ERROR='missing or invalid JSON field data.budgetable_cash_minor'
		return 1
	fi
	# This smoke household creates one envelope, so its balance is the envelope sum.
	sum_envelopes="$envelope"
	if (( cash != ready + sum_envelopes )); then
		VALIDATION_ERROR="budget identity failed: budgetable_cash_minor=$cash, ready_to_assign_minor=$ready, envelope sum=$sum_envelopes"
		return 1
	fi
	STEP_NOTE="ready_to_assign_minor=$ready, envelope balance_minor=$envelope, account posted_minor=$account, budgetable_cash_minor=$cash"
	return 0
}

validate_ready_to_assign() {
	expect_field data.ready_to_assign_minor 100000
}

EPOCH="$(date +%s)"
RAND="${RANDOM}${RANDOM}"
EMAIL="smoke-${EPOCH}-${RAND}@example.com"
PASSWORD="Sm0ke-${EPOCH}-${RAND}-Strong!"
TODAY="$(date -u +%F)"
HOUSEHOLD_NAME="smoke-test $EPOCH"
TOKEN=""
HOUSEHOLD_ID=""
ACCOUNT_ID=""
ENVELOPE_ID=""
ALLOCATION_ID=""
IDEMPOTENCY_KEY="smoke-alloc-${EPOCH}"

request_step '1. Liveness probe GET /healthz' GET /healthz 200 '' 0 ''
request_step '2. Readiness probe GET /readyz' GET /readyz 200 '' 0 ''
request_step '3. Register POST /api/v1/auth/register' POST /api/v1/auth/register '201|409' \
	"{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" 0 '' validate_registration
request_step '4. Login POST /api/v1/auth/login' POST /api/v1/auth/login 200 \
	"{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" 0 '' validate_token
request_step '5. Authenticated GET /api/v1/auth/me' GET /api/v1/auth/me 200 '' 1 '' validate_value user.email "$EMAIL"
request_step '6. Create household POST /api/v1/households' POST /api/v1/households 201 \
	"{\"name\":\"$HOUSEHOLD_NAME\",\"currency\":\"USD\",\"timezone\":\"UTC\"}" 1 '' validate_household
request_step '7. Create checking account POST /api/v1/households/{id}/accounts' POST \
	"/api/v1/households/$HOUSEHOLD_ID/accounts" 201 \
	'{"name":"Smoke checking","kind":"CHECKING","budget_eligible":true}' 1 '' validate_account
request_step '8. Record opening balance POST /api/v1/households/{id}/accounts/{accountID}/opening-balance' POST \
	"/api/v1/households/$HOUSEHOLD_ID/accounts/$ACCOUNT_ID/opening-balance" 201 \
	"{\"effective_date\":\"$TODAY\",\"delta_minor\":100000}" 1 "smoke-opening-${EPOCH}-${RAND}"
request_step '9. Verify opening balance GET /api/v1/households/{id}/balances' GET \
	"/api/v1/households/$HOUSEHOLD_ID/balances" 200 '' 1 '' validate_ready_to_assign
request_step '10. Create envelope POST /api/v1/households/{id}/envelopes' POST \
	"/api/v1/households/$HOUSEHOLD_ID/envelopes" 201 '{"name":"Smoke envelope"}' 1 '' validate_envelope
ALLOCATION_BODY="{\"effective_date\":\"$TODAY\",\"envelope_id\":\"$ENVELOPE_ID\",\"amount_minor\":40000}"
request_step '11. Allocate funds POST /api/v1/households/{id}/allocations' POST \
	"/api/v1/households/$HOUSEHOLD_ID/allocations" 201 "$ALLOCATION_BODY" 1 "$IDEMPOTENCY_KEY" validate_allocation
request_step '12. Replay identical allocation with the same Idempotency-Key' POST \
	"/api/v1/households/$HOUSEHOLD_ID/allocations" 201 "$ALLOCATION_BODY" 1 "$IDEMPOTENCY_KEY" validate_replay "$ALLOCATION_ID"
request_step '12. Verify replay did not double-allocate GET /api/v1/households/{id}/balances' GET \
	"/api/v1/households/$HOUSEHOLD_ID/balances" 200 '' 1 '' validate_balances 60000 40000 100000
request_step '13. Reject changed allocation payload with the same key' POST \
	"/api/v1/households/$HOUSEHOLD_ID/allocations" 409 \
	"{\"effective_date\":\"$TODAY\",\"envelope_id\":\"$ENVELOPE_ID\",\"amount_minor\":40001}" \
	1 "$IDEMPOTENCY_KEY" validate_error_code
request_step '14. Record debit purchase POST /api/v1/households/{id}/transactions/purchases' POST \
	"/api/v1/households/$HOUSEHOLD_ID/transactions/purchases" 201 \
	"{\"effective_date\":\"$TODAY\",\"purchase_kind\":\"DEBIT\",\"account_id\":\"$ACCOUNT_ID\",\"envelope_id\":\"$ENVELOPE_ID\",\"amount_minor\":15000}" \
	1 "smoke-purchase-${EPOCH}-${RAND}"
request_step '15. Verify final accounting GET /api/v1/households/{id}/balances' GET \
	"/api/v1/households/$HOUSEHOLD_ID/balances" 200 '' 1 '' validate_balances 60000 25000 85000

printf '[smoke] Summary: %s checks run, %s passed.\n' "$STEP_COUNT" "$PASS_COUNT"
printf '[smoke] Created household id: %s\n' "$HOUSEHOLD_ID"
printf '[smoke] Created email: %s\n' "$EMAIL"
printf '[smoke] This run created throwaway data in the target database.\n'
