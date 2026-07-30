#!/usr/bin/env bash
#
# Tests for -e/--env and the per-project .claude-contained/env file.
#
# The contract lives at the launcher boundary: whatever ends up as `-e KEY=VALUE`
# on the runtime command line is what the tool process sees, since the entrypoint
# preserves the inherited environment through gosu and srt. So these tests stub
# the runtime and assert on the emitted argv.
#
# Two properties matter beyond plumbing. Keys that would subvert the container's
# own guarantees (STAY_ROOT, SSH_AUTH_SOCK, the HOST_*/SRT_*/CLAUDE_CONTAINED_*
# namespaces) must be refused from any source, and the loader/interpreter vars
# must additionally be refused from the project file, which the contained agent
# can write. The file must never be evaluated, only parsed.
#
# Usage: tests/env-flags.test.sh
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(dirname "$here")"

stub_dir="$(mktemp -d)"
proj="$(mktemp -d)"
home="$(mktemp -d)"
canary="$(mktemp -d)/canary"

# Stub runtime: prints one argv element per line, and reports a running
# container so the attach paths can be exercised.
cat > "${stub_dir}/container" <<'EOF'
#!/usr/bin/env bash
set -uo pipefail
[[ "${1:-}" == "system" ]] && exit 0
if [[ "${1:-}" == "list" ]]; then
  echo "aic-live"
  exit 0
fi
[[ "${1:-}" == "inspect" ]] && exit 0
printf '%s\n' "$@"
EOF

cat > "${stub_dir}/docker" <<'EOF'
#!/usr/bin/env bash
set -uo pipefail
[[ "${1:-}" == "info" ]] && exit 0
if [[ "${1:-}" == "ps" ]]; then
  echo "aic-live"
  exit 0
fi
[[ "${1:-}" == "inspect" ]] && exit 0
printf '%s\n' "$@"
EOF

chmod +x "${stub_dir}/container" "${stub_dir}/docker"

launcher_run() { # launcher_run <target> <stdout-file> <stderr-file> <flags...>
  local target="$1" out_file="$2" err_file="$3"
  shift 3

  HOME="$home" PATH="${stub_dir}:$PATH" "${repo_root}/${target}" "$@" >"$out_file" 2>"$err_file"
}

line_has() { # line_has <file> <exact-line>
  grep -Fqx -- "$2" "$1"
}

file_has() { # file_has <file> <fixed-string>
  grep -Fq -- "$2" "$1"
}

line_count() { # line_count <file> <exact-line>
  grep -Fxc -- "$2" "$1" 2>/dev/null || true
}

write_project_env() {
  mkdir -p "${proj}/.claude-contained"
  printf '%s' "$1" > "${proj}/.claude-contained/env"
}

clear_project_env() {
  rm -f "${proj}/.claude-contained/env"
}

suite() {
  set +e
  local target="$1"
  local fails=0
  local out err rc

  _check() { # _check "description" <rc-that-should-be-0>
    if [[ "$2" -eq 0 ]]; then
      echo "  PASS: $1"
    else
      echo "  FAIL: $1"
      fails=$((fails + 1))
    fi
  }

  out="$(mktemp)"
  err="$(mktemp)"
  clear_project_env

  launcher_run "$target" "$out" "$err" -e FOO=bar --env BAZ=qux --env=QUUX=1 -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "FOO=bar" && line_has "$out" "BAZ=qux" && line_has "$out" "QUUX=1"
  _check "-e, --env, and --env= all reach the runtime" $?

  launcher_run "$target" "$out" "$err" -e 'GREETING=hello world' -e 'CONN=k=v;x=y' -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "GREETING=hello world" && line_has "$out" "CONN=k=v;x=y"
  _check "values keep spaces and embedded '='" $?

  launcher_run "$target" "$out" "$err" -e DISPLAY=host.local:0 -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "DISPLAY=host.local:0"
  _check "DISPLAY is passable (the entrypoint honors a pre-set value)" $?

  launcher_run "$target" "$out" "$err" -e FOO=first -e FOO=second -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "FOO=second" && [[ "$(line_count "$out" "FOO=first")" -eq 0 ]] \
    && [[ "$(line_count "$out" "FOO=second")" -eq 1 ]]
  _check "a repeated key is emitted once, last value winning" $?

  launcher_run "$target" "$out" "$err" -e TZ=UTC -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "TZ=UTC" && [[ "$(grep -c '^TZ=' "$out")" -eq 1 ]]
  _check "user TZ replaces the host-timezone built-in instead of duplicating it" $?

  launcher_run "$target" "$out" "$err" -e FOO=bar -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 0 ]] && file_has "$err" "env: FOO (--env)"
  _check "loaded keys are reported by name" $?

  launcher_run "$target" "$out" "$err" -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 0 ]] && ! file_has "$err" "env:"
  _check "no env summary when nothing was passed" $?

  launcher_run "$target" "$out" "$err" -e -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "requires KEY=VALUE"
  _check "bare -e fails with a message, not an unbound-variable crash" $?

  launcher_run "$target" "$out" "$err" -e NOEQUALS -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "expected KEY=VALUE"
  _check "--env without '=' is rejected" $?

  launcher_run "$target" "$out" "$err" -e '9BAD=x' -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "not a valid environment variable name"
  _check "invalid key names are rejected" $?

  for reserved in STAY_ROOT=1 SSH_AUTH_SOCK=/tmp/evil.sock HOME=/tmp PATH=/tmp \
                  JAVA_HOME=/tmp GIT_PROTECT_DIRS=/tmp HOST_HOME=/tmp \
                  SRT_DISABLE=1 CLAUDE_CONTAINED_ZELLIJ=1; do
    launcher_run "$target" "$out" "$err" -e "$reserved" -N -s -C "$proj"
    rc=$?
    [[ $rc -eq 2 ]] && file_has "$err" "reserved by claude-contained"
    _check "-e ${reserved%%=*} is refused" $?
  done

  launcher_run "$target" "$out" "$err" -e NODE_OPTIONS=--require=/tmp/x.js -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "NODE_OPTIONS=--require=/tmp/x.js"
  _check "NODE_OPTIONS is allowed on the command line" $?

  # --- project env file ---

  write_project_env '# a comment
  # an indented comment

BAZ="from file"
QUX=plain
SPACED=a b c
'
  launcher_run "$target" "$out" "$err" -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "BAZ=from file" && line_has "$out" "QUX=plain" \
    && line_has "$out" "SPACED=a b c" && file_has "$err" "(.claude-contained/env)"
  _check "project env file is loaded, quotes stripped, comments skipped" $?

  launcher_run "$target" "$out" "$err" --no-project-env -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 0 ]] && [[ "$(line_count "$out" "QUX=plain")" -eq 0 ]] && ! file_has "$err" "(.claude-contained/env)"
  _check "--no-project-env ignores the file" $?

  launcher_run "$target" "$out" "$err" -e QUX=fromflag -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "QUX=fromflag" && [[ "$(line_count "$out" "QUX=plain")" -eq 0 ]] \
    && [[ "$(grep -c '^QUX=' "$out")" -eq 1 ]]
  _check "--env overrides the file, emitting the key once" $?

  printf 'CRLF=ok\r\n' > "${proj}/.claude-contained/env"
  launcher_run "$target" "$out" "$err" -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "CRLF=ok"
  _check "CRLF line endings do not leak into the value" $?

  printf 'NOEOL=ok' > "${proj}/.claude-contained/env"
  launcher_run "$target" "$out" "$err" -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "NOEOL=ok"
  _check "a final line without a newline is read" $?

  rm -f "$canary"
  write_project_env "EVIL=\$(touch ${canary})
ALSO=\`touch ${canary}\`
"
  launcher_run "$target" "$out" "$err" -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 0 ]] && [[ ! -e "$canary" ]] && line_has "$out" "EVIL=\$(touch ${canary})"
  _check "the file is parsed literally, never evaluated" $?

  write_project_env 'STAY_ROOT=1
'
  launcher_run "$target" "$out" "$err" -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" ".claude-contained/env:1" && file_has "$err" "reserved by claude-contained"
  _check "a reserved key in the file fails the launch, naming the line" $?

  write_project_env 'NODE_OPTIONS=--require=/tmp/x.js
'
  launcher_run "$target" "$out" "$err" -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "cannot be set from a project env file"
  _check "NODE_OPTIONS is refused from the file though allowed as a flag" $?

  write_project_env 'JUSTAKEY
'
  launcher_run "$target" "$out" "$err" -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" ".claude-contained/env:1" && file_has "$err" "expected KEY=VALUE"
  _check "a malformed file line fails the launch" $?

  # A rejected file must not have reached the worktree-lock prompt first.
  ! file_has "$err" "worktree"
  _check "file validation happens before worktree handling" $?

  clear_project_env

  # --- attach ---

  launcher_run "$target" "$out" "$err" -a live -s -e FOO=bar
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "exec" && line_has "$out" "aic-live" && line_has "$out" "FOO=bar"
  _check "--env reaches an attach exec" $?

  launcher_run "$target" "$out" "$err" -a live -s -e 'GREETING=hello world'
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "GREETING=hello world"
  _check "attach env survives a value containing a space" $?

  launcher_run "$target" "$out" "$err" --zellij --attach -e FOO=bar
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "cannot be combined with --zellij --attach"
  _check "--env with --zellij --attach is refused, not silently dropped" $?

  rm -f "$out" "$err"
  return "$fails"
}

total=0
read -ra targets <<< "${CLAUDE_CONTAINED_TEST_TARGETS:-claude-contained claude-docked}"
for target in "${targets[@]}"; do
  echo "== ${target} =="
  suite "$target"
  total=$((total + $?))
done

rm -rf "$stub_dir" "$proj" "$home" "$(dirname "$canary")"

if [[ "$total" -gt 0 ]]; then
  echo
  echo "${total} env test(s) failed."
  exit 1
fi

echo
echo "All env tests passed."
