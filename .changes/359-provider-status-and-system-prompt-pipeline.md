### Features

- **provider-status**: a status registry and a single-table dashboard that separates "we know how to reach this provider" from "we have seen it answer". Each of the six specified providers carries one record with its documented base/model/chat endpoints, its auth methods, its quota source, a status and a verification timestamp.

  Status is three-valued and deliberately hard to move. A credential validated against the documented `/models` endpoint reaches **configured**; only a real completion returning text reaches **live-verified**, and there is deliberately no setter for the latter — a call site that wants to claim verification has to make the request. Re-listing models after a verification clears the stamp until a completion runs again, so the column always describes the last thing that actually happened.

  Endpoints are transcribed from each provider's own documentation. Where a provider documents no `/models` path the field stays null and the row renders "unavailable" rather than a guessed URL; `codex` (ChatGPT via the documented Codex OAuth) and `glm` are the two entries in that state. Qwen carries API-key auth only: its console login is a browser session, not a documented OAuth grant, and an undocumented flow is not invented.

  New endpoints: `GET /api/provider-status` (the table), `POST /api/provider-status/{id}/validate` (calls the documented `/models` endpoint with the stored credential and shows the provider's own error text verbatim on failure), `POST /api/provider-status/{id}/smoke` (one minimal real completion; an empty response is refused rather than recorded). Credentials are read from the vault only — never from the request — so the handler cannot be pointed at an upstream of the caller's choosing.

- **pipeline**: sanitize client messages and collapse the conversation to exactly one system message. The four ZCode fragments are stripped from user and tool turns (case-insensitively, all occurrences), as are `<system-reminder>`, `<guardrail>`, `<safety>` and `<policy>` blocks. Every system message the client sent is dropped and the proxy's own prompt is installed at index 0. `Verify` asserts that invariant and the chat path refuses to forward a body that violates it, logging which index was wrong rather than silently changing the model's behaviour.

  The body is decoded and re-encoded as raw field maps, so tool definitions, stream flags, sampling parameters, `tool_calls` and `tool_call_id` pass through byte for byte; only a message's `content` is rewritten, and only when it is a plain string, which leaves multimodal array content untouched. A body with no `messages` key — a Responses-native request — is returned unchanged rather than being given a synthesized messages array.

### Verification

- `go test ./internal/proxy/systemprompt/ ./internal/providers/status/ ./internal/handlers/dashboard/ ./internal/handlers/chat/` — sanitizer and reorder table tests, the schema and state-machine tests, and the handler tests for both actions. `go test -tags=integration ./internal/integration/...` and `go test -short ./...` pass, as do `go vet ./...`, `bun run build`, `make vet-svelte` at the pinned baseline, and `bun test src/`.
