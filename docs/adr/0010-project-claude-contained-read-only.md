# `.claude-contained/` Is Read-Only Inside The Container

Status: accepted

Ticket 04 of #20 (#39) restores mount-flag injection as user configuration,
read from a new project-local file, `<project-dir>/.claude-contained/commands.json`.
The project SRT policy, `<project-dir>/.claude-contained/srt-settings.json`,
is likewise host-read configuration. Deciding where those files live raised a
question the project env file and the tooling layer had already answered,
differently: both were previously
documented as *"writable from inside the container, so it is a convenience
rather than a trusted input."* Adding a third file to that trust tier without
revisiting it would have meant a contained agent could rewrite its own mount
injection map to influence its own next run -- and reopened the same question
for the two files already there.

## The decision

When `<project-dir>/.claude-contained` exists on disk at probe time, the
launcher adds a second bind mount for the whole directory, layered read-only on
top of the read-write project-directory mount. A Zellij run then adds its
generated session store as a narrower read-write child mount:

```
--mount type=bind,src=<PROJ>,dst=<PROJ>
--mount type=bind,src=<PROJ>/.claude-contained,dst=<PROJ>/.claude-contained,readonly
--mount type=bind,src=<PROJ>/.claude-contained/zellij,dst=<PROJ>/.claude-contained/zellij
```

Most-specific-mount-wins is already how both runtimes resolve overlapping
binds, so this needs no new mechanism -- it is the same nested-mount shape
`internal/plan` already emits for a plain `-m dir:ro` extra mount
(`internal/plan/plan.go`'s mount list; `internal/runtime/runtime.go`'s
`renderMount`), aimed at a subtree of an already-mounted directory instead of
an independent one.

This reverses previously documented, previously shipped behavior: the project
env file (`internal/env`) and, when present, the tooling layer
(`.claude-contained/layer/`, [ADR-0006](0006-tooling-layers.md)) become
read-only from inside the container. A running container can no longer rewrite
either -- or `commands.json` or `srt-settings.json` -- to change the *next*
run. The Zellij child is generated runtime state rather than host-authored
configuration, so it remains writable without weakening those sibling paths;
see [ADR-0002](0002-zellij-session-store.md).

## Why the whole directory, not just the new file

`commands.json` could have been mounted alone, leaving the env file and layer
on their previous trust tier. Rejected: it would leave two files in
`.claude-contained/` writable from inside the container and one not, for no
principled reason a project owner could explain, and it would not fix the
staleness the env-file and layer documentation already had. Mounting the whole
configuration directory is one mount and one story: host-authored inputs under
`.claude-contained/` are read once before the container starts. Generated state
uses narrower, explicit child mounts rather than making the configuration mount
writable.

## Scope: hardens against a running container, not a malicious checkout

The read-only mount stops a *running* container from tampering with
`.claude-contained/` to affect a *later* run. It does not sanitize a file a
checkout already ships: every file under `.claude-contained/` is read on the
host, before the container starts, so a malicious `commands.json`,
`srt-settings.json`, or env file already committed to an untrusted checkout is
read exactly as before this decision. `--no-project-env` (skipping the env
file) and inspecting or avoiding project-local configuration remain the
mitigations for an untrusted checkout -- this decision does not add or remove
either.

The host-side malformed-JSON hard errors for `commands.json` and
`srt-settings.json` (exit 2, path named) are independent of the mount: they
fire when the launcher parses the file before the container starts, mount or
no mount.

## Interactions checked

- **Node_modules overlay** ([ADR-0006](0006-tooling-layers.md), `-N`). Its
  writable bind targets `<dir>/node_modules`, a *sibling* of
  `.claude-contained/`, not a path underneath it. The overlay's directory does
  live at `<dir>/.claude-contained/node_modules-<platform>`, but that directory
  is created *during* the run -- after the probe this decision gates on --
  so a project whose only `.claude-contained/` content is the overlay never
  gets the read-only mount at all, and a project that already has one for
  another reason (an env file, say) sees a read-only *view* of the overlay's
  source sitting alongside the overlay's own read-write bind at
  `node_modules` -- harmless, because nothing writes through the read-only
  path.
- **Apple Containers' sequential-bind limitation** (issue #25,
  `Profile.ReadonlyRemountNeedsExistingMountpoint`) bites when a read-only
  parent must create a *new* destination for a nested mount. Here the parent
  (the project directory) is mounted read-write and applied first, and
  `.claude-contained/` already exists on disk as part of that mounted tree --
  the destination is not newly created under a read-only parent, so the
  limitation does not apply. Live Apple Containers verification confirmed the
  nested behavior: a Zellij run could write its child session store while a
  write to the sibling `.claude-contained/env` failed as read-only.
- **Zellij session store** ([ADR-0002](0002-zellij-session-store.md)). The host
  creates `.claude-contained/zellij/` before starting the runtime, then the
  runtime applies its read-write child mount after the read-only parent. The
  destination therefore already exists and most-specific-mount-wins preserves
  the configuration boundary around its siblings.

## Existence-gated, not unconditional

The mount is emitted only when `.claude-contained` exists **as a directory** at
probe time. Both runtimes can only bind-mount a directory, never a single file,
so gating on "exists and is a directory" is what makes the mount possible at
all, not an optimization. A project with no `.claude-contained/` -- the common
case -- gets no new mount, no new fact, and an unchanged golden result.

## Consequences

- `internal/plan.Facts` gains `ProjectClaudeContainedExists`, probed with
  `os.Stat` in `probeFacts` alongside the package's other filesystem facts
  (`Build` stays pure and may not stat the directory itself).
- `CONTEXT.md`'s "Project env file" entry and `USAGE.md`'s env-file security
  paragraph and layer-size paragraph are corrected to describe the file as
  read-only inside the container rather than writable; see those documents for
  the current wording.
- Golden cases that write into `.claude-contained/` before probe (the env-file
  and layer cases, plus the new mount-flag-injection case) each gain the one
  read-only mount line shown above. A case that creates
  `.claude-contained/` only *during* the run (the node overlay) does not.
