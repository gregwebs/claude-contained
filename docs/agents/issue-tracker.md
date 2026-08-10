# Issue tracker: GitHub

GitHub Issues is the tracker of record for this repository. Specs, tickets, and
their discussion live as GitHub issues and comments, not as files under
`.scratch/`.

## Reading and writing issues

Go through the `/github-app` skill, which dispatches to
`./scripts/gh-app.sh`:

- `issue-get` — fetch an issue's body and metadata
- `issue-create` — file a new issue
- `issue-comment` — comment on an issue
- `issue-sub-add` — add a sub-issue relationship (e.g. a ticket under a parent
  epic issue)

Use the `/github-app` skill even for read-only access: it allow-lists the
interaction patterns this repository relies on.

## Wayfinding operations

Used by `/wayfinder` for exploratory planning artifacts (map/child tickets),
which is a separate concern from the GitHub issue tracker above.

- **Map**: `.scratch/<effort>/map.md` — the Notes / Decisions-so-far / Fog body.
- **Child ticket**: `.scratch/<effort>/issues/NN-<slug>.md`, numbered from `01`, with the question in the body. A `Type:` line records the ticket type (`research`/`prototype`/`grilling`/`task`); a `Status:` line records `claimed`/`resolved`.
- **Blocking**: a `Blocked by: NN, NN` line near the top. A ticket is unblocked when every file it lists is `resolved`.
- **Frontier**: scan `.scratch/<effort>/issues/` for files that are open, unblocked, and unclaimed; first by number wins.
- **Claim**: set `Status: claimed` and save before any work.
- **Resolve**: append the answer under an `## Answer` heading, set `Status: resolved`, then append a context pointer (gist + link) to the map's Decisions-so-far in `map.md`.
