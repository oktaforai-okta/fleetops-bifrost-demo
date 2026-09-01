#!/usr/bin/env bash
# Live feed of the GATEWAY's own decisions, one line per tool call.
#
# Why this exists: the web app shows what happened, and Okta's System Log shows the token
# grants, but neither is Bifrost speaking. This is. The refusal below is the gateway
# recording that it refused, in the plugin's own words, which is the third independent
# account of the same event and the one a sceptical engineer will ask for.
#
# Bifrost's default log is mostly HTTP request-completed noise, so this keeps only the tool
# handler lines and strips the JSON envelope. Run it in a second window before you demo.
#
#   scripts/watch-gateway.sh            # follow live
#   scripts/watch-gateway.sh 40         # replay the last 40 decisions, then follow
#
# What you are looking for, on a refusal:
#
#   tool handler error tool="fleetops_command-dispatch_vehicle"
#     error=okta denied "..." id-jag exchange: invalid_scope: ... [task.dispatch]
#
# "okta denied" is the plugin's PreMCPHook, the per-call re-ask. That wording means the
# check ran on THIS call, not once when the connection was made. If you instead see
# "okta refused to issue a token for", that is the connect-time path, which is also
# correct but is the weaker claim.
set -euo pipefail

TAIL="${1:-20}"
CONTAINER="${BIFROST_CONTAINER:-}"

# Find the Bifrost container without assuming the compose project name, which changes with
# the directory name and is a common reason a copied command does not work.
#
# Matching a bare "bifrost" is WRONG here and cost a wrong answer once: this project is
# itself called fleetops-bifrost-demo, so every container in it has "bifrost" in its name
# and the fleetops container matched first, producing an empty feed that looked like the
# gateway had logged nothing. Match the compose SERVICE segment instead, which is the
# trailing "-bifrost-<n>".
if [ -z "$CONTAINER" ]; then
	CONTAINER=$(docker ps --format '{{.Names}}' | grep -E '(^|-)bifrost(-[0-9]+)?$' | grep -v 'admin' | head -1 || true)
fi

if [ -z "$CONTAINER" ]; then
	echo "no running bifrost container found." >&2
	echo "running containers:" >&2
	docker ps --format '  {{.Names}}' >&2
	echo "set BIFROST_CONTAINER=<name> to override." >&2
	exit 1
fi

echo "watching $CONTAINER, tool decisions only. ctrl-c to stop."
echo

# --line-buffered matters: without it grep holds output in a block buffer and the feed
# arrives in lumps well after the click that caused it, which is useless when presenting.
docker logs -f --tail "$TAIL" "$CONTAINER" 2>&1 |
	grep --line-buffered 'tool handler' |
	sed -u -E \
		-e 's/^\{"level":"[^"]*","time":"([^"]*)","message":"/\1  /' \
		-e 's/"\}$//' \
		-e 's/\\"/"/g' \
		-e 's/\[mcp-server\] //'
