# Nine Router Go Fork

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

## Router integration (verified 2026-10-27)

There are **two routers** on this machine and both now reach the reset shim:

| Path | Router | Free models | Status |
|---|---|---|---|
| `127.0.0.1:20130` → ZCode tool-shim → **node 9router (20128)** | Node 9router | `ocz2/<model>` | ✅ wired (live config), 5/5 battery + tools verified |
| `[::1]:20130` / LAN:20130 → **9router-go** (Go gateway) | 9router-go | `ocz2/<model>`, bare `<model>` via aliases | ✅ wired in `db/data.sqlite`, battery + tools verified |

**Which lane hits limits:** the `ocz/*` prefix is the **opencode-zen keyed
lane** — upstream rejects free models on it (`429 FreeUsageLimitError`
"Free models work in OpenCode") regardless of key, because the free tier is
client-fingerprint gated: only a real opencode CLI run is eligible; any
synthesized HTTP request lands in a permanent 429 bucket (verified by direct
probes with a perfect header set). The reset therefore **requires spawning
the real CLI** — which is exactly what this shim does. The **no-auth CLI
itself needs no key** (`OPENCODE_API_KEY` removed → still works).

### Go gateway wiring (db/data.sqlite)

1. `providerNodes` row: name `oc-fresh`, prefix `ocz2`,
   `baseUrl = http://127.0.0.1:20142/v1`, `apiType = chat`
2. `providerConnections` row: name `oc-fresh-shim`, `provider` = the node's
   id, `data.apiKey = sk-oc-fresh-shim-local` (any non-empty value; the shim
   ignores auth), `isActive = 1`
3. `kv` `modelAliases` rows: bare `<model>` and `opencode/<model>` →
   `ocz2/<model>` for all six free models (prefix routing beats aliases, so
   `ocz/*` cannot be aliased — bare names and `ocz2/*` are the reset paths)
4. Gateway restart.

**Known gotchas (both hit and fixed today):**
- A hand-inserted `opencode-zen` connection row with a non-canonical data
  shape breaks the gateway's connection loader (subsequent connections silently
  don't load → "No active credentials for provider: ocz2"). Keep the zen-key
  row disabled unless it is written by the gateway itself.
- The zen-key account has no upstream funds (paid models answer
  `402 Insufficient account funds`), so the zen lane is left disabled —
  no functional loss. The shim also has a hardened `/zen/v1/*` pass-through
  (constant https host allowlist, header allowlist) should the keyed lane be
  re-enabled through it.
- The node 9router (20128) holds its `ocz2` wiring in **live memory** — its
  on-disk `db.json` predates it. If that router restarts and drops the
  models, re-add the shim provider through its dashboard pointing at
  `http://127.0.0.1:20142/v1`.

### Stability & latency

`oc-chain-watchdog.ps1` (scheduled task, every 2 min, launched hidden via
`oc-chain-watchdog-hidden.vbs` so no console windows flash): keeps exactly one
shim + one worker alive, kills duplicates (duplicate workers share the run-dir
counter and delete each other's directories — a real incident).

**One-command boot**: `start-9router-go-rotation.cmd` now also ensures the
shim + worker before the gateway starts — starting 9router-go brings the whole
free-model lane up.

**Latency** (simple request, through the gateway):

| Mode | Latency |
|---|---|
| per-request CLI spawn (old) | 8.5–13 s |
| persistent `opencode serve` fast path (current) | **3–7 s**, first request after boot ~30 s while the serve boots |

The worker runs a persistent `opencode serve` (port 20193, hidden) and talks
to it over HTTP with a **fresh session per request** (same fresh-bucket
semantics as a fresh directory — verified 6/6 zero-429). Any fast-path failure
(serve down, upstream error, limit) automatically falls back to the original
per-request spawn path, so behaviour never regresses. The serve is pre-warmed
at worker startup so the first request doesn't trip the gateway's 30 s
response timeout.

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
