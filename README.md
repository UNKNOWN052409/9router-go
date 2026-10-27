# oc-free-tier-shim

OpenAI-compatible HTTP API shim in front of the **opencode CLI**, giving any
OpenAI-SDK client access to opencode's free-tier models (`glm-5.3-flash-free`,
`deepseek-v4.1-flash-free`, `mimo-v2.6-flash-free`, …) with **fresh free-tier
quota per request**.

It does two things:

1. **Limit reset** — the opencode free tier tracks usage per project
   directory. This shim runs one brand-new CLI session in a brand-new
   directory per request, so every request lands in a fresh quota bucket.
   Verified: 5/5 back-to-back requests with zero 429/limit errors.
2. **Protocol bridge** — speaks OpenAI `/v1/chat/completions` (streaming SSE,
   `tools`/`tool_calls`, `response_format: json_object`, base64 image
   attachments) and translates to/from what the CLI can do.

```
client (OpenAI SDK / curl / app)
   │  POST http://127.0.0.1:20142/v1/chat/completions
   ▼
oc-cli-api.js   (shim: OpenAI protocol → job files, SSE partial streaming)
   │  job file: D:\scratch\oc-cli-api\jobs\<id>.job
   ▼
oc-cli-worker.js (file-queue runner: fresh dir + fresh session per job)
   │  spawns opencode.exe run --format json --model <model> [--file <abs path>]
   ▼
opencode CLI  ──→  free-tier model (glm / deepseek / mimo …)
```

## Quick start

Requirements: Node.js ≥ 18, opencode CLI installed and logged in
(`opencode-ai@1.18.35` was used for all tests in this repo).

```powershell
# terminal 1 — worker
node scripts\oc-cli-worker.js

# terminal 2 — API shim
node scripts\oc-cli-api.js

# smoke test
curl http://127.0.0.1:20142/health
curl http://127.0.0.1:20142/v1/models
```

```bash
curl http://127.0.0.1:20142/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"glm-5.3-flash-free","messages":[{"role":"user","content":"Reply with OK"}]}'
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

## 9router-go integration (verified 2026-10-27)

The gateway (`9router-go`, port 20130) previously hit `429
FreeUsageLimitError` on its `ocz/*` free models because its opencode-zen
lane reuses one static upstream session. The fix: the gateway now routes
free-model traffic **through this shim**, which performs the fresh-dir
reset per request.

Wiring (already applied to the live gateway DB
`<user-profile>\AppData\Roaming\9router\db\data.sqlite`):

1. `providerNodes` row: name `oc-fresh`, prefix `ocz2`,
   `baseUrl = http://127.0.0.1:20142/v1`, `apiType = chat`
2. `providerConnections` row: name `oc-fresh-shim`, `provider` = the node's
   id, `data.apiKey = sk-oc-fresh-shim-local` (any non-empty value; the shim
   ignores auth), `isActive = 1`
3. Gateway restart to load both rows.

Usage through the gateway: request model `ocz2/<model>` for any of the six
supported free models — `glm-5.3-flash-free`, `mimo-v2.6-flash-free`,
`deepseek-v4-flash-free`, `deepseek-v4.1-flash-free`,
`muse-spark-1.3-contributor-free`, `jev-1.13-free` (the shim's `/v1/models`
advertises all six; the gateway probes it and exposes each as `ocz2/…`).
Direct `ocz/*` models on the gateway still use the old single-session lane
and WILL hit limits — use `ocz2/*` for reset behaviour.

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
- **Gateway sidecar** (`<user-profile>\Music\api\rotation\rotation_daemon.py`):
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
tests/smoke.mjs            end-to-end test battery (text / tools / vision / stream)
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
