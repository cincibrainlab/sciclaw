# Independent review: same-provider retry policy

Review `feature/provider-error-taxonomy...feature/provider-error-retry` independently.

Verify bounded two-attempt retries use only typed `ProviderErrorKind` values,
honor bounded Retry-After values, stop immediately on context cancellation, and
do not retry authentication, permission, quota, context, model, invalid request,
policy, cancellation, or unknown errors.

Verify the helper is used by the main agent loop, summaries, shared tool loop,
and lightweight web chat; intermediate attempts do not produce user output;
and final agent logs report the real attempt count and correlation ID.

Excluded: SDK retry changes, fallback chains, cooldowns, compaction, proactive
rate limiting, configuration, and semantic empty-response retry.
