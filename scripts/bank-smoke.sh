#!/usr/bin/env bash

set -euo pipefail

usage() {
	cat <<'EOF'
Usage: scripts/bank-smoke.sh <aspsps|connect|status> [options]

Exercise the local Enable Banking sandbox API. Login credentials and household
can be supplied with BANK_SMOKE_EMAIL, BANK_SMOKE_PASSWORD and
BANK_SMOKE_HOUSEHOLD. The API defaults to http://localhost:8080/api/v1.

Options:
  --api URL             API base URL (default: BANK_SMOKE_API)
  --email EMAIL         Login email (default: BANK_SMOKE_EMAIL)
  --household ID        Household UUID (default: BANK_SMOKE_HOUSEHOLD)
  --aspsp-name NAME     Exact provider name for connect
  --aspsp-country CODE  Provider country for connect (default: BG)
  -h, --help            Show this help without making network requests

Examples:
  BANK_SMOKE_EMAIL=owner@example.com BANK_SMOKE_PASSWORD='...' \
    BANK_SMOKE_HOUSEHOLD='<household-uuid>' scripts/bank-smoke.sh aspsps
  scripts/bank-smoke.sh connect --aspsp-name 'Fibank'
  scripts/bank-smoke.sh status
EOF
}

error() {
	printf '[bank-smoke] ERROR: %s\n' "$1" >&2
	exit 1
}

BANK_SMOKE_API="${BANK_SMOKE_API:-http://localhost:8080/api/v1}"
BANK_SMOKE_EMAIL="${BANK_SMOKE_EMAIL:-}"
BANK_SMOKE_PASSWORD="${BANK_SMOKE_PASSWORD:-}"
BANK_SMOKE_HOUSEHOLD="${BANK_SMOKE_HOUSEHOLD:-}"
ASPSP_NAME=""
ASPSP_COUNTRY="BG"

if [[ $# -eq 0 ]]; then
	usage
	exit 2
fi

COMMAND="$1"
shift
case "$COMMAND" in
	aspsps | connect | status) ;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		printf '[bank-smoke] ERROR: unknown command: %s (see --help)\n' "$COMMAND" >&2
		exit 2
		;;
esac

while [[ $# -gt 0 ]]; do
	case "$1" in
		--api | --email | --household | --aspsp-name | --aspsp-country)
			if [[ $# -lt 2 || -z "$2" ]]; then
				printf '[bank-smoke] ERROR: %s requires a value\n' "$1" >&2
				exit 2
			fi
			case "$1" in
				--api) BANK_SMOKE_API="$2" ;;
				--email) BANK_SMOKE_EMAIL="$2" ;;
				--household) BANK_SMOKE_HOUSEHOLD="$2" ;;
				--aspsp-name) ASPSP_NAME="$2" ;;
				--aspsp-country) ASPSP_COUNTRY="$2" ;;
			esac
			shift 2
			;;
		-h | --help)
			usage
			exit 0
			;;
		*)
			printf '[bank-smoke] ERROR: unknown option: %s (see --help)\n' "$1" >&2
			exit 2
			;;
	esac
done

if ! command -v curl >/dev/null 2>&1 || ! command -v python3 >/dev/null 2>&1; then
	error 'curl and python3 are required'
fi
if [[ -z "$BANK_SMOKE_EMAIL" || -z "$BANK_SMOKE_PASSWORD" ]]; then
	error 'set BANK_SMOKE_EMAIL and BANK_SMOKE_PASSWORD (or pass --email; password is supplied through BANK_SMOKE_PASSWORD)'
fi
if [[ -z "$BANK_SMOKE_HOUSEHOLD" ]]; then
	error 'set BANK_SMOKE_HOUSEHOLD or pass --household'
fi
if [[ "$COMMAND" == connect && -z "$ASPSP_NAME" ]]; then
	error 'connect requires --aspsp-name with the exact name from aspsps'
fi

BANK_SMOKE_API="${BANK_SMOKE_API%/}"
HTTP_STATUS=""
HTTP_BODY=""
TOKEN=""

api_request() {
	local method="$1" path="$2" payload="${3:-}" auth="${4:-0}"
	local -a args response
	args=(--silent --show-error --connect-timeout 5 --max-time 30 -X "$method" -H 'Accept: application/json')
	if [[ -n "$payload" ]]; then
		args+=(-H 'Content-Type: application/json' --data-binary "$payload")
	fi
	if [[ "$auth" == 1 ]]; then
		args+=(-H "Authorization: Bearer $TOKEN")
	fi
	if ! response="$(curl "${args[@]}" -w $'\n%{http_code}' "$BANK_SMOKE_API$path" 2>/dev/null)"; then
		error "API is unreachable at $BANK_SMOKE_API"
	fi
	HTTP_STATUS="${response##*$'\n'}"
	HTTP_BODY="${response%$'\n'*}"
}

json_value() {
	local path="$1"
	printf '%s' "$HTTP_BODY" | python3 -c '
import json, sys
value = json.load(sys.stdin)
for part in sys.argv[1].split("."):
    value = value[int(part)] if isinstance(value, list) else value[part]
if value is None:
    print("")
elif isinstance(value, (dict, list)):
    print(json.dumps(value, separators=(",", ":")))
else:
    print(value)
' "$path" 2>/dev/null
}

error_code() {
	json_value error.code 2>/dev/null || true
}

check_status() {
	local expected="$1" context="$2" code
	if [[ "$HTTP_STATUS" == "$expected" ]]; then
		return
	fi
	case "$HTTP_STATUS" in
		000) error "API is unreachable at $BANK_SMOKE_API" ;;
		401)
			if [[ "$context" == login ]]; then
				error 'login rejected (HTTP 401); check BANK_SMOKE_EMAIL and BANK_SMOKE_PASSWORD'
			fi
			error 'API rejected the login session (HTTP 401); log in again and check the credentials'
			;;
		404) error 'household not found or not accessible (HTTP 404); check BANK_SMOKE_HOUSEHOLD' ;;
		*)
			code="$(error_code)"
			if [[ "$context" == connect && "$code" == INVALID_VALUE ]]; then
				error "unknown ASPSP: $ASPSP_NAME ($ASPSP_COUNTRY; INVALID_VALUE); choose an exact name from aspsps"
			fi
			if [[ -n "$code" ]]; then
				error "$context failed (HTTP $HTTP_STATUS, $code)"
			fi
			error "$context failed (HTTP $HTTP_STATUS)"
			;;
	esac
}

login() {
	local payload
	payload="$(BANK_SMOKE_EMAIL="$BANK_SMOKE_EMAIL" BANK_SMOKE_PASSWORD="$BANK_SMOKE_PASSWORD" python3 -c 'import json, os; print(json.dumps({"email": os.environ["BANK_SMOKE_EMAIL"], "password": os.environ["BANK_SMOKE_PASSWORD"]}))')"
	api_request POST /auth/login "$payload"
	check_status 200 login
	if ! TOKEN="$(json_value token)" || [[ -z "$TOKEN" ]]; then
		error 'login response did not contain a valid session token'
	fi
}

login
HOUSEHOLD_PATH="/households/$BANK_SMOKE_HOUSEHOLD/bank"

case "$COMMAND" in
	aspsps)
		api_request GET "$HOUSEHOLD_PATH/aspsps" '' 1
		check_status 200 'list ASPSPs'
		if ! printf '%s' "$HTTP_BODY" | python3 -c '
import json, sys
for item in json.load(sys.stdin)["data"]["aspsps"]:
    print("{} ({})".format(item["name"], item["country"]))
'; then
			error 'API returned an invalid ASPSP list'
		fi
		;;
	connect)
		payload="$(ASPSP_NAME="$ASPSP_NAME" ASPSP_COUNTRY="$ASPSP_COUNTRY" python3 -c 'import json, os; print(json.dumps({"aspsp_name": os.environ["ASPSP_NAME"], "aspsp_country": os.environ["ASPSP_COUNTRY"]}))')"
		api_request POST "$HOUSEHOLD_PATH/connections" "$payload" 1
		check_status 201 connect
		if ! CONNECTION_ID="$(json_value data.connection.id)" || [[ -z "$CONNECTION_ID" ]] || ! REDIRECT_URL="$(json_value data.redirect_url)" || [[ -z "$REDIRECT_URL" ]]; then
			error 'API returned an invalid connection response'
		fi
		printf 'Connection ID: %s\nRedirect URL: %s\n' "$CONNECTION_ID" "$REDIRECT_URL"
		printf 'Open the redirect URL in a browser to complete bank SCA.\n'
		;;
	status)
		api_request GET "$HOUSEHOLD_PATH/connections" '' 1
		check_status 200 'list connections'
		if ! printf '%s' "$HTTP_BODY" | python3 -c '
import json, sys
items = json.load(sys.stdin)["items"]
if not items:
    print("No bank connections found.")
for item in items:
    print("ID: {id}\nBank: {bank} ({country})\nStatus: {status}\nSession expiry: {expires}\nFailure: {failure}".format(
        id=item.get("id", ""), bank=item.get("aspsp_name", ""), country=item.get("aspsp_country", ""),
        status=item.get("status", ""), expires=item.get("session_expires_at") or "none",
        failure=item.get("failure_summary") or "none"))
'; then
			error 'API returned an invalid connections list'
		fi
		;;
esac
