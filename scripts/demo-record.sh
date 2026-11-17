#!/usr/bin/env bash
# Re-record demo.gif without leaking anything real.
#
# 1. Loads the demo account from $BBKT_DEMO_ENV (default ~/.config/bbkt-demo/env),
#    which must export BITBUCKET_USERNAME and BITBUCKET_API_TOKEN for the
#    throwaway demo account (a read-only scoped token is enough).
# 2. Runs every command the tape types, with HOME pointed at an empty dir, and
#    refuses to record if the output contains an email address, a token-shaped
#    string, or any term listed in $BBKT_DEMO_BLOCKLIST (comma-separated, e.g.
#    company or client names).
# 3. Runs vhs.
set -euo pipefail

cd "$(dirname "$0")/.."
env_file="${BBKT_DEMO_ENV:-$HOME/.config/bbkt-demo/env}"
[ -f "$env_file" ] || { echo "missing demo env file: $env_file" >&2; exit 1; }
tape="${BBKT_DEMO_TAPE:-demo.tape}"

# Commands the tape types, one per line, read straight from the tape.
cmds=()
while IFS= read -r line; do cmds+=("$line"); done < <(sed -n 's/^Type "\(bbkt .*\)"$/\1/p' "$tape")
[ "${#cmds[@]}" -gt 0 ] || { echo "no commands found in demo.tape" >&2; exit 1; }

out="$(mktemp)"; tmp_home="$(mktemp -d)"
trap 'rm -rf "$out" "$tmp_home"' EXIT
(
  # shellcheck disable=SC1090
  source "$env_file"
  export HOME="$tmp_home"
  unset BBKT_PROFILE BBKT_WORKSPACE BBKT_REPO
  cd "$HOME"
  for c in "${cmds[@]}"; do
    echo "\$ $c"
    eval "$c" || { echo "command failed: $c" >&2; exit 1; }
  done
) >"$out" 2>&1 || { cat "$out" >&2; exit 1; }

fail=0
check() { # label, extended regex
  if grep -n -i -E "$2" "$out" >/dev/null; then
    echo "preflight: found $1:" >&2
    grep -n -i -E "$2" "$out" | sed 's/^/  /' >&2
    fail=1
  fi
}
check "an email address" '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}'
check "a token-shaped string" 'ATAT[A-Za-z0-9_=-]{10,}|ATCT[A-Za-z0-9_=-]{10,}|[A-Za-z0-9_-]{40,}'
IFS=',' read -r -a block <<<"${BBKT_DEMO_BLOCKLIST:-}"
for term in ${block[@]+"${block[@]}"}; do
  [ -n "$term" ] && check "blocklisted term '$term'" "$term"
done
if [ "$fail" -ne 0 ]; then
  echo "preflight failed: not recording." >&2
  exit 1
fi

echo "preflight clean (${#cmds[@]} commands)"
[ -z "${BBKT_DEMO_PREFLIGHT_ONLY:-}" ] || exit 0
command -v vhs >/dev/null || { echo "vhs not installed (brew install vhs)" >&2; exit 1; }
echo "recording..."
vhs "$tape"
echo "wrote demo.gif. Review it before committing."
