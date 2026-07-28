# Differential harness

Proves that a launcher invocation behaves identically across two independent
runs by comparing the **full observable result** — runtime arguments,
standard output, standard error, exit status, and a filesystem manifest —
rather than just the command line a diff-only comparison would see. Several
behaviors have no runtime-argument footprint at all (every reserved-key
rejection, session-name validation, tool validation, the `--env` +
`--zellij --attach` refusal, the read-only project-directory error, and the
entire worktree lock/unlock cycle), so a comparison that only looked at the
command line would compare empty-against-empty for all of them and pass even
on total disagreement.

## Usage

```
tests/differential/harness.sh [--target NAME]... [--compare REF:CANDIDATE]...
                              [--case GLOB]...
```

With neither `--target` nor `--compare`, runs the whole corpus against both
`claude-contained` and `claude-docked`. Exits non-zero if anything diverges, or
if a corpus entry turns out to be a no-op (see "The no-observable-result guard"
below).

`--compare REF:CANDIDATE` runs each corpus entry once per side against two
*different* launchers and diffs across them — this is how the Go launcher is
proven equivalent to a bash reference. `--case GLOB` (repeatable) restricts the
corpus by basename, so a caller can assert the subset a given ticket implements
while the rest stays wired up. Both target names resolve relative to the
repository root, so `--compare claude-contained:bin/claude-go` works.

## Why target-vs-itself is still the default

This harness is a prefactor for the Go rewrite (see
`.scratch/go-launcher/issues/`): the Go binary is one side of the comparison
and a bash launcher the other, proving the port is behavior-preserving. That is
what `--compare` does. The *default* mode remains running each corpus entry
**twice against the same target** — two fresh, fully isolated invocations of
`claude-contained`, and separately of `claude-docked` — with the two runs
compared against each other.

This is deliberate, not a placeholder. `claude-contained` and `claude-docked`
have a few small, *intentional* differences (`claude-docked` has no forced
default DNS resolver, and its Docker `run` emits Zellij-tracking labels
`claude-contained.zellij*` that Apple Containers has no equivalent for) that
a direct cross-comparison would report as failures even though nothing is
wrong. Cross-runtime parity for everything that *should* be identical is
already covered by the existing `tests/*.test.sh` suites, which run the same
hand-written assertions against both targets. What this harness proves
instead — and what a fixed-assertion suite structurally can't — is that two
independent runs of one target produce byte-identical *normalized* output.
That's the real substance of the ticket: the isolation is complete (neither
run can observe the other's mutations) and the normalization of
timestamp/PID/path noise is complete (two runs of the identical invocation
never spuriously diverge). The same machinery, driven by
`--compare claude-contained:bin/claude-go`, compares the Go launcher against a
bash reference instead of against itself. Note that two `--target` flags do
*not* cross-compare — each is still run against itself; crossing requires
`--compare`.

## How prompts are handled under non-terminal stdin

Neither launcher checks `[[ -t 0 ]]`/isatty anywhere; every prompt is a bare
`read -p` under `set -euo pipefail`. Under closed or non-terminal stdin,
`read` hits EOF, returns 1, and `set -e` kills the script on that line —
**no prompt text is ever printed, and the process exits 1.** That is "how
prompts behave today" absent a real terminal, and it's the reason this
harness does not need to drive a pty:

- Corpus entries that don't care about a prompt path use `/dev/null` as
  stdin (the default when a case doesn't override `case_stdin`), which
  reproduces exactly that abort-with-no-output behavior for any prompt they
  happen to hit.
- Corpus entries that exist specifically to exercise a prompt (the Zellij
  attach picker, the bare `-a` attach picker, and 52/53's worktree-lock
  offer accepted/declined) set `CASE_STDIN_OUT` to a scripted answer via
  `case_stdin()`. `tests/worktree-offer.test.sh` covers the same prompt as a
  black-box suite outside this harness (it runs against the Go binary too,
  via `CLAUDE_CONTAINED_TEST_TARGETS`) and scripts its answer the same way,
  just piped directly rather than through `case_stdin()`.

This is an explicit choice, not an oversight: driving a real pty would let
the harness observe the *rendered* prompt text, but nothing in this corpus
needs that — only the resulting behavior (which branch was taken) does — and
a pty adds a real hang risk if a corpus entry's flags don't line up with its
scripted answer. `case_stdin()` failing to satisfy a `read -p` the launcher
actually reaches will hang the harness the same way a real terminal session
would; there is no timeout wrapper. Get the flags and the fixture right
instead of relying on one to bail the other out.

## What's isolated, and how

Every corpus entry runs each side against `make_fixture` (`lib/isolate.sh`)
output: a fresh, randomly-rooted `FIXTURE_ROOT` containing `home/`,
`project/`, and `stub/` subdirectories. `FIXTURE_PROJ` and `FIXTURE_HOME`
always use the fixed basenames `project` and `home` — not `mktemp`'s own
random name — because the container name embeds
`sanitize_foldername(project-dir)`, which is a function of the project
directory's *basename*. Two sides with different random basenames would get
different container names by construction, and every axis downstream would
"diverge" on that alone before ever exercising the behavior under test. A
fixed basename removes that source of noise at the source, the same way the
shared project-directory template removes it for file contents.

`stub/container` and `stub/docker` (also `lib/isolate.sh`) intercept the
runtime binary: `system`/`info` always succeed (that prompt isn't part of
this corpus), `list`/`ps` and `inspect` return whatever a corpus case wired
up via `DIFF_LIST_OUTPUT`/`DIFF_INSPECT_DIR` (empty by default — no running
containers), and everything else (the actual `run`/`exec`/`build` the
launcher would otherwise perform) is appended to `DIFF_ARGV_LOG` and
succeeds immediately. That log is what makes "runtime arguments" an
independently diffable axis instead of something scraped out of stdout.

## Normalization

Two things make an otherwise-identical pair of runs compare unequal, and
both are handled once, centrally, rather than per corpus entry:

- **Timestamps and PIDs** (`lib/normalize.sh`, `normalize_text`): the
  container name's `HHMM` minute stamp, the rebuild cache-bust token's
  14-digit UTC timestamp, and the worktree mutex owner file's `PID EPOCH`
  line. This is textual, post-capture normalization rather than freezing
  `date` in the stub `PATH` — the owner file's PID half (`$$`) can't be
  frozen that way regardless, so one mechanism covers all three fields
  instead of half-solving it with a stub and half with regex.
- **Absolute fixture paths** (`lib/normalize.sh`, `neutralize_paths` and
  `neutralize_path_hash`): mount `src`/`dst` arguments and `HOST_HOME=...`
  are the literal fixture paths by the launcher's own path-parity design, so
  two sides never share an absolute path even when otherwise identical.
  `neutralize_paths` collapses each side's own `$HOME`/project path to
  `<HOME>`/`<PROJ>` before comparing. The default Zellij session name also
  embeds an 8-character hash of the *full* project path (not just its
  basename, so the fixed-basename trick above doesn't cover it);
  `neutralize_path_hash` recomputes that exact hash for the side's own path
  and substitutes it too.

Both passes are applied uniformly to stdout, stderr, the runtime-argv dump,
and the filesystem manifest (`harness.sh`'s `prep()`), so a fourth volatile
field found later is one new named substitution in one place, not a hunt
through every comparison site.

## The filesystem manifest

`lib/manifest.sh` walks each fixture's `home/` and `project/` trees after
the run, labels every path relative to a neutral `HOME`/`PROJ` root instead
of the fixture's real absolute path, and inlines normalized file content so
a diverging diff is directly readable instead of just "hash changed". Two
classes of real, expected noise are excluded rather than normalized, because
they're structurally unrelated to anything the launcher does:

- Content under any real `.git/objects/<2-hex>/...` is pruned from the walk
  entirely — content-addressed object names embed the commit's author
  timestamp, so two independent `git commit`s of "the same" fixture setup
  land in different hashes whenever they don't fall in the same
  wall-clock second.
- File *content* elsewhere under a `.git` directory (the index, reflogs,
  packed-refs) is skipped for the same reason, except the two files this
  harness actually cares about: the worktree lock file itself (`locked`)
  and the mid-run snapshot the stub takes of it (below). Presence/type/mode
  is still compared for everything else, just not byte content.

### Proving the worktree lock/unlock cycle

The runtime argv for a run that auto-locks a hidden worktree looks
*identical* to one that doesn't — locking adds no `-e`/`--mount` of its own,
it's a pure filesystem side effect (`DIFF_SNAPSHOT_PATHS` in
`lib/isolate.sh` exists because of this one behavior). And within a single
launcher invocation, the lock is taken before the container "runs" and
released again in the launcher's own `EXIT` trap right after — so by the
time the whole process exits, the net effect on disk is zero even when
locking worked perfectly.

`41-worktree-lock-unlock-cycle.case` proves it happened anyway: it points
`DIFF_SNAPSHOT_PATHS` at the hidden worktree's lock file, and the stub
copies that file to `<path>.mid-run-snapshot` at the exact moment it would
otherwise start a real container — after the lock, before the unlock. That
snapshot lands inside the fixture tree, so the ordinary manifest walk picks
it up with no special casing in the comparison logic itself, only in what
gets set up.

## The no-observable-result guard

Before comparing two sides, the harness checks each side independently: if
stdout, stderr, and the runtime-argv dump are all empty, exit status is 0,
and the post-run manifest is byte-identical to a pre-run baseline, that side
produced literally nothing to distinguish it from doing nothing at all. That
is a harness error, not a pass — it means the corpus entry never reached the
behavior it claims to exercise, and a silently-skipped case must never
masquerade as a passing one just because "empty" trivially equals "empty" on
both sides.

## Adding a corpus entry

Each `corpus/NN-<slug>.case` is a plain shell file the harness `source`s. It
can define:

- `CASE_DESCRIPTION` — one-line summary shown in the report.
- `CASE_EXPECT_RUNTIME_ARGS` — `1` (default) or `0`. Set to `0` for entries
  that are supposed to exit before any runtime argument is built (reserved
  keys, tool/session-name validation, the read-only project-dir error, the
  `--env`+`--zellij --attach` refusal, an attach-by-name miss) — the harness
  treats a `1` entry that captured no runtime args as its own error, since
  that usually means the corpus entry didn't reach what it meant to test.
- `case_setup(){ ... }` — `$1` is the fixture project dir, `$2` the fixture
  home dir. Seed files, git fixtures, or `export DIFF_LIST_OUTPUT=...` /
  `DIFF_INSPECT_DIR=...` / `DIFF_SNAPSHOT_PATHS=...` here.
- `case_args(){ CASE_ARGS_OUT=(...); }` — same two arguments; sets the flags
  passed to the target.
- `case_stdin(){ CASE_STDIN_OUT="..."; }` — defaults to empty (→
  `/dev/null`); set this to script an answer for a prompt the entry
  deliberately exercises.

All four run twice per corpus entry — once for each side of the
comparison — with fresh, independent fixtures, so nothing needs to be
written with two sides in mind.

If a launcher gains a new env var it reads directly from its own process
environment (like `CLAUDE_DNS` or `CLAUDE_CONTAINED_SHARE_HOST_CLAUDE`), add
it to the `unset` line at the top of `run_side` in `harness.sh` — otherwise
one case's `export` leaks into every later case that doesn't touch it.
