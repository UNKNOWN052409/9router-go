### Fixes

- **opencode-go**: stop offering the subscription lane as an image, video and embedding provider. Its catalog entry listed `serviceKinds: ["llm", "image", "video", "embedding"]`, which put OpenCode Go on the dashboard's Text to Image, Video and Embedding pages — where every request 404s upstream (probed live: `opencode.ai/zen/go/v1/{images/generations,embeddings,audio/*,videos}` all return 404, as do the same paths on the `zen` and `inference` bases). `serviceKinds` is now `["llm"]`, so the lane appears only where it answers.
- **opencode-go**: register the subscription lane's own System One endpoint and client headers in the backend registry. `opencode-go` pointed only at `.../zen/go/v1/chat/completions`, so a System One request fell back to the derived `.../v1/systemone` on the wrong lane. It now declares `SystemoneURL: https://opencode.ai/zen/go/v1/systemone` and the same `x-opencode-client` / `User-Agent` static headers the Zen lane sends.

### Verification

- `bun test src/lib/providers.test.ts` — new assertions pin that no OpenCode lane (`opencode`, `opencode-go`, `opencode-zen`) is returned by `getProvidersByKind` for `image`, `video`, `embedding`, `tts` or `stt`, and that the go lane's `serviceKinds` is exactly `["llm"]` with the go System One base URL. The existing artwork test also caught and now covers the missing `nous.png` provider icon (real Nous mark, upscaled to the catalog's 128×128).
- `go test ./internal/providers/`, `bun run build` and `make vet-svelte` at the pinned baseline.