# PR #131 final two-root architecture F1 correspondence

## Responsible Agent

- Terra, primary F1 implementation engineer

## Summary

Replaced PR #118/#131's unified-root design with explicit user-facing and machine-managed SciClaw roots. Normal SciClaw runtime no longer resolves PicoClaw's home, and routed workspaces receive deterministic isolated runtime state.

## Description

- `~/sciclaw` owns the sole active config, profile files, global skills, templates, backups, addon source, and visible workspaces; the default/shared workspace is `~/sciclaw/workspaces/default`.
- `~/.sciclaw` owns auth plus centralized `logs/`, `runtime/`, `cache/`, `tmp/`, and stable per-workspace sessions, state, hook events, inbound staging, `.ctxclaw`, `.venv`, and heartbeat logs.
- `~/.picoclaw` remains untouched and PicoClaw-owned. `migrate --unify` and all remove/symlink behavior were deleted; generic OpenClaw import now targets SciClaw-owned destinations.
- Local, web, TUI, services, VM, Docker, Compose, Makefile, backup, doctor, templates, tests, README, published docs, and the ops review packet were aligned.

## Status And Verification

- Status: independent-review corrections, broad validation, and final diff inspection are complete.
- Passed: `gofmt`, focused tests, `go test ./...`, `go build ./...`, `docker compose config`, and `git diff --check`.
- Vet: `go vet ./...` reproduces only the unrelated lock-copy diagnostic at `cmd/picoclaw/phi_eval_cmd.go:128`, where assigning `cfgCopy` copies `config.Config` containing `sync.RWMutex`.

## Conflicts And Decisions

- The prior plan used `~/sciclaw/workspace` and proposed an automated split migration. Owner instructions superseded it with `~/sciclaw/workspaces/default` and no PicoClaw migration behavior.
- Legacy SciClaw migration is manual-only. Runtime session/state/service code has no `~/.picoclaw` lookup or fallback candidates and rejects actively configured PicoClaw-owned roots/workspaces.
- Backup intentionally includes full arbitrary visible workspace content and only selected recoverable machine state.
- Independent review required lexical workspace hashing without symlink resolution, centralized hidden subdirectories, persisted service root overrides, private runtime permissions, direct profile import planning, profile/workspace hook precedence, all-workspace backup coverage, and VM workspace selection before venv setup; these corrections are implemented.
- Final review additionally required migration root/source validation, physical symlink containment checks, non-overlapping homes, confined hook audit paths, backup-preserving `--force`, profile-based doctor TOOLS checks, and fatal propagation of migration execution errors; these corrections and regression tests are implemented.
- The final narrow pass rejects workspaces overlapping machine state, validates hook audit parents physically and rejects destination symlinks, follows only configured workspace root symlinks during backup, and removes all runtime `share/picoclaw` asset discovery.
- Portable path validation and file open remain separate operations, so a privileged local actor could race a hook-path component between validation and append. Eliminating that TOCTOU window requires platform-specific no-follow directory-handle APIs beyond this minimal portable change.
- Before test-home isolation was added, an early onboarding test created six templates under the real `~/sciclaw/profile`. The owner authorized removal, and those generated files and the empty directory were removed.

## Consultations

- Terra implemented the owner-provided architecture. Independent read-only reviewers checked migration, runtime isolation, service behavior, documentation, and the final security boundary; all blocking findings were addressed.

## Future Steps

- Obtain independent review using `docs/ops/pr-118-unify-migration-safety-review.md` before merge.
- Group commits by paths, runtime, migration, packaging, and documentation; do not merge or release without owner approval.
