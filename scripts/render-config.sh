#!/bin/sh
# Renders bifrost/config.template.json into bifrost/config.json using values from .env.
#
# This exists so nothing tenant-specific is ever committed: the template holds
# placeholders, .env holds the real values and is gitignored, and the rendered output is
# gitignored too because it contains the agent's private key.
#
# No container and no third-party tooling. envsubst is used when present, otherwise a
# sed fallback covers the same variables, because envsubst is not installed by default
# on macOS and requiring it would make this repo harder to run than it needs to be.

set -eu

cd "$(dirname "$0")/.."

TEMPLATE=bifrost/config.template.json
OUT=bifrost/config.json

[ -f "$TEMPLATE" ] || { echo "missing $TEMPLATE" >&2; exit 1; }

if [ ! -f .env ]; then
	echo "no .env found." >&2
	echo "  cp .env.example .env    then fill it in from your tenant (see docs/RUNBOOK.md)" >&2
	exit 1
fi

# Must match private_key_jwk_file in the template. Checking a different file than the
# one the config names is worse than not checking at all: it passes, and the plugin then
# fails at Init on a path this script just told you was fine.
if [ ! -f secrets/sentinel-intake-key.jwk ]; then
	echo "missing secrets/sentinel-intake-key.jwk" >&2
	echo "  Save the agent's PRIVATE key JWK there. Okta shows it exactly once, when the" >&2
	echo "  key pair is generated; if you missed it, generate a new key pair." >&2
	exit 1
fi

# Load .env by parsing it, not by sourcing it. Sourcing breaks on any unquoted value
# containing a space, which docker's own --env-file accepts happily, so the file can be
# known-good everywhere else and still fail only here. See scripts/load-env.sh.
# shellcheck disable=SC1091
. "$(dirname "$0")/load-env.sh"

# The private key is deliberately absent from this list. It is read by the plugin from
# /secrets/agent-key.jwk, so it never passes through this script, never lands in
# config.json, and never has to survive shell or JSON escaping.
#
# That last point is not theoretical: a JWK pasted unquoted into .env gets its quotes
# stripped and its commas treated as brace expansion the moment the file is sourced,
# producing a mangled key and a JSON parse error some distance from the cause.
VARS='OKTA_DOMAIN OKTA_AGENT_ID OKTA_AGENT_RESOURCE_URL
OKTA_READ_LANE_AS_ID OKTA_COMMAND_LANE_AS_ID
FLEETOPS_READ_RESOURCE_URL FLEETOPS_COMMAND_RESOURCE_URL
PLUGIN_ARCH'

# PLUGIN_ARCH must match the architecture of the Bifrost binary, not just the host. A
# .so of the wrong architecture is refused outright, and the message does not say so
# plainly. Default to the host's own arch, which is right for local runs.
if [ -z "${PLUGIN_ARCH:-}" ]; then
	case "$(uname -m)" in
		arm64|aarch64) PLUGIN_ARCH=arm64 ;;
		x86_64|amd64)  PLUGIN_ARCH=amd64 ;;
		*)             PLUGIN_ARCH=amd64 ;;
	esac
	export PLUGIN_ARCH
fi

# Fail on anything unset before writing, so a missing value never reaches Bifrost as a
# literal ${VAR} and resurface as a confusing plugin Init error.
missing=
for v in $VARS; do
	eval "val=\${$v:-}"
	[ -n "$val" ] || missing="$missing $v"
done
if [ -n "$missing" ]; then
	echo "these are unset or empty in .env:" >&2
	for v in $missing; do echo "  $v" >&2; done
	exit 1
fi

if command -v envsubst >/dev/null 2>&1; then
	# Restrict substitution to our own variables, so a $ appearing anywhere in the
	# template is left alone rather than being eaten by an unrelated shell variable.
	list=$(for v in $VARS; do printf '${%s}' "$v"; done)
	envsubst "$list" < "$TEMPLATE" > "$OUT"
else
	cp "$TEMPLATE" "$OUT"
	for v in $VARS; do
		eval "val=\${$v}"
		# Escape the replacement for sed: backslash, the delimiter, and ampersand.
		esc=$(printf '%s' "$val" | sed -e 's/[\\|&]/\\&/g')
		sed -e "s|\${$v}|$esc|g" "$OUT" > "$OUT.tmp" && mv "$OUT.tmp" "$OUT"
	done
fi

# Belt and braces: refuse to leave a half-rendered config on disk.
if grep -q '\${' "$OUT"; then
	echo "ERROR: $OUT still contains unsubstituted variables:" >&2
	grep -o '\${[A-Za-z_][A-Za-z0-9_]*}' "$OUT" | sort -u >&2
	rm -f "$OUT"
	exit 1
fi

echo "rendered $OUT"
