# Independent review: typed provider error taxonomy

Review `cincibrainlab/codex/safe-provider-errors...feature/provider-error-taxonomy` independently.
Do not trust the implementation summary. Submit findings by severity with file
and line references; approve only when no material findings remain.

Verify:

1. `providers.APIError` remains the sole structured HTTP/provider error type.
2. `ProviderErrorInfo` classification uses typed context, network, EOF, syscall,
   status, provider-code, and message evidence in a safe precedence order.
3. A semantic model or context error overrides a misleading generic 5xx status.
4. `Retry-After` parsing accepts millisecond, seconds, and HTTP-date headers
   without altering current request behavior.
5. Agent safe presentation remains equivalent for JSON CLI, web, Discord, and
   jobs; normal CLI behavior is unchanged.
6. Final provider failures and hooks contain stable category and logical-call
   correlation fields without requiring raw error parsing for statistics.
7. No retries, fallback chains, cooldowns, compaction, or configuration changes
   were introduced in this phase.
8. Tests fail for the prior classification/correlation behavior and cover the
   important precedence and transport cases.
