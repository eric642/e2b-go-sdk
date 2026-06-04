#!/usr/bin/env bash
# Generate GitHub Release notes for a tag by diffing it against the
# previous release tag.
#
# Usage:
#   scripts/release-notes.sh v0.2.0     # notes for an explicit tag
#   scripts/release-notes.sh            # notes for $GITHUB_REF_NAME, else the
#                                       # tag pointing at HEAD, else newest v*
#
# Prints Markdown to stdout: a "What's Changed" commit list (the changes
# between the previous release tag and this one) plus a "Full Changelog"
# compare link. Designed to feed `gh release create --notes-file -`.
#
# Requires full git history with tags (clone with fetch-depth: 0 in CI).
#
# Environment:
#   GITHUB_REPOSITORY   owner/repo for compare + commit links. Falls back to
#                       parsing `git config remote.origin.url`.

set -euo pipefail

die() { printf 'error: %s\n' "$*" >&2; exit 1; }

# --- resolve the tag we're cutting notes for ---------------------------------

TAG="${1:-${GITHUB_REF_NAME:-}}"
if [[ -z "$TAG" ]]; then
  # Tag pointing exactly at HEAD, else the newest v* tag.
  TAG="$(git describe --tags --exact-match 2>/dev/null \
    || git tag --list 'v*' --sort=-v:refname | head -n1)"
fi
[[ -n "$TAG" ]] || die "could not determine a tag; pass one explicitly"

git rev-parse -q --verify "refs/tags/$TAG" >/dev/null \
  || die "tag not found: $TAG (is the full history fetched?)"

# --- resolve the previous release tag in this line of history ----------------
# `git describe` walks commit topology, so PREV is the most recent v* tag
# reachable from the commit before TAG — i.e. the previous release, even when
# tags aren't strictly version-ordered.
PREV="$(git describe --tags --abbrev=0 --match 'v*' "${TAG}^" 2>/dev/null || true)"

# --- derive owner/repo for links ---------------------------------------------

repo_slug() {
  if [[ -n "${GITHUB_REPOSITORY:-}" ]]; then
    printf '%s' "$GITHUB_REPOSITORY"
    return
  fi
  local url
  url="$(git config --get remote.origin.url 2>/dev/null || true)"
  url="${url%.git}"
  url="${url#git@github.com:}"
  url="${url#ssh://git@github.com/}"
  url="${url#https://github.com/}"
  url="${url#http://github.com/}"
  printf '%s' "$url"
}
SLUG="$(repo_slug)"

# --- build the body ----------------------------------------------------------

printf '## What'\''s Changed\n\n'

if [[ -n "$PREV" ]]; then
  RANGE="${PREV}..${TAG}"
else
  # First release: everything reachable from the tag.
  RANGE="$TAG"
fi

# Newest first, drop merge commits so the list reads as real changes rather
# than "Merge pull request #N …" noise. Subjects and the short SHA are
# auto-linkified by GitHub in release bodies.
COMMITS="$(git log --no-merges --pretty=format:'- %s (%h)' "$RANGE" || true)"
if [[ -n "$COMMITS" ]]; then
  printf '%s\n' "$COMMITS"
else
  printf '_No code changes since %s._\n' "${PREV:-the start of history}"
fi

printf '\n'

if [[ -n "$SLUG" ]]; then
  if [[ -n "$PREV" ]]; then
    printf '**Full Changelog**: https://github.com/%s/compare/%s...%s\n' "$SLUG" "$PREV" "$TAG"
  else
    printf '**Full Changelog**: https://github.com/%s/commits/%s\n' "$SLUG" "$TAG"
  fi
fi
