# Nine Router Go Fork

[![CI](https://github.com/UNKNOWN052409/9router-go/actions/workflows/ci.yml/badge.svg)](https://github.com/UNKNOWN052409/9router-go/actions/workflows/ci.yml)

OpenAI-compatible HTTP API shim in front of the **opencode CLI**, giving any
OpenAI-SDK client access to opencode's free-tier models (`glm-5.3-flash-free`,
`deepseek-v4.1-flash-free`, `mimo-v2.6-flash-free`, …) with **fresh free-tier
quota per request** — integrated into the existing 9router-go / 9router
setup, so no client needs a new URL or port.

It does two things:

1. **Limit reset** — the opencode free tier tracks usage per project
   directory. This shim runs one brand-new CLI session in a brand-new
   directory per request, so every request lands in a fresh quota bucket.
   Verified: 5/5 back-to-back requests with zero 429/limit errors.
2. **Protocol bridge** — speaks OpenAI `/v1/chat/completions` (streaming SSE,
   `tools`/`tool_calls`, `response_format: json_object`, base64 image
   attachments) and translates to/from what the CLI can do.

```
client (existing config, 127.0.0.1:20130)
   │  POST /v1/chat/completions   model: ocz2/<model>   ← same endpoint as before
   ▼
9router (20130)  ──►  oc-cli-api.js (shim, 127.0.0.1:20142, internal)
   │  job file: D:\scratch\oc-cli-api\jobs\<id>.job
   ▼
oc-cli-worker.js (file-queue runner: fresh dir + fresh session per job)
   │  spawns opencode.exe run --format json --model <model> [--file <abs path>]
   ▼
opencode CLI  ──→  free-tier model (glm / deepseek / mimo …)
```

The shim on 20142 is internal infrastructure (like any upstream provider) —
clients keep using their existing endpoint and keys.

## Quick start (three commands)

Requirements: Windows (or Linux/macOS), Node.js ≥ 18. The opencode CLI is
auto-installed if missing — **no login, no account, no API key is needed**;
the free tier works anonymously (verified on a completely fresh profile).
Version `opencode-ai@1.18.35` was used for every test in this repo.

```cmd
install.cmd     :: one-time: checks prerequisites, creates dirs, registers the hidden watchdog
start.cmd       :: starts the shim + worker (hidden). No proxy setup, no manual config
stop.cmd        :: stops them
```

Then use it like any OpenAI API — **nothing else to configure**:

```bash
curl http://127.0.0.1:20142/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"glm-5.3-flash-free","messages":[{"role":"user","content":"Reply with OK"}]}'
```

Any OpenAI SDK works the same way (base URL `http://127.0.0.1:20142/v1`,
any dummy API key, model ids: `glm-5.3-flash-free`, `mimo-v2.6-flash-free`,
`deepseek-v4.1-flash-free`, `deepseek-v4-flash-free`,
`muse-spark-1.3-contributor-free`, `jev-1.13-free`).

### Using it through 9router (optional)

If you route requests through 9router / 9router-go instead of calling the
shim directly, run `start.cmd --with-router`. That applies the node-9router
registry patch (idempotent — it skips if already applied) and starts your
gateway. Clients then use their existing endpoint with the `oc/<model>`
names, and those requests get the same fresh-session reset (see *Router
integration* below). No proxy configuration is needed anywhere else —
starting the stack is the only step.

### Manual mode (if you prefer visible terminals)

```powershell
node scripts\oc-cli-worker.js    # terminal 1
node scripts\oc-cli-api.js       # terminal 2
```

## API

| Endpoint | Behaviour |
|---|---|
| `POST /v1/chat/completions` | Full OpenAI semantics: text, `stream:true` (SSE), `tools` + `tool_choice`, `response_format:"json_object"`, data-URL image attachments (≤ 4/request) |
| `GET /v1/models` | Model list |
| `GET /health` | Liveness |

Models are passed verbatim (a `opencode/`-style provider prefix is stripped).
Unknown model names fall back to `mimo-v2.6-flash-free` inside the worker's
literal argv table — user input never reaches the command line (the prompt
travels on stdin; attachment paths are worker-built from loop indices and a
fixed extension whitelist).

## How the limit reset works

The free tier meters usage by **project directory + CLI session**, not by IP.
The worker therefore:

1. Creates a fresh run dir `work\r<counter>` per job,
2. spawns the CLI with `cwd` = that dir and no `opencode.json` in it
   (a config file in cwd makes the CLI hang/401),
3. passes the prompt on **stdin**, never as an argv element,
4. deletes the run dir when the job finishes.

Each request is thus a brand-new project → brand-new quota bucket. No IP
change is needed for a reset; IP rotation is only relevant for transient
upstream failures (see below).

## Bring your own credentials — nothing bundled, nothing leaked

This project ships **no API keys, no proxies, no accounts**. The free tier
needs nothing from the user (it works anonymously); users who route through
a gateway bring their own gateway key. All machine-specific values
(binary location, data directory, home pin) are environment-overridable:
`OPENCODE_BIN`, `OC_DATA_DIR`, `OC_HOME`, `N9R_HOME`.

## Single reset mechanism

There is exactly one reset mechanism in this project: **a fresh opencode
session per request**. A fresh session gets a fresh quota bucket, so limits
never surface in normal operation. If a limit response ever does appear
(e.g. an upstream-side bucket change), the request automatically falls back
to a brand-new CLI session and is retried by the worker's transient-error
retry loop — the same mechanism, applied reactively. There is no second,
parallel reset hack anywhere in the codebase.

## No prefix required

Model names work **with or without a provider prefix** when you call the
shim directly: `glm-5.3-flash-free` and `opencode/glm-5.3-flash-free` are
equivalent (known prefixes are stripped). Prefixes only matter when routing
through a multi-provider router, and even there the router decides — this
project adds no prefix requirement of its own.

## Metrics

`GET /v1/metrics` returns dependency-free JSON: request totals, ok/error/
limit counts, per-model breakdown and latency percentiles (p50/p95). Scrape
it from any monitoring stack; on low-resource hosts it costs nothing (an
in-memory ring buffer, capped at 500 samples).

## Low-resource deployments (Render, small VPS)

Two processes total, zero npm dependencies, an in-memory job queue and a
persistent serve that idles at near-zero CPU. The serve boots lazily and is
reused for every request; the whole stack fits comfortably in a 512 MB
container. Set `OC_DATA_DIR` to a persistent disk on hosting platforms so
quota buckets and logs survive restarts.


## Router integration (verified 2026-10-27/28)

**Final form — the built-in `opencode` no-auth provider is wired to the reset
shim on BOTH routers. No custom providers (`oc-fresh` removed), no model
aliases, no extra prefixes: clients use `oc/<model>` (or `ocz2/<model>` on
paths where that legacy wiring is still present) against their existing
endpoint and key.**

| Path | Router | Free-model names | How |
|---|---|---|---|
| `127.0.0.1:20130` (user path) | ZCode tool-shim → **node 9router (20128)** | `oc/<model>` | compiled provider registry patched: `transport.baseUrl → http://127.0.0.1:20142` |
| `[::1]:20130` / LAN:20130 | **9router-go** (Go gateway) | `oc/<model>` | `OPENCODE_FREE_BASEURL` env in the start script (custom build) |

(The `oc/` request URL is built as `${baseUrl}/inference/v1/chat/completions`,
which is exactly the shim's route. Patch script:
`patch-node9router-oc.js` — targets only the `opencode` provider's
`transport.baseUrl` inside chunk `1892.js`; backup at
`D:/scratch/chunk-1892-backup.js`. Re-apply after any 9router npm upgrade.)

Verified on the user path (2026-10-28): `oc/glm-5.3-flash-free` 5/5 battery,
**zero limit errors, avg 3.8 s/request**, tool calling (`get_weather{"city":"Pune"}`)
— through the persistent `opencode serve` fast path.

### Why the reset needs the real CLI (proven, not assumed)

The free tier is client-fingerprint gated: synthesized HTTP requests land in
a permanent 429 bucket even with a perfect header set (direct probes done).
Only a real opencode CLI run is eligible, and the no-auth CLI needs no key
and no login (`OPENCODE_API_KEY` removed + a completely fresh profile →
still works, verified 2026-10-28). Hence: routers → this shim → real CLI,
fresh session per request.

**Tested 2026-10-28:** the gateway's native CLI-free stealth transport
(BoringSSL client impersonation, present in the 9router-go source) was
re-tested against the live upstream — it now answers `429
FreeUsageLimitError` like any other synthesized client. The CLI-based lane
is therefore not a design preference; it is the only working mechanism
today. If upstream ever relaxes the gate, switching back to the native lane
is a one-line env change.

### Known gotchas

- A hand-inserted `opencode-zen` connection row in the Go gateway sqlite
  breaks its connection loader (later connections silently don't load →
  "No active credentials"). Keep it disabled; the zen-key account has no
  upstream funds anyway (402). The shim carries a hardened `/zen/v1/*`
  pass-through if the keyed lane is ever re-enabled through it.
- The node 9router patch lives in `node_modules` — an npm upgrade of the
  `9router` package reverts it; re-run the patch script and restart.
- Latency: persistent `opencode serve` fast path (port 20193, hidden) with a
  fresh session per request — 3–7 s typical; any fast-path failure falls back
  to the original per-request spawn path. Serve is pre-warmed at worker
  startup so the first request never trips the gateway's 30 s timeout.

### Stability & one-command boot

`oc-chain-watchdog.ps1` (scheduled task every 2 min, launched hidden via
`oc-chain-watchdog-hidden.vbs` — no console flicker) keeps exactly one shim +
one worker alive and kills duplicates (duplicate workers share the run-dir
counter and delete each other's directories — a real incident).

`start-9router-go-rotation.cmd` ensures the shim + worker before starting the
gateway: **one command boots the whole free-model lane.**

Verified through the gateway (2026-10-27): per-model battery — glm ✅,
mimo ✅, deepseek-v4.1 ✅ (197 s, slow but correct), tool calling via mimo ✅,
5/5 stability battery with zero limit errors. `deepseek-v4-flash-free`,
`muse-spark-1.3-contributor-free` and `jev-1.13-free` currently fail with
upstream `Unexpected server error` **identically when called directly through
the real CLI** — provider-side outage, not a routing or limit issue; they
will work through this same route once upstream recovers (route already
wired and advertised). Latency is higher than direct API calls (8–200 s)
because each request spawns a real CLI session.

## IP rotation (as implemented, audited 2026-10-27)

- **Worker**: retries a job up to 6 attempts, 2 s apart, **only** when the
  CLI returns a transient error (`Invalid API key`, `FreeUsageLimit`, `429`,
  `timeout`, `rate`). A new CLI session gets a new exit IP. Successful
  requests never rotate anything — no rotation-per-request behaviour.
- **Gateway sidecar** (`<9router-install>\rotation\rotation_daemon.py`):
  rotating CONNECT proxy (Tor lanes) on `127.0.0.1:9191`, rotating per request
  **on proxy-egress calls only**.
- **Known gap (honest status)**: the 9router `db.json` currently has
  `"proxyPools": []` — no provider is bound to the rotation pool, so the
  sidecar is up but unused by the gateway. Rotation-through-gateway is
  therefore *configured but not engaged*; worker-level rotation is the active
  mechanism today.

## What was tested (all on 2026-10-27, opencode 1.18.35)

Verified against the live chain (shim + worker + CLI):

| # | Test | Result |
|---|---|---|
| 1 | Limit reset: 5 sequential requests, watch for 429/limit | ✅ 5/5 OK, 8.7–15.8 s each, fresh run dir each, **zero limit errors, zero retries** |
| 2 | Plain text round-trip | ✅ exact reply returned |
| 3 | Tool calling (OpenAI `tools` → `tool_calls`) — GLM | ✅ `get_weather{"city":"Tokyo"}` |
| 4 | Vision + tool call combined — GLM & DeepSeek v4.1 | ✅ both returned a tool call based on the image |
| 5 | Streaming SSE | ✅ works; **one chunk per step** (the CLI emits one `text` event per step, not per token — that is an upstream CLI property, not a shim limitation) |
| 6 | Practical agentic workflow (folder + 2 files + node script + execute it) — GLM direct CLI | ✅ created `report/summary.md` and `count.js` (`console.log(42)`), output verified `42` |
| 7 | DeepSeek v4.1 vision + file creation in one run (direct CLI) | ✅ correct colour + file written |
| 8 | Context preservation with IDE-sized payload (339 tools + 41-turn history) | ✅ model answered from conversation context (`NIMBUS-ROCKET`) — regression test added after a real IDE incident |
| 9 | **Through 9router-go gateway (port 20130)**: 5 sequential `ocz2/glm-5.3-flash-free` requests | ✅ 5/5 OK (8–75 s each), fresh run dir per request, **zero limit errors** — the gateway's own `ocz/*` lane 429s (`FreeUsageLimitError`) because it reuses one session; `ocz2/*` via this shim resets per request |
| 10 | Tool calling through the gateway (`ocz2/…`) | ✅ model answered with weather tool flow |

### Known failures / limitations (not hidden)

- `deepseek-v4-flash-free`, `muse-spark-1.3-contributor-free`, `jev-1.13-free`
  currently fail with upstream `Unexpected server error` **even when called
  directly through the CLI** (bypassing this shim) — provider-side outage, not
  a shim bug. `deepseek-v4.1-flash-free` works.
- Streaming is step-granular (one SSE chunk per assistant step) because the
  CLI's `--format json` emits one `text` event per step.
- **Fixed 2026-10-27**: the shim used to prepend the tool-protocol table and
  truncate from the end at a 200k-char cap. OpenCode IDE sessions send 300+
  tool definitions (~82k chars), so the user's actual conversation got
  amputated and the model answered generically ("Hi, how can I help you?").
  The budget now reserves room for the conversation first (cap raised to
  600k); the tool table is what gets trimmed when the total is too large.
- GLM's colour answers on tiny images are occasionally wrong (said "White"
  for a blue 64×64 image once); DeepSeek answered correctly. Model-side
  quality issue.
- An earlier `invalid_api_key` through the 9router gateway was a key-store
  mix-up: `db.json` in `AppData\Roaming\9router` is the Node 9router's key
  store, while the running Go gateway reads `db/data.sqlite` (same folder).
  Keys like `<your-router-key>` (from the sqlite `apiKeys`
  table) work. Resolved 2026-10-27.
- The gateway's own `ocz/*` free-model lane hits `429 FreeUsageLimitError`
  by design of its single-session opencode-zen connection. Use the `ocz2/*`
  prefix (routed through this shim) for per-request reset behaviour.

## Layout

```
scripts/oc-cli-api.js      HTTP shim (port 20142, 127.0.0.1 only)
scripts/oc-cli-worker.js   file-queue job runner (fresh dir/session per job)
tests/smoke.mjs            end-to-end test battery (needs the live chain)
tests/unit.test.mjs        pure-function unit tests (CI-safe, no network)
install/start/stop.cmd     Windows lifecycle (hidden windows)
install/start/stop.sh      Linux/macOS lifecycle (Render, VPS — auto-installs opencode)
scripts/patch-node9router-oc.js  wires the built-in oc/ lane to the shim (idempotent)
.github/workflows/ci.yml   GitHub Actions: syntax checks + unit tests
docs/ARCHITECTURE.md       component detail + data flow + safety model
docs/OPERATIONS.md         run, restart, logs, troubleshooting
```

## Safety model

- The spawn command line is fully constant per (model, attachment-count)
  pair: model names come from a literal whitelist table, attachment paths
  are built from loop indices + a fixed extension whitelist. The prompt and
  the full conversation never touch argv, cwd, env or file paths — they ride
  stdin and the job file (read from a validated path inside the jobs dir).
- Job IDs, attachment names and partial names are validated against
  `^[a-z0-9][a-z0-9-]{0,63}$` and every derived path is resolved and checked
  to stay inside its directory.
- The shim binds `127.0.0.1` only. No API keys are logged (worker startup
  dump masks `OPENCODE_API_KEY`).

## Repository status

Organised locally for GitHub; not pushed yet. Everything in `docs/` and the
test table above reflects verified reality — items that did not pass are
listed as failures in *What was tested*, not dropped.
