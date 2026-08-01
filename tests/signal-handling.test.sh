#!/usr/bin/env bash
#
# End-to-end signal-handling tests -- the property no in-process test can
# prove, because it depends on what a real *child* process observes when the
# launcher (a separate OS process) receives a real signal while a real child
# (the stub container run) is in the foreground.
#
# The regression this guards: in Go, calling signal.Ignore instead of
# signal.Notify would give the launcher's own process an ignored disposition
# for INT/TERM/HUP -- and an *ignored* (SIG_IGN) disposition survives execve
# and is inherited by whatever the launcher execs next, including the
# container runtime child. That child would then not die on the signal at
# all: closing the terminal would kill neither process, the launcher's wait
# would never return, and the worktree locks it holds would leak permanently.
# A *caught* disposition (signal.Notify) is reset to the default on exec, so
# the child dies normally -- which is what "well under the stub sleep" below
# is checking for.
#
# The bash launchers' own signal discipline was never at risk here (bash's
# traps defer to `exit N` and never give the shell's own children an ignored
# disposition); this suite proved Go's signal.Notify reproduces the same
# observable behavior before the bash launchers were retired, and now runs
# against the Go binary exclusively.
#
# Usage: tests/signal-handling.test.sh
#        CLAUDE_CONTAINED_TEST_TARGETS="bin/claude-contained bin/claude-contained-docked" tests/signal-handling.test.sh
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(dirname "$here")"
unset CLAUDE_CONTAINED_LOG_LEVEL

fails=0
_check() { # _check "description" <rc-that-should-be-0>
  if [[ "$2" -eq 0 ]]; then
    echo "  PASS: $1"
  else
    echo "  FAIL: $1"
    fails=$((fails + 1))
  fi
}

resolve_dir() { (cd "$1" && pwd -P); }

# Long enough that "the launcher hung / gave its child an ignored
# disposition" (near-full STUB_SLEEP elapsed) is trivially distinguishable
# from "the child died normally" (near-instant), even under CI scheduling
# jitter.
STUB_SLEEP="${STUB_SLEEP:-3}"

# setup_runtime_stubs <marker-file>; sets stub_dir. The stub "container run"
# writes the marker the moment it starts, then sleeps for STUB_SLEEP seconds.
setup_runtime_stubs() {
  local marker="$1"
  stub_dir="$(mktemp -d)"
  cat > "${stub_dir}/container" <<EOF
#!/usr/bin/env bash
set -uo pipefail
case "\${1:-}" in
  system) exit 0 ;;
  list) exit 0 ;;
esac
if [[ "\${1:-}" == "run" ]]; then
  : > "${marker}"
  sleep "${STUB_SLEEP}"
fi
exit 0
EOF
  cat > "${stub_dir}/docker" <<EOF
#!/usr/bin/env bash
set -uo pipefail
case "\${1:-}" in
  info) exit 0 ;;
  ps)
    [[ "\${STUB_ATTACH:-}" == 1 ]] && echo aic-live
    exit 0
    ;;
  inspect)
    if [[ "\${STUB_ZELLIJ:-}" == 1 ]]; then
      printf '%s\n' 'CLAUDE_CONTAINED_ZELLIJ=1' 'CLAUDE_CONTAINED_ZELLIJ_SESSION=alpha'
    fi
    exit 0
    ;;
esac
if [[ "\${1:-}" == "run" || ( "\${1:-}" == "exec" && "\${STUB_ATTACH:-}" == 1 ) ]]; then
  : > "${marker}"
  sleep "${STUB_SLEEP}"
fi
exit 0
EOF
  chmod +x "${stub_dir}/container" "${stub_dir}/docker"
}

worktree_is_locked() { # worktree_is_locked <main-repo> <wt-path>
  git -C "$1" worktree list --porcelain 2>/dev/null \
    | awk -v p="$2" '
        $1=="worktree"{cur=$2}
        $1=="locked" && cur==p {found=1}
        END{exit found?0:1}'
}

make_repo_with_worktree() { # echoes "<main> <wt>"
  local root="$1" name="$2" main wt
  main="${root}/main"
  wt="${root}/${name}"
  mkdir -p "$main"
  git init -q "$main"
  git -C "$main" -c user.email=t@example.com -c user.name=test commit -q --allow-empty -m init
  git -C "$main" worktree add -q --detach "$wt" >/dev/null 2>&1
  main="$(resolve_dir "$main")"
  wt="$(resolve_dir "$wt")"
  echo "$main $wt"
}

wait_for_file() { # wait_for_file <path> <timeout-secs>
  local path="$1" timeout="$2" waited=0
  while [[ ! -e "$path" ]]; do
    sleep 0.1
    waited=$((waited + 1))
    [[ $waited -ge $((timeout * 10)) ]] && return 1
  done
  return 0
}

# start_launcher <target> <main> <marker>; backgrounds the launcher inside a
# job-control-enabled subshell (set -m), so it gets its own process group
# whose pgid equals its own PID -- letting the caller choose between signaling
# just the launcher or the whole group (launcher + the stub child it execs).
# Writes the launcher's PID to launcher_pid_file and its eventual exit status
# to launcher_rc_file; the caller waits on harness_pid.
start_launcher() {
  local target="$1" main="$2" marker="$3"
  launcher_pid_file="$(mktemp)"; rm -f "$launcher_pid_file"
  launcher_rc_file="$(mktemp)"; rm -f "$launcher_rc_file"

  (
    set -m
    env HOME="$home" PATH="${stub_dir}:$PATH" \
      "${repo_root}/${target}" -N -s -C "$main" -W \
      </dev/null >/dev/null 2>/dev/null &
    lp=$!
    printf '%s' "$lp" > "$launcher_pid_file"
    wait "$lp"
    echo $? > "$launcher_rc_file"
  ) &
  harness_pid=$!

  wait_for_file "$launcher_pid_file" 5
  launcher_pid="$(cat "$launcher_pid_file")"
}

start_log_only_attach() {
  local target="$1" marker="$2" kind="${3:-plain}"
  launcher_pid_file="$(mktemp)"; rm -f "$launcher_pid_file"
  launcher_rc_file="$(mktemp)"; rm -f "$launcher_rc_file"

  (
    set -m
    if [[ "$kind" == zellij ]]; then
      env HOME="$home" PATH="${stub_dir}:$PATH" STUB_ATTACH=1 STUB_ZELLIJ=1 \
        CLAUDE_CONTAINED_LOG_LEVEL='' \
        "${repo_root}/${target}" --container-runtime=docker \
        --log-only --log-level=off --zellij --attach --session alpha \
        </dev/null >/dev/null 2>/dev/null &
    else
      env HOME="$home" PATH="${stub_dir}:$PATH" STUB_ATTACH=1 \
        CLAUDE_CONTAINED_LOG_LEVEL='' \
        "${repo_root}/${target}" --container-runtime=docker \
        --log-only --log-level=off --attach live \
        </dev/null >/dev/null 2>/dev/null &
    fi
    lp=$!
    printf '%s' "$lp" > "$launcher_pid_file"
    wait "$lp"
    echo $? > "$launcher_rc_file"
  ) &
  harness_pid=$!

  wait_for_file "$launcher_pid_file" 5
  launcher_pid="$(cat "$launcher_pid_file")"
  wait_for_file "$marker" 5
}

read -ra targets <<< "${CLAUDE_CONTAINED_TEST_TARGETS:-bin/claude-contained bin/claude-contained-docked}"

for target in "${targets[@]}"; do
  echo "== ${target} =="

  for spec in "INT:130" "TERM:143" "HUP:129"; do
    sig="${spec%%:*}"
    want_code="${spec##*:}"

    # --- Whole process group: the container child must not hang. A launcher
    #     that handed its child an ignored disposition would only exit after
    #     the full STUB_SLEEP; one that didn't exits near-instantly, because
    #     the child dies of the signal's own default disposition. ---
    home="$(mktemp -d)"; root="$(mktemp -d)"
    read -r main wt <<< "$(make_repo_with_worktree "$root" hidden-wt)"
    marker="$(mktemp -u)"
    setup_runtime_stubs "$marker"

    start_launcher "$target" "$main" "$marker"
    wait_for_file "$marker" 5
    _check "${target} ${sig} (group): stub container run started" $?

    worktree_is_locked "$main" "$wt"
    _check "${target} ${sig} (group): worktree is locked while the container runs" $?

    start=$SECONDS
    kill "-${sig}" "-${launcher_pid}" 2>/dev/null
    wait "$harness_pid" 2>/dev/null
    elapsed=$((SECONDS - start))

    rc="$(cat "$launcher_rc_file" 2>/dev/null || echo -1)"
    if [[ "$rc" == "$want_code" ]]; then check_rc=0; else check_rc=1; fi
    _check "${target} ${sig} (group): exit status is ${want_code} (got ${rc})" "$check_rc"
    if [[ $elapsed -lt $((STUB_SLEEP - 1)) ]]; then check_rc=0; else check_rc=1; fi
    _check "${target} ${sig} (group): child died promptly, not after the full sleep (${elapsed}s)" "$check_rc"
    worktree_is_locked "$main" "$wt"; rc2=$?
    if [[ $rc2 -ne 0 ]]; then check_rc=0; else check_rc=1; fi
    _check "${target} ${sig} (group): worktree is unlocked once the launcher exits" "$check_rc"

    rm -rf "$stub_dir" "$home" "$root" "$launcher_pid_file" "$launcher_rc_file"
    rm -f "$marker"

    # --- Launcher only (not the group): the run must complete naturally
    #     (after the stub's full sleep) before the launcher exits with the
    #     signal's status -- proving the signal is deferred, not acted on
    #     immediately, matching bash's "trapped signal waits for the
    #     foreground command to return". ---
    home="$(mktemp -d)"; root="$(mktemp -d)"
    read -r main wt <<< "$(make_repo_with_worktree "$root" hidden-wt)"
    marker="$(mktemp -u)"
    setup_runtime_stubs "$marker"

    start_launcher "$target" "$main" "$marker"
    wait_for_file "$marker" 5
    _check "${target} ${sig} (solo): stub container run started" $?

    start=$SECONDS
    kill "-${sig}" "${launcher_pid}" 2>/dev/null
    wait "$harness_pid" 2>/dev/null
    elapsed=$((SECONDS - start))

    rc="$(cat "$launcher_rc_file" 2>/dev/null || echo -1)"
    if [[ "$rc" == "$want_code" ]]; then check_rc=0; else check_rc=1; fi
    _check "${target} ${sig} (solo): exit status is ${want_code} (got ${rc})" "$check_rc"
    if [[ $elapsed -ge $((STUB_SLEEP - 1)) ]]; then check_rc=0; else check_rc=1; fi
    _check "${target} ${sig} (solo): the run completed naturally before the launcher exited (${elapsed}s)" "$check_rc"
    worktree_is_locked "$main" "$wt"; rc2=$?
    if [[ $rc2 -ne 0 ]]; then check_rc=0; else check_rc=1; fi
    _check "${target} ${sig} (solo): worktree is unlocked once the launcher exits" "$check_rc"

    rm -rf "$stub_dir" "$home" "$root" "$launcher_pid_file" "$launcher_rc_file"
    rm -f "$marker"
  done

  # --log-only keeps the launcher alive to proxy attach output. Its signal
  # behavior must therefore match the ordinary foreground run rather than
  # orphaning the attached runtime child.
  for delivery in group solo; do
    home="$(mktemp -d)"; root="$(mktemp -d)"
    marker="$(mktemp -u)"
    setup_runtime_stubs "$marker"
    start_log_only_attach "$target" "$marker"
    _check "${target} TERM (${delivery}, log-only attach): stub exec started" $?

    start=$SECONDS
    if [[ "$delivery" == group ]]; then
      kill -TERM "-${launcher_pid}" 2>/dev/null
    else
      kill -TERM "${launcher_pid}" 2>/dev/null
    fi
    wait "$harness_pid" 2>/dev/null
    elapsed=$((SECONDS - start))

    rc="$(cat "$launcher_rc_file" 2>/dev/null || echo -1)"
    if [[ "$rc" == 143 ]]; then check_rc=0; else check_rc=1; fi
    _check "${target} TERM (${delivery}, log-only attach): exit status is 143 (got ${rc})" "$check_rc"
    if [[ "$delivery" == group ]]; then
      if [[ $elapsed -lt $((STUB_SLEEP - 1)) ]]; then check_rc=0; else check_rc=1; fi
      _check "${target} TERM (group, log-only attach): child died promptly (${elapsed}s)" "$check_rc"
    else
      if [[ $elapsed -ge $((STUB_SLEEP - 1)) ]]; then check_rc=0; else check_rc=1; fi
      _check "${target} TERM (solo, log-only attach): child completed before exit (${elapsed}s)" "$check_rc"
    fi

    rm -rf "$stub_dir" "$home" "$root" "$launcher_pid_file" "$launcher_rc_file"
    rm -f "$marker"
  done

  home="$(mktemp -d)"; root="$(mktemp -d)"
  marker="$(mktemp -u)"
  setup_runtime_stubs "$marker"
  start_log_only_attach "$target" "$marker" zellij
  _check "${target} TERM (solo, log-only Zellij attach): stub exec started" $?
  start=$SECONDS
  kill -TERM "${launcher_pid}" 2>/dev/null
  wait "$harness_pid" 2>/dev/null
  elapsed=$((SECONDS - start))
  rc="$(cat "$launcher_rc_file" 2>/dev/null || echo -1)"
  [[ "$rc" == 143 ]]
  _check "${target} TERM (solo, log-only Zellij attach): exit status is 143 (got ${rc})" $?
  [[ $elapsed -ge $((STUB_SLEEP - 1)) ]]
  _check "${target} TERM (solo, log-only Zellij attach): child completed before exit (${elapsed}s)" $?
  rm -rf "$stub_dir" "$home" "$root" "$launcher_pid_file" "$launcher_rc_file"
  rm -f "$marker"

done

echo
if [[ "$fails" -gt 0 ]]; then
  echo "${fails} signal-handling test(s) failed."
  exit 1
fi
echo "All signal-handling tests passed."
