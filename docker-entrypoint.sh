#!/bin/sh
# docker-entrypoint.sh — Railway launch shim for CLIProxyAPI (all-in-one image).
# The Go server reads its listen port only from config.yaml (`cfg.Port`); it has no
# $PORT env support. This shim materializes config.yaml then forces `port: $PORT`
# so Railway's router can reach the container.

set -eu

PORT="${PORT:-8080}"
CONFIG_FILE="/CLIProxyAPI/config.yaml"
EXAMPLE_FILE="/CLIProxyAPI/config.railway.example.yaml"

# Inject the current $PORT into a YAML file's top-level `port:` scalar.
inject_port() {
  file="$1"
  if [ ! -f "$file" ]; then
    return
  fi
  if grep -qE '^port:[[:space:]]*[0-9]+' "$file"; then
    awk -v p="$PORT" '
      !done && $0 ~ /^port:[[:space:]]*[0-9]+/ { sub(/^[^0-9]*port:[[:space:]]*[0-9]+/, "port: " p); done=1 }
      { print }
    ' "$file" > "$file.tmp" && mv "$file.tmp" "$file"
  else
    printf 'port: %s\n' "$PORT" | cat - "$file" > "$file.tmp" && mv "$file.tmp" "$file"
  fi
}

# 1) Materialize config.yaml.
if [ -s "$CONFIG_FILE" ]; then
  : # user-mounted config wins; we still fix its port below.
elif [ -n "${CONFIG_YAML:-}" ]; then
  printf '%s' "$CONFIG_YAML" | base64 -d > "$CONFIG_FILE"
else
  cp "$EXAMPLE_FILE" "$CONFIG_FILE"
fi

# 2) Ensure a top-level `port: <PORT>` scalar is present and correct.
inject_port "$CONFIG_FILE"

# 3) Ensure host binds all interfaces (default empty already does; leave as-is).

# 4) Patch the Postgres bootstrap template so fresh PGSTORE_DSN databases
#    bootstrap with the Railway port (the server ignores --config when PGSTORE_DSN
#    is set, so the port injection above does not apply).
if [ -n "${PGSTORE_DSN:-}" ] && [ -f "/CLIProxyAPI/config.example.yaml" ]; then
  inject_port "/CLIProxyAPI/config.example.yaml"
fi

# 5) Ensure auth dir exists.
mkdir -p /CLIProxyAPI/auths

echo "docker-entrypoint: starting CLIProxyAPI on port $PORT"
exec ./CLIProxyAPI --config "$CONFIG_FILE"
