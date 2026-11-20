#!/usr/bin/env bash
set -euo pipefail

echo "⚡ Installing FreeGPU (Colab GPU Terminal for OpenCode)..."

if ! command -v go >/dev/null 2>&1; then
  echo "❌ Go (1.22+) is required to build from source. Or download FreeGPU.dmg from GitHub Releases."
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

make app

INSTALL_DIR="${HOME}/.local/bin"
mkdir -p "$INSTALL_DIR"
cp freegpu "${INSTALL_DIR}/freegpu"
chmod +x "${INSTALL_DIR}/freegpu"

if [[ "$(uname -s)" == "Darwin" && -d "/Applications" ]]; then
  rm -rf "/Applications/FreeGPU.app" 2>/dev/null || true
  cp -R FreeGPU.app "/Applications/FreeGPU.app" 2>/dev/null && \
    echo "✅ Installed /Applications/FreeGPU.app" || \
    echo "ℹ️  Built ./FreeGPU.app (drag to /Applications if desired)"
fi

./freegpu opencode >/dev/null 2>&1 || true

echo "✅ Installed ${INSTALL_DIR}/freegpu & configured OpenCode"
echo ""
echo "Launch FreeGPU:"
echo "  open FreeGPU.app"
