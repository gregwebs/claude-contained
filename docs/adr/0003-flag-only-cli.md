# Flag-Only Command Line

The launcher accepted `[main_dir] [extra_dir ...]` positionally, which forced every optional-value flag to guess whether a bare token was its own value or a path. `-a/--attach` used a heuristic (not `-*`, no `/`, not `.`), `-R` consumed the next token only when it was literally `tools` or `full`, and `--new-session` went further and *rejected* a name-like token outright so it could not be confused with `main_dir`. An unrecognized flag fell through the parse loop's `*) break` arm and silently became `main_dir`, so a typo surfaced later as a confusing path error. The project directory now comes from `-C/--dir` (default: the current directory) and extra mounts from repeatable `-m/--mount DIR[:ro|:rw]`; nothing before `--` is positional, so all three heuristics are gone and unknown flags and stray positionals are hard errors that name their replacement.

This was done deliberately ahead of the planned Go rewrite of the launcher, so that the port is a behavior-preserving translation instead of a reimplementation of disambiguation logic that was about to be deleted anyway.

Two flags were split rather than carried over. `-a NAME` had three meanings — attach to a running container, or, on a name miss, *create* a new container with that name, or, under `--zellij`, name a Zellij session — so a typo silently started a second container. `-a/--attach` now only attaches and errors when nothing matches; `--name NAME` names a new container; `--session NAME` names a Zellij session and is the only way to do so, leaving `--new-session` as a force flag that takes no value.

## Consequences

- `claude-contained .` is now an error rather than a synonym for the default. Bare `claude-contained` is unchanged, which is the common case, and the error names both replacement flags.
- The `claude-docked` name was dropped in ticket 11: both runtimes now install under one name, and Docker is selected with `--container-runtime=docker` or `CLAUDE_CONTAINED_RUNTIME=docker` (an `argv[0]` basename containing `dock` still works too, as a compat affordance).
- A second `--` now reaches the tool verbatim instead of being silently dropped, so `--` can be forwarded to tools that have their own separator convention.
- `--share-skills` accepts both `--share-skills DIR` and `--share-skills=DIR`, matching `--dns`, `--allow-host` and `--env`.
- Value-taking flags used to run a bare `shift; x="$1"` and died with `unbound variable` under `set -u` when given last; they now report a missing value. `tests/arg-parsing.test.sh` covers this for every such flag.
