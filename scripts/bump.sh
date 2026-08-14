#!/bin/sh
# bump.sh — version bump automation for eka-core (tag-driven semver).
#
# eka-core has no version file: the version is the git tag. This script
# computes the next semantic version from the latest tag and pushes it,
# which is what Go module consumers resolve (`go get
# github.com/maleolabs/eka-core@v1.1.0`).
#
# Usage:
#   ./scripts/bump.sh patch    # 1.0.0 -> 1.0.1
#   ./scripts/bump.sh minor    # 1.0.0 -> 1.1.0
#   ./scripts/bump.sh major    # 1.0.0 -> 2.0.0
#
# Prerequisites:
#   - clean working tree
#   - the latest tag is the current version
#
# This is an internal development tool.

set -eu

if [ $# -lt 1 ]; then
  echo "Usage: $0 <patch|minor|major>"
  exit 1
fi

BUMP_TYPE="$1"
case "$BUMP_TYPE" in
  patch|minor|major) ;;
  *)
    echo "Error: invalid bump type '$BUMP_TYPE'. Use: patch, minor, or major"
    exit 1
    ;;
esac

CURRENT="$(git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//')"
if [ -z "$CURRENT" ]; then
  echo "Error: no existing tag found — create an initial tag first (e.g. git tag v1.0.0)"
  exit 1
fi

MAJOR="$(echo "$CURRENT" | cut -d. -f1)"
MINOR="$(echo "$CURRENT" | cut -d. -f2)"
PATCH="$(echo "$CURRENT" | cut -d. -f3)"

case "$BUMP_TYPE" in
  major) MAJOR=$((MAJOR + 1)); MINOR=0; PATCH=0 ;;
  minor) MINOR=$((MINOR + 1)); PATCH=0 ;;
  patch) PATCH=$((PATCH + 1)) ;;
esac

NEW_VERSION="$MAJOR.$MINOR.$PATCH"
TAG="v$NEW_VERSION"

echo "Current version: $CURRENT"
echo "New version:     $NEW_VERSION ($TAG)"

git tag "$TAG"
git push origin "$TAG"

echo ""
echo "Done. $TAG pushed — Go module consumers can now resolve $NEW_VERSION."
