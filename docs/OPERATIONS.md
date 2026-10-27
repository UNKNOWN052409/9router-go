# Operations runbook

## Start / stop

```powershell
# start (order does not matter, but worker first is cleaner)
Start-Process -WindowStyle Hidden node -ArgumentList "D:\Projects\oc-free-tier-shim\scripts\oc-cli-worker.js"
Start-Process -WindowStyle Hidden node -ArgumentList "D:\Projects\oc-free-tier-shim\scripts\oc-cli-api.js"

# stop — and verify exactly ONE of each remains
Get-CimInstance Win32_Process -Filter "Name='node.exe'" |
  Where-Object { $_.CommandLine -match 'oc-cli-worker|oc-cli-api' } |
  ForEach-Object { Stop-Process -Id $_.ProcessId -Force }
```

> **Single-worker rule.** The run-dir counter restarts at `r1` per process.
> Two workers → two jobs share one run dir → the first to finish deletes the
> dir under the second's feet. Always kill-all before restarting.

## Verify health

```powershell
curl http://127.0.0.1:20142/health        # {"ok":true,...}
curl http://127.0.0.1:20142/v1/models
# port listening?
netstat -ano | findstr :20142
```

## Logs

| Log | Content |
|---|---|
| `D:\scripts\oc-worker.log` | per-job: run dir, model, attach count, retries, errors, done |
| `D:\scripts\oc-cli-api.log` | per-request: prompt chars, stream/tools flags, images attached |

Large logs can be archived into `D:\scripts\logs\` — the worker recreates
them on start.

## Run the test battery

```bash
node tests/smoke.mjs
```

Covers: health, text round-trip, tool calling, vision, streaming, and a
5-request limit-reset battery (asserts zero limit errors).

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `File not found: attachment-1.png` | duplicate worker deleted the run dir, or CLI given a relative `--file` | ensure exactly one worker; attachment paths must be absolute (worker already does this) |
| CLI hangs / 401 every request | `opencode.json` inside the run dir cwd | keep run dirs config-free; model goes on argv only |
| `invalid_api_key` | stale opencode login or key env | re-run `opencode auth login`; check `OPENCODE_API_KEY` |
| `timeout waiting for worker` | worker dead, or CLI stuck | check `oc-worker.log`; kill+restart single worker |
| `{"_tag":"BadRequest"}` from CLI | malformed image file sent as attachment | validate the client's data-URL payload (a truncated base64 PNG reproduces this) |
| upstream `Unexpected server error` on some free models | provider-side outage (seen 2026-10-27 on deepseek-v4 / muse-spark / jev-1.13) | retry later; verify by calling the CLI directly, bypassing the shim |
| two "worker started" lines in log at restart | double launch | kill all workers, start one |

## Changing models

Edit the literal `MODEL_RUN_ARGS` table in `scripts/oc-cli-worker.js`
(key = public model name, value = constant argv). Nothing else needs to
change; the shim passes the model name through verbatim.
