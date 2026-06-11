---
name: Version Release (Tag & Changelog)
description: Creates a new semver tag on main with auto-generated release notes. CI picks up the tag and publishes the release automatically.
---

## Context

This repo uses semver tags (`vMAJOR.MINOR.PATCH`, e.g. `v0.7.28`) to trigger the release pipeline.
Pushing a `v*` tag fires `.hangrix/workflows/release.yml`, which builds all artefacts and publishes the release automatically — **do not create a release manually**.

Current tag series: `v0.x.y` (minor = breaking/large features, patch = fixes + small additions).

## Core Steps

### 1. Find the last tag

```bash
LAST_TAG=$(git tag --sort=-version:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1)
echo "Last tag: $LAST_TAG"
```

### 2. Diff from last tag to main

```bash
git fetch origin main
git diff "$LAST_TAG"..origin/main --stat        # volume overview
git log  "$LAST_TAG"..origin/main --oneline     # commit list
git diff "$LAST_TAG"..origin/main               # full diff (pipe to less for large repos)
```

### 3. Decide the version bump

Read the diff and apply this table:

| Signal in diff | Bump |
|---|---|
| Breaking change in API / DB schema / config format | **minor** (we are pre-1.0, so minor = breaking) |
| New user-facing feature (UI, endpoint, agent capability) | **minor** |
| Bug fix, refactor, dependency upgrade, docs, CI change | **patch** |
| Pure documentation / meta-file change | **patch** |

Compute the new version:

```bash
# Example: LAST_TAG=v0.7.27, bump=patch → NEW_TAG=v0.7.28
#          LAST_TAG=v0.7.27, bump=minor → NEW_TAG=v0.8.0
```

### 4. Write the release notes

Organise commits into sections; omit merge commits and trivial chores.

**Template:**

```markdown
## What's Changed

### ✨ New Features
- <short description> (<commit-sha-short>)

### 🐛 Bug Fixes
- <short description> (<commit-sha-short>)

### 🔧 Improvements & Refactors
- <short description> (<commit-sha-short>)

### 📦 Dependencies & CI
- <short description> (<commit-sha-short>)

**Full changelog:** `v0.7.27...v0.7.28`
```

Rules:
- One bullet per logical change (group trivial commits when sensible).
- Keep bullets to ≤ 120 chars.
- Omit sections that have no entries.

### 5. Create and push the tag

The release notes go into the **tag annotation** (not a separate release body — CI reads the tag message to populate the GitHub Release).

```bash
NEW_TAG="v0.7.28"          # replace with computed version

git fetch origin main
git checkout origin/main   # detached HEAD at main tip

git tag -a "$NEW_TAG" -m "$(cat <<'EOF'
## What's Changed

### ✨ New Features
- ...

### 🐛 Bug Fixes
- ...

**Full changelog:** v0.7.27...v0.7.28
EOF
)"

git push origin "$NEW_TAG"
```

> **Verify**: `git show "$NEW_TAG"` — confirm the annotation text looks correct before pushing.
> Once pushed the tag is permanent; CI will fire immediately.

## What NOT to Do

- ❌ Do **not** create a GitHub Release manually — CI handles that via `release.yml`.
- ❌ Do **not** tag a commit that isn't on `main` (or isn't yet merged).
- ❌ Do **not** reuse or force-push an existing tag.

## Acceptance Criteria

- [ ] `git tag --sort=-version:refname | head -1` returns the new tag.
- [ ] `git show <new-tag>` shows the release notes annotation.
- [ ] CI workflow `release` is triggered (check `.hangrix/workflows/release.yml`).
