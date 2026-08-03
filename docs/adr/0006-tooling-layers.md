# Projects Bring Their Own Toolchain As A Derived Image

The launcher cannot run its own development. `make quality` pins ShellCheck
0.11.0, golangci-lint 2.12.2 and a Go toolchain per `go.mod`, and the image
carries none of them — so an agent working on this repository inside a container
built from this repository cannot compile, lint or test the change it is making.

The two extension mechanisms that already existed are both dead ends.
`INCLUDE_JAVA_LAYER` selects between a populated and an empty Dockerfile stage,
so every new toolchain means another stage, another build argument, and a pull
request to *this* repository — the launcher ends up knowing the name of every
language its users write in. `custom-packages.txt` splices extra apt packages
into the base image, but it is gitignored, documented only in a Dockerfile
comment, and limited to apt. Neither travels with the project that needs the
toolchain, which is the whole requirement: the toolchain belongs to the project,
not to the launcher.

The decision is to split the image in two. This repository owns a **base
image**. A project owns a **tooling layer** — a Dockerfile it checks in — and the
launcher builds it into a **derived image** and runs that instead. Everything
below is a consequence of that split, and each section names the alternative it
rejected and the cost it accepted.

## The layer is a whole Dockerfile, not a snippet

A layer declares `ARG BASE_IMAGE=claude-contained:latest` and builds `FROM
${BASE_IMAGE}`. That is the entire contract. The launcher overrides the argument
with the base image's resolved ID; the default in the file is what keeps the
layer buildable by hand, `docker build -t my-layer .`, and by a devcontainer
that has never heard of this launcher.

**Rejected: a fragment spliced into a template.** It needs a template language,
which the launcher would then own; it breaks building the layer by hand, which is
how anyone debugs one; and it makes the launcher the author of half of a file the
project is responsible for.

**Accepted cost:** nothing prevents a layer from breaking the base image's
invariants — replacing the entrypoint, removing the sandbox, changing the user.
Examples and documentation carry that weight rather than validation, because the
container belongs to the project. A launcher that policed a project's Dockerfile
would be back to knowing what a toolchain is.

## Identity is a content hash, not a state file

The derived image is tagged `claude-contained-layer:<project>-<hash>`, where the
hash covers the base image's **resolved ID**, the layer Dockerfile, and every
file in its build context. Staleness detection then follows from naming rather
than being implemented: if an image with that tag exists, run it; if it does not,
build it.

**Rejected: a recorded state file** saying what was built from what. It is a
second source of truth that can disagree with the image store — after a prune,
after a manual `image rm`, after a machine restore — and every disagreement is
silent. **Rejected: an explicit build step.** One more thing to forget, and
forgetting it produces a toolchain that looks healthy and is stale, which is the
one failure mode this feature must not have.

Hashing the base's resolved ID rather than its mutable tag is what makes
`--rebuild` invalidate every derived image implicitly, with no flag and no
bookkeeping: the ID the derived image was named after no longer exists, so its
tag is no longer the tag the launcher computes. `--rebuild` therefore keeps
meaning the base image alone.

**Accepted cost:** derived images accumulate at roughly a gigabyte each, one per
project per layer version per base version, and `image prune` does not remove
tagged images. Cleanup is documented rather than automatic. A launcher that
deleted images it cannot prove are unused would be a worse failure than disk
growth.

## The build context is the layer directory, not the project

The layer directory is both the layer's home and its build context, defaulting
to `.claude-contained/layer/` inside the project. A dedicated subdirectory rather
than `.claude-contained/` itself, because that directory already holds
launcher-owned state — and folding a `node_modules` overlay into the build
context would rebuild the toolchain on every dependency install.

**Rejected: the project root as the context.** It would put the project's entire
source tree into the hash, so every edit to any file would rebuild the toolchain.

**Accepted cost:** everything in the layer directory is hashed, including
anything an agent happens to drop there, and no `.dockerignore` is interpreted —
implementing dockerignore matching above `internal/runtime` would put
build-context semantics in the launcher where they could disagree with the
runtime that actually applies them. Over-hashing costs a spurious rebuild;
under-hashing costs a stale toolchain, so the asymmetry is deliberate.

This is also why an oversized context is *warned about* and never refused. The
directory is writable from inside the container, so a hard limit would let a
contained agent brick its own project's launcher — the only escape being
`--no-layer`, which is exactly the healthy-looking container with no toolchain
this design exists to prevent.

## Every build is confirmed, and nothing is remembered

The layer Dockerfile is writable from inside the container, like the project env
file, but its blast radius is larger: building it makes the host's container
runtime execute arbitrary steps with unrestricted network egress, which is
precisely what the sandbox exists to prevent. Every derived build is therefore
confirmed interactively. Because a build only fires when the content-hash tag is
missing, confirming at build time covers first use and every subsequent change,
with no approval state anywhere.

**Rejected: a record of approved hashes.** It would suppress the prompt only
after a prune or a base rebuild — the two cases where nothing about the layer
changed — and it would need a *second* hash keyed on the Dockerfile alone, since
trust concerns the build steps rather than which base they sit on. Two hashes and
a trust store, to avoid a prompt that fires once per actual change.

`--build-layer` answers the prompt ahead of time; `--no-layer` runs the base
image. Neither gets an environment variable, because an environment variable is a
stored approval by another name — exported once and forgotten, it defeats the
confirmation — and because a forgotten `--no-layer` is a container silently
missing its toolchain.

**Accepted cost:** a base rebuild re-prompts once per project.

## Runtime environment is resolved inside the container

A tooling layer installs declarative `KEY=VALUE` fragments under
`/etc/claude-contained/env.d/`. One resolver reads them after root setup and
sandbox-policy generation, at the point the entrypoint drops to the dev user.
Attach invokes that same resolver because container exec bypasses the
entrypoint. The host therefore supplies only host facts such as `HOME`; it does
not encode Java or toolchain paths.

The parser follows the project-env file's literal line and quote rules, but
expands only complete `$HOME`/`${HOME}` and `$PATH`/`${PATH}` references. An
explicit process environment value wins over a fragment, while `PATH` alone
remains fragment-owned so tooling layers can extend it compositionally.
Ordinary keys, including `JAVA_HOME`, stay user-settable. Files are applied in
lexical order so later fragments can extend the environment resolved by earlier
ones.

**Rejected: sourcing fragments.** It would turn a data file into code executed
before the sandbox. Keys that affect privilege setup, dynamic loading, shell
startup, or the sandbox are refused, and sandbox executables are invoked by
absolute path so a fragment-controlled `PATH` cannot replace them.

**Rejected: resolving fragments on the host.** The host would need to know
container paths and duplicate image behavior for normal run, attach, and
Zellij. Keeping the resolver in the image gives all entry paths one contract.

**Accepted cost:** this parser is a small image-side counterpart to the Go
project-env parser. They share documented behavior rather than code because the
resolver must also work when the Go launcher is no longer present during
attach.

## Failure is hard, and absence must be earned

A failed layer build is a hard error carrying the builder's own exit status, and
never falls back to the base image. A fallback starts a container that looks
healthy while its toolchain is missing, and a build failure is a defect in the
layer rather than a recoverable cache problem. A missing base image is reported
rather than built, because building it implicitly would turn a first run into a
long surprise.

The corollary is easy to miss and is the reason `Runtime.ImageID` has the shape
it does. Because "the base image is absent" sends the user somewhere expensive
and possibly wrong, the image probe never infers absence from a bare nonzero
exit: it confirms the probe subcommand exists first, so a runtime CLI that spells
that subcommand differently produces a named fault instead of an unfixable "run
`--rebuild=full`" on a machine where the image is right there.

**Rejected: matching the runtime's error text** to recognize "no such image".
Locale-, version- and wording-dependent, and both runtimes are free to change it.

## Labels are Docker-only, and nobody reads them

Derived images carry `claude-contained.layer*` labels naming the project, the
source Dockerfile and the base image's ID — on Docker. The Apple renderer drops
them, mirroring what `RenderRun` already does with `LabelArg`.

**Rejected: emitting `--label` on both runtimes** on the strength of the
documented flag. Apple Containers' `container build --label` is documented but
was not run before this decision was made, and the failure shapes are not
comparable: a wrong image-probe guess degrades to "rebuilds every run, loudly",
while a rejected build flag *fails the build*, on the primary platform, with no
fallback by design. Choosing the asymmetry before the risk can fire costs
nothing; discovering it after merge is an outage.

**Accepted cost:** on Apple Containers, derived images carry no provenance
metadata. That costs nothing operational, because nothing ever reads a label
back — identity and staleness ride entirely on the tag, the rule ADR-0002
established for Zellij discovery. The repository name and the tag are the cleanup
story on both runtimes, which is what they were anyway.

## The Java built-in is retired

`INCLUDE_JAVA_LAYER` and the unconditional Maven and Vaadin mounts go away. Java
becomes a shipped example tooling layer, and the launcher stops knowing any
toolchain's name. The Java retirement is now implemented; its fragment serves
launcher runs and attach, while matching image environment serves direct
devcontainer execution that bypasses the entrypoint.

**Accepted cost:** the contained Maven cache splits from the host's, which
`-m ~/.m2` restores for anyone who wants it back.

## Consequences

- Nothing above `internal/runtime` learns any new runtime syntax. The base
  image's identity comes back through one seam method as an opaque string, the
  two runtimes report different kinds of digest, and the caller sets labels
  unconditionally while the runtime decides whether to render them.
- Labels are written and never read, and only on Docker.
- **Switching container runtimes rebuilds every derived image**, because the two
  report different identities for the same base image. Correct, and surprising.
- The derived tag is a stable function of the checked-in tree: two developers on
  the same commit with the same base get the same tag. File modes are hashed in
  git's model — one execute bit — rather than as raw permission bits, which is
  what makes that true across umasks. It also narrows "a changed layer always
  rebuilds": a `chmod 0640` genuinely changes what `COPY` puts in the image and
  does *not* change the tag.
- Two concurrent runs of the same project can both build the same tag. The second
  overwrites; nothing is lost. There is no lock, because the worktree mutex
  exists to guard mutable *user* state and a duplicated idempotent build wastes
  only time.
- A project with no tooling layer takes the plain base-image path: no hash,
  probe, prompt, or new argument. That holds because the layer directory is
  resolved
  *before* the base image is probed — an ordering worth preserving deliberately,
  and one the golden suite pins.
- `--rebuild` never builds a layer. It returns before the layer step, so a
  derived image is rebuilt on the next ordinary run rather than as part of the
  rebuild.
- `plan.Summarize` is called on prompt rounds, where `program.Run` is nil and
  `program.Pending` is not. Any future diagnostic field derived from the run spec
  must be set inside that guard.
