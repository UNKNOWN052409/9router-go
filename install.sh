#!/usr/bin/env bash
# ============================================================
#  INSTALL — one-time setup for Linux/macOS (Render, VPS, etc.)
#
#  - Checks Node.js 18+
#  - AUTO-INSTALLS the opencode CLI if missing (no manual step;
#    you bring your own account: run `opencode auth login` once)
#  - Creates the data directory (override with OC_DATA_DIR)
# ============================================================
set -e

echo "[install] checking Node.js..."
if ! command -v node >/dev/null 2>&1; then
  echo "[install] FAIL: Node.js not found. Install Node 18+ first."; exit 1
fi
node --version

echo "[install] checking opencode CLI..."
if ! command -v opencode >/dev/null 2>&1; then
  echo "[install] not found — auto-installing opencode-ai (npm global)..."
  npm install -g opencode-ai
else
  opencode --version 2>/dev/null || true
fi

echo "[install] IMPORTANT: bring your own account."
echo "  Run once:  opencode auth login"
echo "  (choose the free/no-auth tier; no key is bundled with this project)"

DATA_DIR="${OC_DATA_DIR:-$HOME/.oc-free-tier-shim}"
echo "[install] creating data directories at $DATA_DIR ..."
mkdir -p "$DATA_DIR/jobs" "$DATA_DIR/partials" "$DATA_DIR/work" "$DATA_DIR/attachments"

echo ""
echo "[install] DONE. Next:"
echo "  1. ./start.sh"
echo "  2. Point any OpenAI client at http://127.0.0.1:20142/v1"
echo "     (model example: glm-5.3-flash-free)"
