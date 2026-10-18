### Nous Research OAuth device flow (RFC 8628)

Added `nous` as a first-class provider in the device-code OAuth family. Ported
from upstream decolua/9router [`src/lib/oauth/providers/nous.js`] alongside
[`open-sse/providers/registry/nous.js`] and [`open-sse/services/nous.js`].

- `POST portal.nousresearch.com/api/oauth/device/code` with
  `client_id=hermes-cli` and scope `inference:invoke inference:mint_agent_key`
  opens the flow; the dashboard renders the verification URL and polls.
- Polling follows RFC 8628: `authorization_pending` / `slow_down` map to the
  `pending` status the dashboard already drives, and the exchange emits the
  access token on success.
- After the exchange the connector immediately mints an agent key at
  `.../api/oauth/agent-key` (`min_ttl_seconds=7200`) — that key is the
  credential inference traffic authenticates with and is what is stored as
  `apiKey`. When the mint fails, the raw access token is used as the fallback,
  exactly like upstream `mapTokens`.
- Registry/transport entry uses the Nous inference base
  (`https://inference-api.nousresearch.com/v1`), and the model catalogue,
  aliases (`ns`, `nous-portal`, `nous-research`) and frontend catalog entry
  carry the upstream display name "Nous Research", icon `psychology`, colour
  `#7C3AED`, and its `modelsFetcher` (`nous-free`) so the free-model list can
  be refreshed from the live catalogue.

Verified with table-driven unit tests (`nous_test.go` — pending mapping,
error-description fallback, JWT email extraction, full fake-portal flow, mint
failure fallback, bad-credential probe) and a live-gated smoke test
(`NINE_ROUTER_LIVE_OAUTH=1`) that opened a real device flow against
portal.nousresearch.com and confirmed the first poll reports pending.

[`src/lib/oauth/providers/nous.js`]: https://github.com/decolua/9router/blob/master/src/lib/oauth/providers/nous.js
[`open-sse/providers/registry/nous.js`]: https://github.com/decolua/9router/blob/master/open-sse/providers/registry/nous.js
[`open-sse/services/nous.js`]: https://github.com/decolua/9router/blob/master/open-sse/services/nous.js
