#!/usr/bin/env bash
#
# Single normalization function, applied uniformly to every captured artifact
# (stdout, stderr, the runtime-argv dump, and manifest file contents) before
# anything is diffed. Three volatile fields are known today; each is its own
# named substitution so a fourth is an obvious addition, not a guess.
#
# This is textual/post-capture rather than freezing `date` in the stub PATH:
# the worktree mutex owner file embeds "$$ $(date +%s)" and the PID half can't
# be frozen that way regardless, so one mechanism covers all three fields
# instead of half-solving it with a stub and half with regex.

# normalize_text: reads stdin, writes normalized text to stdout.
normalize_text() {
  sed -E \
    -e 's/(aic-[a-zA-Z0-9-]*)-[0-9]{4}(-[0-9]+)?$/\1-<TIME>\2/' \
    -e 's/AI_TOOLS_CACHE_BUST=[0-9]{14}/AI_TOOLS_CACHE_BUST=<TOKEN>/g' \
    -e 's/^[0-9]+ [0-9]+$/<PID> <EPOCH>/'
}

# path_hash_8 <path>; mirrors sanitize_foldername's companion in
# claude-contained/claude-docked (default_zellij_session_name's hash half),
# so the harness can compute the exact 8-char hash a given fixture path
# produces and neutralize it -- unlike the fixture's basename (fixed to
# "project"/"home" in isolate.sh precisely to dodge this class of noise),
# the *full* absolute path feeds this hash, and two fixture roots always
# have different absolute paths by construction.
path_hash_8() {
  local path="$1"

  if command -v shasum &>/dev/null; then
    printf '%s' "$path" | shasum -a 256 | awk '{print substr($1, 1, 8)}'
  elif command -v sha256sum &>/dev/null; then
    printf '%s' "$path" | sha256sum | awk '{print substr($1, 1, 8)}'
  else
    printf '%s' "$path" | cksum | awk '{printf "%08x\n", $1}'
  fi
}

# neutralize_path_hash <path>; reads stdin, writes stdout.
neutralize_path_hash() {
  local hash
  hash="$(path_hash_8 "$1")"
  sed "s/${hash}/<PHASH>/g"
}

# neutralize_paths <home> <proj>; reads stdin, writes stdout. Every fixture
# gets its own fresh, randomly-named root directory (mount source/dest
# arguments are the literal project/home paths, by the launcher's own
# path-parity design), so the two sides of a comparison never share an
# absolute path even when otherwise identical. This is a per-side pass:
# call it once per side with that side's own paths, before comparing the
# results against the other side's.
neutralize_paths() {
  local home="$1" proj="$2"
  sed -e "s#${proj}#<PROJ>#g" -e "s#${home}#<HOME>#g"
}
