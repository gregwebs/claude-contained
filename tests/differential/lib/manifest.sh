#!/usr/bin/env bash
#
# Produces a normalized, path-relative filesystem manifest for a fixture root,
# so two independent fixture roots (different mktemp paths) compare
# structurally instead of by literal temp-dir name. This is what lets the
# harness see effects that never touch stdout/stderr/runtime-args at all --
# the worktree lock/unlock cycle chief among them.
#
# Requires normalize.sh (normalize_text) to already be sourced.

stat_mode() { # stat_mode <path>
  # GNU stat (-c) first (Linux); BSD/macOS stat rejects -c and falls through
  # to -f. Trying GNU's -f first would misparse as its unrelated
  # --file-system flag instead of erroring, so the order here is load-bearing.
  stat -c '%a' "$1" 2>/dev/null || stat -f '%Lp' "$1" 2>/dev/null
}

# capture_manifest <root> <label>; prints one block per filesystem entry under
# <root>, sorted, with <root> itself replaced by <label> in every path so two
# fixture roots with different absolute paths compare equal when their
# contents match. File contents are inlined (normalized) rather than hashed --
# these fixture trees are small, and inline content makes a diverging diff
# directly readable instead of just "hash changed".
capture_manifest() {
  local root="$1" label="$2" path rel

  [[ -d "$root" ]] || return 0

  # Content-addressed object storage (.git/objects/<2-hex>/<38-hex>) is
  # pruned entirely rather than listed: two independent `git commit`s of the
  # "same" fixture setup land in different loose-object hashes whenever they
  # don't fall in the same wall-clock second (the commit object embeds the
  # author/committer timestamp), which has nothing to do with anything the
  # launcher does. What this harness cares about in a git fixture -- the
  # worktree lock file, admin files under .git/worktrees/* -- lives outside
  # this subtree.
  while IFS= read -r path; do
    if [[ "$path" == "$root" ]]; then
      rel="$label"
    else
      rel="${label}/${path#"$root"/}"
    fi

    if [[ -L "$path" ]]; then
      local target
      target="$(readlink "$path")"
      # Symlinks created by the launcher (e.g. ~/.claude.json) point at an
      # absolute path inside this same fixture root, so it needs the same
      # root->label rewrite as rel, or two fixture roots would never compare
      # equal even when structurally identical.
      [[ "$target" == "$root"/* ]] && target="${label}/${target#"$root"/}"
      printf '%s symlink %s -> %s\n' "$rel" "$(stat_mode "$path")" "$target"
    elif [[ -d "$path" ]]; then
      printf '%s dir %s\n' "$rel" "$(stat_mode "$path")"
    elif [[ -f "$path" ]]; then
      printf '%s file %s\n' "$rel" "$(stat_mode "$path")"
      # Everything under a real .git directory except the two files this
      # harness actually cares about (the worktree lock file itself, and the
      # mid-run snapshot isolate.sh's stub takes of it) is git-internal
      # plumbing -- packed refs, the index, reflogs, commit objects -- whose
      # raw bytes embed real wall-clock timestamps and content-derived
      # hashes unrelated to anything the launcher does. Diffing that content
      # would flake on which second each side's `git commit` landed in, so
      # only structural presence (the type/mode line above) is compared for
      # it; the file we actually care about is dumped as normal.
      case "$path" in
        */.git/*)
          case "$(basename "$path")" in
            locked|*.mid-run-snapshot)
              normalize_text < "$path" | sed 's/^/  | /'
              ;;
          esac
          ;;
        *)
          normalize_text < "$path" | sed 's/^/  | /'
          ;;
      esac
    fi
  done < <(
    find "$root" \
      \( -path '*/.git/objects/[0-9a-f][0-9a-f]' -o -path '*/.git/objects/[0-9a-f][0-9a-f]/*' \) -prune \
      -o -print \
    | sort
  )
}
