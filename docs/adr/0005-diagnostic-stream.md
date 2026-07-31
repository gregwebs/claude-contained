# A Separate Diagnostic Stream, Silent By Default

The launcher printed composed prose for users and nothing at all for whoever was
debugging it: six warning sites in `internal/host/worktreelock.go` print a
summary and discard the underlying error, and the generic `error: %v` wrappers
flatten an error chain to one line. We added a second output channel built on
`log/slog` — **diagnostic records** — and deliberately did *not* route the
existing user-facing output through it. Those messages are composed prose with
hanging indents and aligned continuations (`rebuild.go:134-136`,
`cli.go:425-428`); emitting them as slog records would mean either a
raw-passthrough handler, which is `fmt.Fprintln` in costume, or rewriting them
into `level=WARN msg="..."`, a visible regression in an interactively-run CLI.

Three consequences will look like bugs to someone who wasn't part of the
decision, so they are recorded here.

## The channel is silent by default

With no flag and no environment variable the launcher emits nothing new: a
user's terminal is byte-identical to what it was. The alternative — defaulting to
`error`, so that detail is present the first time something fails — was rejected
because "identical unless you ask" is a rule anyone can hold in their head and
needs no per-site judgment about whether a record is user-appropriate. The price
is real and was accepted: a **non-reproducible** failure loses its detail
permanently, and re-running with `--log-level=debug` does not recover a runtime
that was briefly down or a file since cleaned up.

## Retrieval falls back to a discarding logger, not `slog.Default()`

The logger travels in `context.Context`. When none is present, retrieval returns
`slog.New(slog.DiscardHandler)`. `slog.Default()` would have been the obvious
choice and is wrong here: it writes through the `log` package to the real
`os.Stderr`, bypassing the `stdout`/`stderr` writers threaded through `run` —
the writers the golden suite captures into a buffer. A call site whose context
was never plumbed would emit records that are absent from the golden `stderr`
section, leak into the test runner's own output, and do it at `Info` with no flag
passed. The invariant worth protecting is that bytes reach a terminal **only**
through the threaded writers.

The accepted cost is that a forgotten plumb is invisible: you add a log line, run
with `--log-level=debug`, see nothing, and have to discover that the context at
that call site is a bare `context.Background()`. Threading a `*slog.Logger`
explicitly instead would have made that a compile error, at the cost of a third
parameter on every signature that already carries two writers.

## Relocated output is never level-filtered

Under `--log-only` the threaded writers are substituted so that every print
becomes a record — **relocated output**. Those records do not pass through the
level filter, so `--log-only --log-level=error` still emits a user-facing
warning. That reads as a filter bug and is not one.

The level filter governs diagnostics, which the launcher chose to emit for a
debugger's benefit and which are safe to discard. Relocated output is the
program's *actual output*, merely changed address, and discarding it is data
loss — `--log-only --log-level=error` would otherwise mean "do the work, destroy
the report." The two share a destination and a record shape; they do not share a
filter. Classification therefore rides on a `kind=output|diagnostic` attribute
rather than on severity, which also leaves room for filtering by metadata later.
Levels on relocated output are metadata only, assigned by stream — every stderr
line is tagged `warn`, which is often wrong and tolerable precisely because these
records are never filtered.

## The CLI front end parses silently before it validates

The front-end order is `Probe → Parse → Select → Help → Validate`. The first CLI
pass is permissive and emits nothing: it records flag state and ordered syntax
failures so parsing cannot write a diagnostic before the diagnostic stream is
configured. Container-runtime selection between the two passes is pure and does
not inspect or start either runtime.

Parsing continues after effective help only far enough to derive the final
container runtime and therefore choose the correct runtime-specific help
profile. The validation pass owns rendering the first recorded failure, the
existing semantic checks, their exact order, and their exit status. Help that
was reached before a syntax failure remains an early success; a syntax failure
reached first remains the usage error.

## Consequences

- Some facts are emitted twice: once as a user-facing warning, once as a record
  carrying the cause. That is the intended shape, not duplication to be cleaned
  up.
- Environment variable values are never logged at any level, enforced by
  `slog.LogValuer` on the carriers. The most useful record in the program is the
  container argv, and it carries the tool process environment as `-e KEY=VALUE`,
  including `GH_TOKEN`. The logged argv is consequently *not* the argv and cannot
  be pasted to reproduce a run.
- The glossary in `CONTEXT.md` says "diagnostic"; the flags say `--log-level`,
  `--log-file`, `--log-only`. The mismatch is deliberate: this project already
  has container logs, Zellij logs, and five agent CLIs producing things people
  call logs, but `--log-level` is what every CLI on earth calls that flag.
