# PR #118/#131 replacement-architecture review packet

## Requested review

Act as an independent senior engineer reviewing the complete SciClaw stack from the PR #118 base through current PR #131. Inspect repository-visible evidence and rerun tests; do not merge, push, or modify the branch.

Verify:

1. `~/sciclaw` contains only user-facing configuration, profile/global assets, backups, addon source, and visible workspaces; the default/shared workspace is `~/sciclaw/workspaces/default`.
2. `~/.sciclaw` owns auth; `logs/`; `runtime/`; `cache/`; `tmp/`; and deterministic per-workspace runtime state.
3. Normal runtime never resolves, reads, writes, removes, or symlinks `~/.picoclaw`; a valid PicoClaw installation does not trigger a doctor warning.
4. Workspace IDs hash the cleaned absolute configured path without resolving symlinks, and all runtime producers use those stable directories.
5. Backup includes arbitrary configured workspace files and user-facing assets, while machine state is explicitly separated and limited to intentional recoverable data.
6. OpenClaw import targets final config/profile/workspace destinations, refuses an existing authoritative config, and has no PicoClaw destination/import semantics.
7. Local, VM, Docker, Compose, service, TUI, web, and onboarding paths agree with the final architecture.
8. Documentation and tests preserve the `paired-scientist assistant` identity contract.

Return findings by severity with file and line references, commands rerun, residual risks, and a merge recommendation. Do not treat implementer correspondence as proof.

## Required checks

Run `gofmt`, focused path/runtime/migration/backup/doctor/VM/container tests, `go test ./...`, `go build ./...`, `git diff --check`, and `docker compose config` when Docker is available. Report the known `go vet` lock-copy failure precisely if it reproduces; do not change unrelated code to suppress it.

## Safety boundary

There is no automated PicoClaw-to-SciClaw migration. Legacy SciClaw layouts require manual, reviewed relocation. `~/.picoclaw` must remain untouched because it may contain an independent PicoClaw installation.
