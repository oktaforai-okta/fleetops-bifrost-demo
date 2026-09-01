# Sourced, not executed. Loads .env into the environment the way docker does, rather
# than the way the shell does.
#
#   . "$(dirname "$0")/load-env.sh"
#
# The obvious implementation is `set -a; . ./.env; set +a`, and it is wrong for this file.
# Sourcing runs .env as a shell script, so an unquoted value containing a space is parsed
# as a variable assignment followed by a COMMAND:
#
#   OKTA_SERVICE_CLIENT_SCOPE=agent.invoke task.read
#
# becomes "run task.read with OKTA_SERVICE_CLIENT_SCOPE set", which fails with
# "task.read: command not found" and points nowhere near the actual problem. The same
# file is accepted without complaint by `docker run --env-file`, which splits on the
# first = and treats the rest as literal bytes, so the file looks fine everywhere else
# and only the shell-sourcing path breaks.
#
# Other things sourcing gets wrong on a working .env: a JWK's commas become brace
# expansion, a $ in a secret is expanded, and a value containing & or ` is worse still.
#
# So this parses instead of sourcing. Values are taken literally, exactly as docker takes
# them, with one concession: a wholly-quoted value has its outer quotes removed, since
# both forms are common in a .env and docker strips them too.

# shellcheck shell=sh

_load_env_file=${1:-.env}

if [ ! -f "$_load_env_file" ]; then
	echo "no $_load_env_file found in $(pwd)" >&2
	return 1 2>/dev/null || exit 1
fi

# The `|| [ -n "$_line" ]` keeps a final line with no trailing newline.
while IFS= read -r _line || [ -n "$_line" ]; do
	# Strip a UTF-8 BOM on the first line, which is invisible in an editor and turns the
	# first key into something that will never match a lookup.
	_line=${_line#\xef\xbb\xbf}

	# Leading whitespace, then blanks and comments.
	while :; do
		case "$_line" in
			' '*|'	'*) _line=${_line#?} ;;
			*) break ;;
		esac
	done
	case "$_line" in
		''|'#'*) continue ;;
	esac

	# An optional `export ` prefix, which is valid in a sourced file and appears in
	# hand-written .env files.
	case "$_line" in
		'export '*) _line=${_line#export } ;;
	esac

	# Anything that is not an assignment is skipped rather than guessed at.
	case "$_line" in
		*=*) ;;
		*) continue ;;
	esac

	_key=${_line%%=*}
	_val=${_line#*=}

	# Trailing whitespace on the key, e.g. `FOO = bar`.
	while :; do
		case "$_key" in
			*' '|*'	') _key=${_key%?} ;;
			*) break ;;
		esac
	done

	# Refuse a key that is not a shell-safe identifier instead of letting export fail
	# with something obscure.
	case "$_key" in
		''|[0-9]*) continue ;;
		*[!A-Za-z0-9_]*) continue ;;
	esac

	# Outer quotes only. An inner quote is part of the value.
	case "$_val" in
		'"'*'"') _val=${_val#\"}; _val=${_val%\"} ;;
		"'"*"'") _val=${_val#\'}; _val=${_val%\'} ;;
	esac

	export "$_key=$_val"
done < "$_load_env_file"

unset _line _key _val _load_env_file
