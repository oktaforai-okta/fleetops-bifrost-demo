#!/bin/sh
# Prints one caller token (T1) on stdout, and nothing else, so it can be substituted
# directly into a curl call:
#
#   TOKEN=$(./scripts/get-caller-token.sh)
#   curl -H "Authorization: Bearer $TOKEN" ...
#
# T1 is the token the Watch Service mints FOR ITSELF via client_credentials at the
# agent's own authorization server, with resource set to the agent's resource URL. That
# resource parameter is the point: it makes the grant specific to invoking this one agent
# rather than being ambient authority the service can spend anywhere.
#
# This is step 1 of three, and the only step the plugin never performs. Registered agents
# are not permitted the client_credentials grant at all, which is exactly why a separate
# service client has to start the chain. Steps 2 and 3, the ID-JAG exchange and its
# redemption, are the plugin's job and happen inside Bifrost.
#
# Everything is read from .env. Nothing is echoed but the token itself, because this
# output is routinely pasted into terminals and shared in demos.

set -eu

cd "$(dirname "$0")/.."

# Parsed rather than sourced. See scripts/load-env.sh for why that distinction matters.
# shellcheck disable=SC1091
. "$(dirname "$0")/load-env.sh"

missing=
for v in OKTA_DOMAIN OKTA_AGENT_OWN_AS_ID OKTA_AGENT_RESOURCE_URL \
         OKTA_SERVICE_CLIENT_ID OKTA_SERVICE_CLIENT_SECRET; do
	eval "val=\${$v:-}"
	[ -n "$val" ] || missing="$missing $v"
done
if [ -n "$missing" ]; then
	echo "these are unset or empty in .env:" >&2
	for v in $missing; do echo "  $v" >&2; done
	exit 1
fi

SCOPE=${OKTA_SERVICE_CLIENT_SCOPE:-agent.invoke}
ENDPOINT="https://${OKTA_DOMAIN}/oauth2/${OKTA_AGENT_OWN_AS_ID}/v1/token"

# --data-urlencode rather than -d, because a client secret is arbitrary bytes and a
# literal + or & in one silently truncates the form otherwise.
response=$(curl -sS --fail-with-body -X POST "$ENDPOINT" \
	-H 'Accept: application/json' \
	--data-urlencode 'grant_type=client_credentials' \
	--data-urlencode "scope=${SCOPE}" \
	--data-urlencode "resource=${OKTA_AGENT_RESOURCE_URL}" \
	--data-urlencode "client_id=${OKTA_SERVICE_CLIENT_ID}" \
	--data-urlencode "client_secret=${OKTA_SERVICE_CLIENT_SECRET}" 2>&1) || {
	echo "token request to $ENDPOINT failed:" >&2
	echo "$response" >&2
	exit 1
}

# Extracted without jq so this has no dependencies beyond curl.
token=$(printf '%s' "$response" \
	| sed -n 's/.*"access_token"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')

if [ -z "$token" ]; then
	echo "no access_token in the response from $ENDPOINT:" >&2
	echo "$response" >&2
	exit 1
fi

printf '%s\n' "$token"
