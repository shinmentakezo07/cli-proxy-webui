#!/usr/bin/env bash
#
# build-panel.sh — build the local webui UI and install it as the bundled management
# control panel (static/management.html) that the server serves at GET /management.html.
#
# Default behavior: build ../ui (relative to the repo root) and copy its single-file
# dist/index.html into ./static/management.html. Override paths with env vars:
#   UI_DIR       directory of the React UI (default: <repo>/../ui)
#   STATIC_DIR   destination directory for management.html (default: <repo>/static)
#
set -euo pipefail

script_dir="$(cd "$(dirname "${0}")" && pwd)"
repo_dir="$(cd "${script_dir}/.." && pwd)"

ui_dir="${UI_DIR:-$(cd "${repo_dir}/../ui" && pwd)}"
static_dir="${STATIC_DIR:-${repo_dir}/static}"

echo "Building local management panel:"
echo "  UI dir:     ${ui_dir}"
echo "  Static dir: ${static_dir}"

if [[ ! -d "${ui_dir}" ]]; then
  echo "Error: UI directory not found at ${ui_dir}" >&2
  exit 1
fi

cd "${ui_dir}"

if command -v npm >/dev/null 2>&1; then
  if [[ ! -d node_modules ]]; then
    echo "Installing UI dependencies…"
    npm install
  fi
  echo "Building UI…"
  npm run build
elif command -v bun >/dev/null 2>&1; then
  if [[ ! -d node_modules ]]; then
    echo "Installing UI dependencies (bun)…"
    bun install
  fi
  echo "Building UI (bun)…"
  bun run build
else
  echo "Error: neither npm nor bun is available; install Node.js to build the panel." >&2
  exit 1
fi

dist_html="${ui_dir}/dist/index.html"
if [[ ! -f "${dist_html}" ]]; then
  echo "Error: build did not produce ${dist_html}" >&2
  exit 1
fi

mkdir -p "${static_dir}"
cp -f "${dist_html}" "${static_dir}/management.html"
echo "Installed management panel at ${static_dir}/management.html"
