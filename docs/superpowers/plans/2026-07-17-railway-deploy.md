# Railway Single-Image Deployment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the CLIProxyAPI + management panel repo deploy to Railway as a single all-in-one container serving the panel and the API on one dynamic `PORT`, with no Go source changes.

**Architecture:** A new additive root `Dockerfile.railway` builds `ui/` (npm → single-file `dist/index.html`), bakes it as `/CLIProxyAPI/writable/static/management.html` into the Go binary's image, and runs the server via a `docker-entrypoint.sh` that writes `port: $PORT` into `config.yaml` at startup (the server only reads `config.port`; no `PORT` env exists in code). config comes from `CONFIG_YAML` (base64) env, falling back to a bundled safe-mode example. Existing `docker-compose.yml` and per-context Dockerfiles are untouched.

**Tech Stack:** Docker multi-stage build; Node 20 + npm (panel); Go 1.26 CGO (server); Debian bookworm runtime; Railway `railway.toml` config-as-code; POSIX sh entrypoint.

## Global Constraints

- Do NOT modify any Go source under `CLIProxyAPI/internal/`, `CLIProxyAPI/cmd/`, `CLIProxyAPI/sdk/`. The port requirement is met by the runtime entrypoint editing `config.yaml`, never by code changes. (AGENTS.md: keep changes small, don't touch translator/core.)
- Verbatim Go build flags (copied from `CLIProxyAPI/Dockerfile`): `CGO_ENABLED=1 GOOS=linux go build -buildvcs=false -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" -o ./CLIProxyAPI ./cmd/server/`.
- Panel build (verbatim from `ui/Dockerfile`): base `node:20-bookworm`; `COPY package.json package-lock.json ./`; `RUN npm ci`; `COPY . .`; `RUN npm run build` → `dist/index.html` (single inlined file via `vite-plugin-singlefile`).
- Server binds `fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)` (`internal/api/server.go:417`); `cfg.Host` defaults `""` (all interfaces) (`internal/config/config.go:764`). So binding `0.0.0.0:$PORT` requires only `port: $PORT` in config.
- Padding path: `WRITABLE_PATH=/CLIProxyAPI/writable` → `managementasset.StaticDir()` returns `$WRITABLE_PATH/static` → panel served at `$WRITABLE_PATH/static/management.html` (baked into the image). (`internal/managementasset/updater.go:152`, `internal/api/server.go:525`).
- `disable-auto-update-panel: true` is the config default (`internal/config/config.go:778`) and means the server serves the **bundled** panel locally, no GitHub fetch. Good for an offline Railway container.
- Management access behind Railway's proxy: Gin trusts proxies by default and reads `X-Forwarded-For`, so `c.ClientIP()` returns the real browser IP → non-localhost browsers need `remote-management.allow-remote: true` + always-required `secret-key`. (`internal/api/handlers/management/handler.go:272-294`).
- Node installer is npm (lockfile `ui/package-lock.json` committed; CI uses `npm ci`). Match it — do NOT use bun.
- This is **additive**: do not edit `docker-compose.yml`, `ui/Dockerfile`, `ui/nginx.conf`, `CLIAPI/Dockerfile`, `CLIProxyAPI/.dockerignore`, or the root `.dockerignore` for `.gitignore`.
- Comments/strings in English (AGENTS.md).

## File Structure

- `webui/Dockerfile.railway` (create) — three-stage all-in-one image: panel build → Go build → final Debian runtime with baked panel. Railway's build target.
- `webui/docker-entrypoint.sh` (create) — POSIX sh: resolve `PORT`, materialize `config.yaml` from `CONFIG_YAML` or bundled fallback, force `port: $PORT`, `exec` the server.
- `webui/config.railway.example.yaml` (create) — minimal safe-mode starter config (host "", secret-key "", allow-remote true, disable-auto-update-panel true, placeholder api-keys).
- `webui/railway.toml` (create) — Railway config-as-code: build `Dockerfile.railway`, healthcheck `/healthz`, restart policy.
- `webui/RAILWAY.md` (create) — deploy guide.
- No existing files modified.

---

### Task 1: Safe-mode starter config

**Files:**
- Create: `webui/config.railway.example.yaml`

**Interfaces:**
- Produces: a valid YAML config string the entrypoint (Task 2) copies when `CONFIG_YAML` is unset. Must contain top-level `host`, `port` (overwritten at runtime), `remote-management.{allow-remote,secret-key,disable-auto-update-panel}`, `api-keys` (placeholder so the file is non-empty/valid), `auth-dir`.

- [ ] **Step 1: Create the starter config**

Create `webui/config.railway.example.yaml` with exact content:

```yaml
# Minimal starter config for Railway (single all-in-one image).
# The docker-entrypoint.sh overwrites `port:` with Railway's $PORT at startup.
# For real use, set CONFIG_YAML (base64-encoded config.yaml) in Railway Variables
# instead of relying on this safe-mode starter — it boots the panel + management API
# but leaves the proxy AI API disabled (example api-keys) until you configure real keys.

host: ""            # bind all interfaces (Railway reaches it via the proxy)
port: 8080          # overwritten by docker-entrypoint.sh to match $PORT
auth-dir: "/CLIProxyAPI/auths"

remote-management:
  allow-remote: true            # panel/API reached through Railway's public proxy (non-localhost)
  secret-key: ""                # MUST set a strong key via CONFIG_YAML; empty => panel won't authenticate
  disable-auto-update-panel: true   # serve the bundled panel only, never fetch from GitHub

# Placeholder example keys => proxy AI endpoints (v1/v1beta/...) start in "safe mode" (disabled).
# Replace with real keys via CONFIG_YAML to enable upstream providers.
api-keys:
  - "sk-example-replace-me"
```

- [ ] **Step 2: Validate the YAML parses**

Run: `python3 -c "import yaml,sys; yaml.safe_load(open('webui/config.railway.example.yaml')); print('ok')"`
Expected: `ok` (no traceback). If python3 isn't available, use `docker run --rm -v "$PWD/webui/config.railway.example.yaml:/c.yaml:ro" --entrypoint sh python:3-alpine -c 'import yaml; yaml.safe_load(open("/c.yaml")); print("ok")'` — but python3 is expected.

- [ ] **Step 3: Commit**

```bash
cd /teamspace/studios/this_studio/webui
git add config.railway.example.yaml
git commit -m "feat(railway): add safe-mode starter config for single-image deploy

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 2: Entrypoint port shim

**Files:**
- Create: `webui/docker-entrypoint.sh`

**Interfaces:**
- Consumes: `webui/config.railway.example.yaml` (Task 1) present in the image at `/CLIProxyAPI/config.railway.example.yaml`; env `PORT` (Railway), env `CONFIG_YAML` (optional base64).
- Produces: a runnable `/CLIProxyAPI/config.yaml` whose top-level `port:` equals `$PORT`, then `exec ./CLIProxyAPI --config /CLIProxyAPI/config.yaml`.

- [ ] **Step 1: Write the entrypoint**

Create `webui/docker-entrypoint.sh` with exact content:

```sh
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
```

- [ ] **Step 2: Make it executable**

Run: `chmod +x webui/docker-entrypoint.sh`
Verify: `ls -l webui/docker-entrypoint.sh` shows the execute bit set for owner.

- [ ] **Step 3: Smoke-test the shim logic locally (no Docker)**

Confirm the two port-edit branches behave. Run a quick inline simulation against the example file:

```sh
cd /teamspace/studios/this_studio/webui
# Case A: config has a port line -> overwritten to 9999
sh -c '
  PORT=9999; CONFIG_FILE=/tmp/caseA.yaml; EXAMPLE_FILE=config.railway.example.yaml
  cp "$EXAMPLE_FILE" "$CONFIG_FILE"
  if grep -qE "^port:[[:space:]]*[0-9]+" "$CONFIG_FILE"; then
    awk -v p="$PORT" "!done && \$0 ~ /^port:[[:space:]]*[0-9]+/ { sub(/^[^0-9]*port:[[:space:]]*[0-9]+/, \"port: \" p); done=1 } { print }" "$CONFIG_FILE" > "$CONFIG_FILE.tmp" && mv "$CONFIG_FILE.tmp" "$CONFIG_FILE"
  fi
  grep -E "^port:" "$CONFIG_FILE"
'
# Expected: port: 9999
```

Expected output: `port: 9999`.

Then case B (no port line → prepended):

```sh
sh -c '
  PORT=7777; CONFIG_FILE=/tmp/caseB.yaml
  printf "host: \"\"\nauth-dir: /CLIProxyAPI/auths\n" > "$CONFIG_FILE"
  if grep -qE "^port:[[:space:]]*[0-9]+" "$CONFIG_FILE"; then :; else
    printf "port: %s\n" "$PORT" | cat - "$CONFIG_FILE" > "$CONFIG_FILE.tmp" && mv "$CONFIG_FILE.tmp" "$CONFIG_FILE"
  fi
  head -1 "$CONFIG_FILE"
'
```

Expected output: `port: 7777`.

- [ ] **Step 4: Commit**

```bash
git add docker-entrypoint.sh
git commit -m "feat(railway): add entrypoint that writes port:\$PORT into config.yaml

The Go server reads its listen port only from config.yaml. No Go changes:
docker-entrypoint.sh materializes config.yaml (from CONFIG_YAML base64 or the
bundled safe-mode example) then forces top-level port:\$PORT before exec.

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 3: All-in-one Railway Dockerfile

**Files:**
- Create: `webui/Dockerfile.railway`

**Interfaces:**
- Consumes: `ui/package.json`, `ui/package-lock.json`, `ui/` source; `CLIProxyAPI/go.mod`, `CLIProxyAPI/go.sum`, `CLIProxyAPI/cmd/server/`; `webui/docker-entrypoint.sh` (Task 2); `webui/config.railway.example.yaml` (Task 1).
- Produces: image `cliproxy-railway` whose `WORKDIR /CLIProxyAPI` has the binary, baked `$WRITABLE_PATH/static/management.html`, the entrypoint, and the example config. `CMD ["./docker-entrypoint.sh"]`. `EXPOSE 8080`.

- [ ] **Step 1: Write the Dockerfile**

Create `webui/Dockerfile.railway` with exact content:

```dockerfile
# syntax=docker/dockerfile:1
#
# Railway all-in-one image: management panel + API served by a single Go process
# on one port (Railway's $PORT). Additive — does NOT affect the local
# docker-compose.yml 2-service setup or the per-context Dockerfiles.
#
# Stage 1: build the React SPA into a single inlined dist/index.html.
FROM node:20-bookworm AS panel-builder
WORKDIR /ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
COPY ui/ .
ARG VERSION=dev
ENV VERSION=${VERSION}
RUN npm run build

# Stage 2: build the Go server (flags copied verbatim from CLIProxyAPI/Dockerfile).
FROM golang:1.26-bookworm AS builder
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends build-essential git && rm -rf /var/lib/apt/lists/*
COPY CLIProxyAPI/go.mod CLIProxyAPI/go.sum ./
RUN go mod download
COPY CLIProxyAPI/ .
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=1 GOOS=linux go build -buildvcs=false \
    -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" \
    -o ./CLIProxyAPI ./cmd/server/

# Stage 3: runtime. Bake the panel; serve panel+API from one process on $PORT.
FROM debian:bookworm
RUN apt-get update && apt-get install -y --no-install-recommends tzdata ca-certificates && rm -rf /var/lib/apt/lists/*

RUN mkdir -p /CLIProxyAPI/writable/static /CLIProxyAPI/auths

COPY --from=builder /app/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI

# Bake the management panel at the path managementasset expects ($WRITABLE_PATH/static).
COPY --from=panel-builder /ui/dist/index.html /CLIProxyAPI/writable/static/management.html

# Bundled fallback config + the runtime port-shim entrypoint.
COPY config.railway.example.yaml /CLIProxyAPI/config.railway.example.yaml
COPY docker-entrypoint.sh /CLIProxyAPI/docker-entrypoint.sh
RUN chmod +x /CLIProxyAPI/docker-entrypoint.sh

WORKDIR /CLIProxyAPI

ENV WRITABLE_PATH=/CLIProxyAPI/writable
ENV TZ=Asia/Shanghai
RUN cp /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

# Railway's default PORT is 8080; the entrypoint forces the server to bind $PORT.
EXPOSE 8080

CMD ["/CLIProxyAPI/docker-entrypoint.sh"]
```

- [ ] **Step 2: Build the image from the repo root**

Run from `/teamspace/studios/this_studio/webui`:

```bash
docker build -f Dockerfile.railway -t cliproxy-railway:local .
```

Expected: completes with `naming to docker.io/library/cliproxy-railway:local`. If it fails on `npm ci`, the panel `ui/package-lock.json` is committed (verified earlier) so it should pass; a failure here means copy paths (`ui/` vs `CLIProxyAPI/`) are wrong — fix the `COPY` lines (they are relative to root context).

- [ ] **Step 3: Commit**

```bash
git add Dockerfile.railway
git commit -m "feat(railway): add all-in-one Dockerfile (panel baked into Go binary)

Single container for Railway: builds ui/ (npm) and CLIProxyAPI (Go, CGO),
bakes dist/index.html as management.html, runs via docker-entrypoint.sh on
Railway's \$PORT. Existing docker-compose 2-service setup is untouched.

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 4: End-to-end local verification of the image

**Files:**
- No new files. Exercises `webui/Dockerfile.railway`, `webui/docker-entrypoint.sh`, `webui/config.railway.example.yaml`.

**Interfaces:**
- Consumes: the built image `cliproxy-railway:local` (Task 3).

- [ ] **Step 1: Free any port and run with Railway-like env (no CONFIG_YAML → safe-mode fallback)**

If host 8080 is busy, reuse an alternate host port mapping (container still listens on its own `8080`). To truly prove `$PORT` is honored, run with a custom `PORT` and map it:

```bash
docker run -d --name cliproxy-railway-test -p 18080:18080 -e PORT=18080 cliproxy-railway:local
sleep 4
```

- [ ] **Step 2: Verify entrypoint wrote port:$PORT**

Run: `docker logs cliproxy-railway-test 2>&1 | grep -E "docker-entrypoint|API server started successfully"`
Expected: a line `docker-entrypoint: starting CLIProxyAPI on port 18080` AND `API server started successfully on: :18080`. This proves the server bound `$PORT` (not a hardcoded 8317).

- [ ] **Step 3: Verify panel + API + healthz on the one port**

```bash
curl -s -o /dev/null -w "healthz: %{http_code} type=%header{content-type}\n" http://localhost:18080/healthz
curl -s -o /dev/null -w "panel:   %{http_code} type=%header{content-type} bytes=%{size_download}\n" http://localhost:18080/management.html
curl -s -o /dev/null -w "mgmt404: %{http_code}\n" http://localhost:18080/v0/management/config
```

Expected:
- `healthz: 200 type=application/json; charset=utf-8`
- `panel: 200 type=text/html bytes=2386...` (the inlined SPA, served by the baked asset)
- `mgmt404: 404` (safe-mode fallback has `secret-key: ""` → management routes 404, same as the compose stack verified earlier — proves the route is reachable and the panel is the only difference)

- [ ] **Step 4: Prove CONFIG_YAML (base64) makes management API return non-404**

```bash
T=$(mktemp -d)
cat > "$T/c.yaml" <<'YAML'
host: ""
port: 8080
auth-dir: "/CLIProxyAPI/auths"
remote-management:
  allow-remote: true
  secret-key: "railway-key-test"
  disable-auto-update-panel: true
api-keys:
  - "sk-railway-real"
YAML
ENC=$(base64 -w0 "$T/c.yaml")
docker rm -f cliproxy-railway-test >/dev/null 2>&1
docker run -d --name cliproxy-railway-test -p 18080:18080 -e PORT=18080 -e CONFIG_YAML="$ENC" cliproxy-railway:local
sleep 4
# No-key must be 401 (route exists), with-key 200 (proves CONFIG_YAML flowed through).
echo -n "no key: "; curl -s -o /dev/null -w "%{http_code}\n" http://localhost:18080/v0/management/config
echo -n "w/key:  "; curl -s -o /dev/null -w "%{http_code} bytes=%{size_download}\n" -H "Authorization: Bearer railway-key-test" http://localhost:18080/v0/management/config
docker rm -f cliproxy-railway-test >/dev/null 2>&1
rm -rf "$T"
```

Expected:
- `no key: 401`
- `w/key:  200 bytes=<nonzero>`

- [ ] **Step 5: Commit (nothing to stage — verification only; skip if tree clean)**

If `git status` is clean, no commit. Otherwise commit any scratch. (Expected: clean.)

---

### Task 5: Railway config-as-code + deploy guide

**Files:**
- Create: `webui/railway.toml`
- Create: `webui/RAILWAY.md`

**Interfaces:**
- Consumes: `webui/Dockerfile.railway` (Task 3), image behavior verified in Task 4.
- Produces: `railway.toml` (Railway reads `dockerfilePath = "Dockerfile.railway"` + `/healthz` healthcheck) and a deploy doc.

- [ ] **Step 1: Write `railway.toml`**

Create `webui/railway.toml` with exact content:

```toml
# Railway config-as-code for the single all-in-one CLIProxyAPI image.
# Railway builds Dockerfile.railway (panel + API on one process) and routes its
# public domain to the port the app listens on (env PORT, default 8080).
# docker-entrypoint.sh forces the Go server to bind $PORT.

[build]
builder = "DOCKERFILE"
dockerfilePath = "Dockerfile.railway"

[deploy]
healthcheckPath = "/healthz"
healthcheckTimeout = 30
restartPolicyType = "ON_FAILURE"
restartPolicyMaxRetries = 5
```

- [ ] **Step 2: Write `RAILWAY.md`**

Create `webui/RAILWAY.md` with exact content:

````markdown
# Deploying to Railway

This repo deploys to Railway as a **single all-in-one container**: the React management
panel is baked into the Go server image and served at `/management.html` on the **same
port** as the API. Railway builds `Dockerfile.railway`, runs one process, and routes its
public URL to Railway's `PORT` (default `8080`). `docker-entrypoint.sh` writes
`port: $PORT` into `config.yaml` at startup so the server binds Railway's port.

> The local 2-service `docker-compose.yml` (nginx panel + backend) is unchanged and is
> for local/dev use only — Railway cannot run docker-compose multi-container.

## 1. Create the service

- New Project → deploy from this GitHub repo. Railway detects `Dockerfile.railway`
  via `railway.toml` and builds it.

## 2. Provide configuration via `CONFIG_YAML` (base64)

The server reads its config from `config.yaml`. On Railway, set a **`CONFIG_YAML`**
variable to a base64-encoded `config.yaml`. The entrypoint decodes it at startup.

Build one locally:

```bash
cat > /tmp/config.yaml <<'YAML'
host: ""
auth-dir: "/CLIProxyAPI/auths"
remote-management:
  allow-remote: true                 # reached through Railway's public proxy (non-localhost)
  secret-key: "REPLACE-WITH-A-STRONG-KEY"
  disable-auto-update-panel: true    # serve the bundled panel only
api-keys:
  - "sk-YOUR-REAL-PROXY-KEY"
# Add provider credentials / auth files as needed via the panel once it's up.
YAML
base64 -w0 /tmp/config.yaml   # paste the output as the CONFIG_YAML variable value
```

If `CONFIG_YAML` is unset, the image boots a safe-mode starter
(`config.railway.example.yaml`): panel + `/healthz` run and Railway's healthcheck
passes, but the proxy AI API is disabled (example keys) and the management API needs a
`secret-key` set — so set `CONFIG_YAML`.

## 3. (Optional) Persistent OAuth credentials

OAuth credential files are written under `auth-dir` (`/CLIProxyAPI/auths`). To persist
them across redeploys, add a Railway **Volume** mounted at `/CLIProxyAPI/auths`. Without
a volume, OAuth flows re-run on each fresh deploy.

## 4. (Optional) Postgres/cloud config backend

For a remote config store instead of `CONFIG_YAML`, set `DEPLOY=cloud` plus the
`PGSTORE_*` (or `GITSTORE_*` / `OBJECTSTORE_*`) variables — see CLIProxyAPI's
`.env.example` / `.env.cluster.example`. This is optional; `CONFIG_YAML` is the simple
path.

## 5. Open the panel

Once deployed, open `https://<your-app>.up.railway.app/management.html` and log in with
the `secret-key` you set. The panel auto-detects its backend base from `window.location`
(same origin) and talks to `/v0/management` with no configuration.

## Healthcheck

`/healthz` returns `{"status":"ok"}` once the server is up. `railway.toml` uses it as
the healthcheck path.
````

- [ ] **Step 3: Lint the TOML**

Run: `python3 -c "import tomllib; tomllib.load(open('webui/railway.toml','rb')); print('toml ok')"` (Python 3.11+). If older: skip (no tomllib) — the file is trivial.
Expected: `toml ok`.

- [ ] **Step 4: Commit**

```bash
git add railway.toml RAILWAY.md
git commit -m "feat(railway): add railway.toml config-as-code + deploy guide

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 6: Push to the root repo

**Files:**
- None (git operations only).

- [ ] **Step 1: Confirm clean working tree besides planned work**

Run: `cd /teamspace/studios/this_studio/webui && git status --short`
Expected: only the files added in Tasks 1–5 are clean/committed; no stray files (e.g. no `ui/` changes leaked, since `ui/` is a separate repo and gitignored in the root repo).

- [ ] **Step 2: Push**

Run from `/teamspace/studios/this_studio/webui`:
```bash
git log --oneline origin/main..HEAD
git push origin main
```
Expected: pushes Tasks 1–5 commits (`origin/main..HEAD` lists 4–5 commits: config, entrypoint, Dockerfile, railway.toml+guide, plus the spec commit `d8ab074` if not already pushed). If the spec commit isn't yet on origin, include it.

- [ ] **Step 3: After Railway builds — manual cloud check (user)**

User confirms on Railway: build succeeds, `/healthz` green, `/management.html` loads, `CONFIG_YAML` applied. (Not automated here.)

---

## Self-Review Notes

-Spec coverage: spec "new files" (Dockerfile.railway, docker-entrypoint.sh, config.railway.example.yaml, railway.toml, RAILWAY.md) → Tasks 1,2,3,5. Spec "data flow" + "verification" → Task 4. Spec "error handling" (missing/invalid CONFIG_YAML, port mismatch) → Task 2 (fallback) + Task 4 (safe-mode 404, non-404 with CONFIG_YAML). Spec "out of scope" (no Go changes, untouched compose) → Global Constraints. All sections covered.
-Placeholder scan: every step has verbatim file content or an exact command + expected output. No TODO/TBD.
-Type/name consistency: `config.railway.example.yaml` and `CONFIG_YAML` used identically across Tasks 1–5; `Dockerfile.railway` referenced consistently; `PORT=18080` used consistently in Task 4.
-Fixed: Task 4 Step 1 originally mentioned "container still listens on its own 8080" then mapped 18080 — reconciled so the run passes `-e PORT=18080` and the container truly binds 18080 (proven by Step 2's log assertion `:18080`).
