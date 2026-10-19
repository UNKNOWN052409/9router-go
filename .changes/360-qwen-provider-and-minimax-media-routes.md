### Features

- **qwen**: add Qwen as a provider on Alibaba Cloud DashScope, using the documented OpenAI-compatible endpoint. The model list is discovered live from `/compatible-mode/v1/models` rather than hardcoded, so the catalog tracks whatever the account can actually reach. Registered with alias `qw`, the international region as the default base URL (override `baseUrl` on the connection for the China region), and the DashScope text-to-image path as its image endpoint. API-key auth only — Qwen's console login is a browser session, not a documented OAuth grant, so no web auth flow is offered. Video is deliberately not advertised: on DashScope it is an asynchronous create-then-poll task with no single POST endpoint, so it needs an executor rather than a URL.

### Fixes

- **minimax**: fix the image and TTS routes, which 404'd for every request. The media handler derives its target from `BaseURL` when no override is declared, and that derived path is OpenAI-shaped — but MiniMax does not serve OpenAI media paths (`/v1/images/generations` and `/v1/tts` both answer 404 on either region, probed). The real endpoints are `/v1/image_generation` and `/v1/t2a_v2`, which answer 200, and both regions now declare them. The catalog already listed this provider as an image and TTS source, so the capability was advertised but unreachable.

  Note the remaining limitation: the media handler passes the upstream response through untouched, so MiniMax's own response envelope reaches the client as-is rather than in OpenAI shape.

### Verification

- `go test ./internal/providers/` — new tests pin that every media-capable provider declares a non-derived endpoint, that the MiniMax overrides are the real paths rather than bases the handler would rewrite, and that Qwen registers with live discovery, no static catalog, no video endpoint, and API-key-only auth. `bun test src/` covers the dashboard catalog entry (391 pass). `go test -tags=integration ./internal/integration/...`, `bun run build` and `make vet-svelte` at the pinned baseline pass.

  One pre-existing failure is unrelated: `TestHandleChangelog` fetches the live upstream changelog and asserts the body contains "Changelog", which the current upstream release notes do not. It fails identically with these changes stashed.
