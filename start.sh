#!/usr/bin/env bash
# ============================================================
#  START — boots the free-model lane (Linux/macOS, Render, VPS).
#  Both processes run in the background; logs next to the scripts.
# ============================================================
DIR="$(cd "$(dirname "$0")/reset-lane" && pwd)"

is_running() { pgrep -f "$1" >/dev/null 2>&1; }

if is_running "oc-cli-api.js"; then
  echo "[start] shim already running"
else
  nohup node "$DIR/shim/oc-cli-api.js" >> "$DIR/oc-cli-api.out.log" 2>&1 &
  echo "[start] shim started (pid $!)"
fi

if is_running "oc-cli-worker.js"; then
  echo "[start] worker already running"
else
  nohup node "$DIR/worker/oc-cli-worker.js" >> "$DIR/oc-cli-worker.out.log" 2>&1 &
  echo "[start] worker started (pid $!)"
fi

# Router wiring (idempotent; skips itself when no 9router database exists),
# so bare model names resolve to the reset-shim lane without any manual step.
if [ -f "$DIR/tools/seed-router-combos.js" ]; then
  node "$DIR/tools/seed-router-combos.js" || echo "[start] WARN: combo seeding skipped"
fi

# Optional gateway start: point N9R_GATEWAY_START at the gateway's start script.
if [ -n "$N9R_GATEWAY_START" ] && [ -f "$N9R_GATEWAY_START" ]; then
  echo "[start] invoking gateway start script"
  bash "$N9R_GATEWAY_START" || true
fi

sleep 3
curl -s -m 5 http://127.0.0.1:20142/health || echo "[start] WARN: shim not responding yet (serve may still be booting)"
echo ""
echo "[start] done. Endpoint: http://127.0.0.1:20142/v1"
