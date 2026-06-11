---
name: Hangrix Development Task
description: Standard engineering task for the Hangrix platform
---

## Context

This is a Hangrix platform development task. The repository is a monorepo:

- `apps/hangrix/` — Go control-plane service
- `apps/hangrix-agent/` — per-session agent binary
- `apps/hangrix-runner/` — container orchestrator
- `apps/web/` — Nuxt 4 frontend

Refer to AGENTS.md for conventions (layering, sqlc, ioc, testing).

## Requirements

[TODO: describe the feature or fix]

## Acceptance Criteria

- [ ] Typecheck passes (`pnpm typecheck`)
- [ ] All tests pass (`pnpm test`)
- [ ] Build succeeds (`pnpm build`)

