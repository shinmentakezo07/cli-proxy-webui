#!/bin/sh
# docker-entrypoint.sh — Railway launch shim for CLIProxyAPI (all-in-one image).
# The Go server reads its listen port only from config.yaml (`cfg.Port`); it has no
# $PORT env support. This shim materializes config.yaml then forces `port: $PORT`
# so Railway's router can reach the container.

set -eu

PORT="${PORT:-8080}"
CONFIG_FILE="/CLIProxyAPI/config.yaml"
EXAMPLE_FILE="/CLIProxyAPI/config.railway.example.yaml"

# 1) Materialize config.yaml.
if [ -s "$CONFIG_FILE" ]; then
  : # user-mounted config wins; we still fix its port below.
elif [ -n "${CONFIG_YAML:-}" ]; then
  printf '%s' "$CONFIG_YAML" | base64 -d > "$CONFIG_FILE"
else
  cp "$EXAMPLE_FILE" "$CONFIG_FILE"
fi

# 2) Ensure a top-level `port: <PORT>` scalar is present and correct.
#    Replace the first top-level `^port:` line; if absent, prepend it.
if grep -qE '^port:[[:space:]]*[0-9]+' "$CONFIG_FILE"; then
  # In-place replace of the (first) top-level port scalar using awk.
  awk -v p="$PORT" '
    !done && $0 ~ /^port:[[:space:]]*[0-9]+/ { sub(/^[^0-9]*port:[[:space:]]*[0-9]+/, "port: " p); done=1 }
    { print }
  ' "$CONFIG_FILE" > "$CONFIG_FILE.tmp" && mv "$CONFIG_FILE.tmp" "$CONFIG_FILE"
else
  printf 'port: %s\n' "$PORT" | cat - "$CONFIG_FILE" > "$CONFIG_FILE.tmp" && mv "$CONFIG_FILE.tmp" "$CONFIG_FILE"
fi

# 3) Ensure host binds all interfaces (default empty already does; leave as-is).
# 4) Ensure auth dir exists.
mkdir -p /CLIProxyAPI/auths

echo "docker-entrypoint: starting CLIProxyAPI on port $PORT"
exec ./CLIProxyAPI --config "$CONFIG_FILE"
