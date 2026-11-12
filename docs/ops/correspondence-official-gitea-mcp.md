# Official Gitea MCP migration closeout

## Answer first

Ready for independent review in draft PR [#137](https://github.com/drpedapati/sciclaw/pull/137). No merge, release, or deployment has occurred.

## What changed

SciClaw now builds the official Gitea MCP fork containing `create_organization` instead of its custom Bun server. The three existing SciClaw skills use the official project, file, and pull-request tools. The local CLI no longer forwards the removed MCP API key as a Gitea credential.

## Repository position

- Worktree: `/Users/menzy3/projects/.codenomad/worktrees/sciclaw-gitea-official-mcp`
- Branch: `feat/official-gitea-mcp`
- Base: `d7ce6b1`
- Implementation commit: `2a644b6`
- Pull request: [#137](https://github.com/drpedapati/sciclaw/pull/137)

## Verification

- `bun test` in `tools/mcp-server-cli` — passed.
- `bun run build` in `tools/mcp-server-cli` — passed.
- `docker build --tag sciclaw-gitea-mcp:test tools/gitea-mcp` — passed.
- Combined Compose configuration rendered with dummy credentials — passed.
- Container smoke test returned `/healthz` and exposed `create_organization` — passed.
- `git diff --check` — passed.
- `go test ./...` — failed in existing `cmd/picoclaw` test `TestHandleChatSuppressesAgentStderr`; no changed Go code is involved.

## Limits

- The image pins a commit in the fork until the upstream Gitea MCP project releases organization creation.
- No live AtomicQMS mutation or deployment has been performed.

## Next safe action

Obtain an independent review and green CI. Merge and deploy only with separate authorization.

## Independent review prompt

```text
Act as an independent senior engineer reviewing this SciClaw pull request. Do not trust the implementer's summary as proof. Inspect the exact base/head commits, full diff, tests, skills, container packaging, and cited evidence.

Repository: https://github.com/drpedapati/sciclaw
Pull request: https://github.com/drpedapati/sciclaw/pull/137
Base: d7ce6b1
Head: 2a644b6 and the current branch head

Review that the custom MCP server is fully removed, the official fork commit is pinned, the old MCP API key cannot become a Gitea credential, and each migrated skill uses the correct official-tool sequence. Verify the Compose port, health check, CA configuration, and tool allowlist. Confirm no merge, release, or deployment happened.

Return findings by severity with file and line references. Do not merge or push fixes.
```
