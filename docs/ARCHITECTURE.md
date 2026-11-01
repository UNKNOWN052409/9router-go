# Architecture

## Components

### 1. `reset-lane/shim/oc-cli-api.js` — OpenAI-compatible shim (HTTP front-end)

- Binds `127.0.0.1:20142`. No child processes.
- Translates one `POST /v1/chat/completions` into one job file
  `<data_root>\jobs\<id>.job` containing
  `{ id, prompt, model, attach }`.
- **Prompt construction**: system/assistant/user/tool turns are serialised
  into a single plain-text prompt with `[System]` / `[Assistant]` / `[User]`
  markers; tool definitions are appended under
  `===AVAILABLE_TOOLS=== … ===END_TOOLS===` sentinels with a strict
  "reply with exactly one JSON object" protocol; the reply is parsed back
  into OpenAI `tool_calls`.
- **Reasoning extraction**: separates `<think>...</think>` blocks and
  `reasoning_content` from text output, allowing deep deliberation without
  confusing tool-call parsers or corrupting JSON argument shapes.
- **Image attachments**: data-URL image parts (`png|jpeg|gif|webp`) are
  decoded and written to `<data_root>\attachments\attachment-<n>.<ext>`
  (`n` = 1..4 per request; older files pruned at request start). The prompt
  lists the absolute paths; the CLI (an agent with file tools) reads them.
  The worker also passes them to the CLI via `--file` (see below).
- **Streaming**: when `stream:true`, the shim tails both the worker's text
  partial file (`<id>.txt`) and reasoning partial file (`<id>.reasoning.txt`).
  Reasoning deltas stream freely as `{ reasoning_content }` even during tool-calling
  turns, keeping the connection alive and preventing gateway/client 502 timeouts.
- Non-stream requests poll the result file up to 180 s.
- Response parsing strips markdown fences from `response_format:json_object`
  answers and attaches `reasoning_content` to completion messages.

### 2. `reset-lane/worker/oc-cli-worker.js` — file-queue job runner

- Polls the jobs dir every 700 ms; reads a job file then immediately unlinks
  it (so the sweep cannot double-run a job mid-CLI-run).
- **Limit reset core**: per job creates a fresh directory
  `<data_root>\work\r<counter>` and runs
  `opencode.exe run --format json --model <literal> [--file <abs>]` with
  `cwd` = that dir. The free tier meters per project dir/session → fresh
  bucket per request. The dir is deleted on exit.
- **Proxy rotation**: automatically loads residential/datacenter proxies from
  `proxies.txt`, `OC_PROXIES`, or `OC_PROXY` (supporting `host:port:user:pass`,
  `host:port`, and `http`/`socks5` URLs) and rotates round-robin on every attempt.
- **Attachment staging**: before spawn, copies
  `attachments\attachment-<i>.<ext>` into the run dir and appends
  `--file <absolute staged path>` (the CLI does not resolve bare relative
  `--file` names). Paths are worker-built (loop index + fixed ext whitelist).
- **Prompt transport**: stdin. Nothing user-supplied ever enters argv, cwd,
  env or a file path.
- **Model selection**: literal `MODEL_RUN_ARGS` table; whitelist chain in
  `selectArgv()`; unknown names fall back to `mimo-v2.6-flash-free`.
- **Retry**: up to 6 attempts, 2 s apart, on transient errors
  (`Invalid API key|FreeUsageLimit|429|timeout|rate`). Each retry = new CLI
  session = rotated proxy exit IP. No rotation on success.
- **Timeout**: dynamic timeout scaling based on prompt length, preventing premature
  kill on large generation passes.
- Streams CLI `text` and `reasoning` events into partial files (`<id>.txt`,
  `<id>.reasoning.txt`) for real-time SSE forwarding by the shim.

### 3. opencode CLI (external, not in repo)

`<npm-global>\opencode-ai\bin\opencode.exe`
(`opencode-ai@1.18.35`). Emits newline-delimited JSON events
(`step_start`, `text`, `step_finish`, `error`). Auth comes from the logged-in
profile via `HOME=<user-profile>`.

## Data flow (non-stream request)

```
POST /v1/chat/completions
  → saveImageAttachments() (0..4 files, pruned + indexed per request)
  → buildPrompt()          (turns + tool protocol + image refs)
  → submitJob()            (job file with validated id)
  → waitForResult()        (poll <id>.out.json, 500 ms, max 180 s)
  worker: sweep → unlink job → fresh run dir → stage attachments →
          spawn CLI → stdin prompt → collect text events → write result
  ← { text, error } → parseToolCall() → OpenAI response
```

## Environment invariants (do not break these)

1. Run dirs stay **config-free** — an `opencode.json` in cwd makes the CLI
   hang/401. Model selection is argv-only.
2. `HOME=<user-profile>` pinned in the spawn env (CLI auth resolution).
3. Prompt travels **stdin-only**; attachment argv paths are worker-built.
4. Job/partial/attachment names pass `^[a-z0-9][a-z0-9-]{0,63}$`; all
   derived paths are resolved-and-checked inside their directory.
5. Shim binds `127.0.0.1` only; `OPENCODE_API_KEY` masked in worker logs.
6. `oc-cli-worker.js` must run as **exactly one process** — two workers share
   the run-dir counter (`r1`, `r2`, …) and delete each other's directories
   mid-run. (This was a real incident: duplicate workers caused
   `File not found: attachment-1.png`.)

## Free-tier mechanics (what we learned)

- Quota is per **session/project directory**, not per IP: a burned session on
  a fresh IP still gets 429; a fresh project dir on the same IP works.
- Daily refill at UTC midnight; the fresh-dir trick makes that irrelevant
  per-request.
- A synthesized (curl/Go) request lands in a permanently-429 bucket; only a
  real CLI run is eligible. Hence this design spawns the genuine CLI.
