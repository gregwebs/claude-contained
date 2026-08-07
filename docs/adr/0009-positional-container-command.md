# Positional Container Command

Status: accepted

[ADR-0003](0003-flag-only-cli.md) removed every positional argument from the
launcher's command line. This decision brings exactly one back: the first
token that is not itself consumed by a flag terminates flag parsing, and
everything from it onward is executed inside the container verbatim, replacing
the image's default `CMD`. This ADR exists because a reader who reaches this
feature after reading ADR-0003 — titled "Flag-Only Command Line" and stating
plainly that "nothing before `--` is positional" — will otherwise find the
reversal inexplicable.

## Why positionals were removed, and why one comes back

ADR-0003's positionals were ambiguous *with flag values*, not merely
old-fashioned. `-a/--attach` guessed whether a bare token was its own value or
a path ("not `-*`, no `/`, not `.`"); `-R` consumed the next token only when it
was literally `tools` or `full`; `--new-session` went further and rejected a
name-like token outright so it could not be confused with `main_dir`. Every
one of those heuristics existed because a positional could appear *anywhere*,
adjacent to any flag, and the parser had to guess which.

This feature reintroduces exactly one positional role — the container
command — and places it behind a boundary that terminates flag parsing
outright: once the first unconsumed token is seen, nothing after it is parsed
as a flag at all, so there is nothing left to disambiguate. The ambiguity
ADR-0003 removed does not return, because there is no longer a flag parser
running past that point to be confused.

## Why the launcher now knows no program names

Today the launcher gates a closed set of five AI CLIs via `-t/--tool`,
encodes each one's permission-skipping flag for `-y/--yolo`, and injects
`--add-dir` for exactly two of the five on `-m` mounts. Each of those is a
standing commitment to track another project's CLI surface: a new flag name,
a renamed tool, or a sixth CLI a user wants to run all require a change to
*this* launcher.

Recognizing a tool is not a lighter form of validation than gating one — it
is the same coupling with a softer failure mode. A gate rejects what it does
not know; a recognizer silently does the wrong thing (or nothing) for what it
does not know, which is worse, because the failure looks like success. The
container command grammar removes the launcher's knowledge of program names
entirely: the first positional is passed through unexamined, and whatever
runs inside the container is the image's problem, not the launcher's.

## Why the default is the image's `CMD`, not a launcher constant

When no container command is given, the container's own `CMD` runs — not a
string the launcher hard-codes. This keeps a tooling layer in control of what
its own image runs by default, the same ownership boundary [ADR-0006](0006-tooling-layers.md)
draws between the base image and a project's layer. It is also what makes
`-s/--shell` an *escape hatch* rather than a *mode*: a shell is one of several
things the image's `CMD` might already be, not a launcher-level alternative to
it.

**Attach exception:** `container exec` / `docker exec` bypass a container's
`ENTRYPOINT`/`CMD` by design — that is what `exec` means on both runtimes —
so there is no image default for an attached session to inherit. Attach
therefore defaults to `shell-run` explicitly rather than falling through to
an image default that does not exist for it (ticket 03 of #20).

## The accepted costs

This is an intentional breaking change to the CLI, not a compatibility-
preserving refactor:

- `claude-contained .` now attempts to execute a directory as the container
  command and fails from inside the container, instead of printing `-C`/`-m`
  guidance the way the old positional heuristics did.
- `-t claud` (a typo) is no longer caught by the launcher; it becomes a
  command-not-found failure inside the container instead of a launcher-level
  `Unknown tool:` message.
- Bare `claude-contained` and `-a foo` land in the container's default shell
  rather than starting Claude, because the launcher no longer assumes Claude
  is what anyone wants by default.
- `-m` stops injecting `--add-dir` until a user opts back in through the
  configuration ticket 04 introduces; there is no interim tool-name gate.
- All three golden trees (`apple-darwin`, `docker-darwin`, `docker-linux`)
  churn deliberately once ticket 02 lands, because the observable CLI
  contract genuinely changes.

## Rejected alternatives

Each of these will otherwise be re-proposed, because each looks like a
smaller change than the grammar above:

- **A dedicated `--run` flag.** `--run claude --foo` spends a flag and a
  positional to carry one decision (what to execute), where a bare positional
  carries it alone. It also does not solve the underlying problem: `--run`
  still needs its own value-consumption boundary, which is the same
  disambiguation ADR-0003 removed, just renamed.
- **`--` as *the* marker for the container command**, rather than an
  always-legal, always-optional one. The chosen grammar treats `--` as a
  marker meaning "the command starts here, if you need to say so explicitly"
  (needed only when the command's own first token could be mistaken for a
  launcher flag) — not as the *only* way to introduce a command. Requiring
  `--` unconditionally would make the common case (`claude-contained claude
  --foo`) fail without it.
- **Keying `--`'s meaning on whether `-t` was given.** Making the positional
  boundary conditional on an earlier flag would make the disambiguator
  non-adjacent: a reader (or the parser) could not tell whether a given token
  was a flag value or the start of the container command without scanning the
  entire line for `-t`. The boundary must be determinable locally, which is
  exactly the property ADR-0003 fixed and this ADR must not undo.
- **Keeping a closed tool set solely to drive `--add-dir` injection.** This
  was the strongest temptation, because `--add-dir` is genuinely useful and
  losing it silently is a real cost. But a closed set kept *only* to gate one
  flag injection is "the AI assumption in a new spelling" — the launcher
  would still need to know program names, just for a narrower reason. Ticket
  04 replaces the gate with explicit user configuration instead.

## Consequences ticket 02 and 03 realize

- Under `--zellij`, the image's `CMD` is never consulted: Zellij owns the
  pane's command and substitutes its own shell invocation, so the container
  command default only matters outside a Zellij session. This is an accepted
  limitation, not an oversight: it means a tooling layer's own `CMD` can never
  be the pane's initial command under Zellij, and it makes `-s` inconsistent
  across the boundary it otherwise treats as invisible -- a debug shell is
  `/usr/local/bin/shell-run` outside Zellij but plain `bash` inside it, because
  the pane already supplies a controlling terminal `shell-run`'s rationale
  assumes is missing.
- `-a`/`-R`, having lost their old value-guessing heuristics, require `--` to
  introduce a container command when one is wanted alongside them, since
  their own values are no longer distinguishable from a bare positional by
  guesswork.
