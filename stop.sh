#!/usr/bin/env bash
# ============================================================
#  STOP — shuts the shim + worker + serve down (Linux/macOS).
# ============================================================
pkill -f "oc-cli-api.js" 2>/dev/null && echo "[stop] shim stopped" || echo "[stop] shim was not running"
pkill -f "oc-cli-worker.js" 2>/dev/null && echo "[stop] worker stopped" || echo "[stop] worker was not running"
pkill -f "opencode serve" 2>/dev/null && echo "[stop] serve stopped" || true
echo "[stop] done."
