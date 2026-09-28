# Provider error statistics

Gateway logs expose stable fields on final provider failures. Do not parse the raw `error` field for statistics.
The same `llm_call_id` is included in `before_llm`, `after_llm`, and `on_error` hook metadata for per-call correlation.

```sh
# Count final failures by category, provider, and model.
jq -r 'select(.message == "LLM call failed" and .fields.final) | [.fields.error_kind, .fields.provider, .fields.model] | @tsv' gateway.log | sort | uniq -c

# Count final outcomes by logical provider call.
jq -r 'select((.message == "LLM call complete" or .message == "LLM call failed") and .fields.final) | if .message == "LLM call failed" then "failure" else "success" end' gateway.log | sort | uniq -c
```
