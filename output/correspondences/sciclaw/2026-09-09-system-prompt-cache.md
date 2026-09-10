# System Prompt Cache

- Responsible agents: Max coordinated; Chloe implemented; Ponytail scoped/pruned; William advised; Rachel analyzed; Jefferson independently reviewed.
- Summary: Added a PR #617-style static prompt cache with a stable provider prefix and measured it against the provider-free baseline.
- Branch: `perf/system-prompt-cache` in `/tmp/sciclaw-system-prompt-cache`, based on `eb557d5`.
- Implementation: Caches static identity/tool prose, bootstrap files, skills, and memory; appends current time and sender/theme dynamically; tracks source metadata; forces a one-second refresh; sorts tool outputs deterministically; exposes a by-value sender API.
- Correctness: Covers primary/shared bootstrap transitions, memory and daily-note changes, skill precedence and edits, same-size preserved-mtime refresh, mutable tool/config inputs, concurrent callers, and prompt-inspection accounting.
- Benchmark boundary: Controlled sentinel provider stops timing immediately on `Chat` entry and performs no provider serialization, HTTP, network, or LLM work.
- Results: Warm prompt construction improved from 164.5 us to 37.1 us; message assembly from 167.6 us to 39.6 us; end-to-end pre-provider work from 414.8 us to 273.5 us. Forced rebuilds increased to 200.3 us.
- Profile: Warm `os.ReadFile` work disappeared; cache validation is now led by `os.ReadDir` and `os.Stat`.
- Verification: `go test ./pkg/agent ./pkg/skills ./pkg/tools`, `go test -race ./pkg/agent ./pkg/skills`, focused registry race testing, benchmarks, profile validation, and `git diff --check` passed.
- Conflict: Full `pkg/tools -race` has pre-existing timeout-test races; focused changed-code race coverage passes. No unrelated files were modified.
- Status: Accepted by independent review; uncommitted for PI inspection.
- Strict cache-only A/B: Thirty fresh-process paired samples, alternating AB/BA order, held all branch behavior constant except cache reuse. Cache reuse reduced mean system-prompt time by 80.303% (95% CI 80.135%-80.471%) and mean pre-provider time by 42.633% (95% CI 41.996%-43.270%). Raw and calculated evidence is stored under `~/notes/projects/sciclaw-strict-cache-ab-*`.
- Reproducibility: The exact temporary treatment diff and collection/statistics methodology are retained as `~/notes/projects/sciclaw-strict-cache-ab-treatment.patch` and `~/notes/projects/sciclaw-strict-cache-ab-methodology.md`. The sentinel provider method executes only to stop timing and validate arguments; no production provider or provider-side serialization runs.
- Restoration: Temporary A/B benchmark code was removed. SHA-256 for `pkg/agent/loop_benchmark_test.go` returned to `5b74eaee8fbcf2848b09ba37464199def49573b161f74adadb5b37c933e2b93d`; `pkg/agent/context.go` remained `614582c2a9cd30798c1796a2f11108c7154f83d7b4533f8d8e04548458856ced`.
- Future steps: Reduce metadata polling and guard disabled debug formatting as separate optimizations.
