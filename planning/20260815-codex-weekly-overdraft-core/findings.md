# Findings

## Approved Design Evidence
- The supplied PoC demonstrates that appending a paired `custom_tool_call` and `custom_tool_call_output` can permit limited generation after a Codex quota window reaches 100%.
- Historical plugin experience showed much better results from supporting tool-output tails than from aggressively increasing pair count.
- Historical concurrency incidents were amplified by long upstream requests, refresh serialization, retries, and duplicated request bodies; the core transform must remain stateless and non-blocking.
- The first release deliberately excludes synchronous five-attempt probes, on-429 replay, token drain, and scheduler changes.

## Working Baseline
- Isolated worktree: `/Users/suo/.config/superpowers/worktrees/CLIProxyAPI/codex-weekly-overdraft-core-20260815`.
- Branch: `codex/codex-weekly-overdraft-core-20260815`.
- Starting commit: `78f0c4079e3e6273d65d03b5549cffc898703264` (`origin/main` at task start).
- Full baseline `go test ./...` passed.
- Latest upstream still has one shared post-normalization boundary after reasoning replay and before cache/identity request construction for both HTTP and WebSocket Codex paths.
- WebSocket retry paths rebuild `response.create` from the already prepared upstream body, so applying the transform once before that point prevents duplicate injection.
- Existing full-YAML management updates and watcher-driven executor rebinding provide the feature write path and hot reload; only a read-only status endpoint is needed in core.
- The nested config can safely use concrete scalar fields because both file and byte parsers seed the complete conservative defaults before YAML unmarshalling; explicit zero values are rejected instead of broadening behavior.
