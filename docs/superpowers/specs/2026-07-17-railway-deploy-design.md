# Railway Deployment — Single All-in-One Docker Image (panel + API on one port)

Date: 2026-07-17

## Context & Problem

Railway deploys **one service per repo**: it builds a single Dockerfile, runs one container, injects a dynamic `PORT` env var, and routes its public URL to that port. It does **not** run `docker compose` with multiple communicating containers.

The current root `docker-compose.yml` describes **two services** — `panel` (nginx, port 8317) and `backend` (Go server, internal 8317) — which Railway cannot run. Additionally, the Go server determines its listen port solely from `config.yaml`'s `port:` field (`internal/api/server.go:417`, `Addr: fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)`); there is **no `PORT` env var override** anywhere in the code. `Host` defaults to `""` (all interfaces, IPv4+IPv6) — good, Railway can reach it. So Railway can only route to the server if the runtime config's `port:` equals Railway's `$PORT`.

Separately, the goal "management panel on the same API port" is exactly what Railway wants: **one process serving `/management.html` + the full API on one port.** The upstream CLIProxyAPI supported this all-in-one pattern by baking the built SPA as `management.html` into the image (`internal/api/server.go:525` `GET /management.html` → `managementasset.FilePath()` → `$WRITABLE_PATH/static/management.html`). We previously removed that stage when we split into two services; for Railway we restore it as a **dedicated Railway image**, leaving the local 2-service compose untouched.

Remote-management access must also work behind Railway's proxy: `internal/api/handlers/management/handler.go:272-294` treats `127.0.0.1`/`::1` as "local"; Railway's router forwards `X-Forwarded-For`, and Gin trusts proxies by default, so `c.ClientIP()` returns the real browser IP → non-localhost browsers need `remote-management.allow-remote: true` (plus the always-required `secret-key`). Same as the docker-compose stack's documented behavior.

## Design

### Approach: one Railway Dockerfile, all-in-one image + entrypoint port shim

A new root **`Dockerfile.railway`** (Railway builds it explicitly so the local `docker-compose.yml`'s per-context Dockerfiles are untouched) that:

1. **Stage `panel-builder`** (Node 20): build `ui/` with `npm ci && npm run build` → `ui/dist/index.html` (single-file, all inlined).
2. **Stage `builder`** (golang:1.26): build `CLIProxyAPI/cmd/server` with CGO + version ldflags (same flags as `CLIProxyAPI/Dockerfile:32`).
3. **Stage final** (debian:bookworm): copy binary to `/CLIProxyAPI/CLIProxyAPI`; bake the panel → `/CLIProxyAPI/writable/static/management.html`; copy `config.example.yaml`; copy a small `docker-entrypoint.sh`; set `WRITABLE_PATH=/CLIProxyAPI/writable`, `TZ=Asia/Shanghai`; `EXPOSE 8080` (Railway's default `PORT`); `CMD ["./docker-entrypoint.sh"]`.

### Port shim — `docker-entrypoint.sh` (runtime, no Go changes)

The server reads `port:` from `config.yaml`. Rather than patch Go to read `PORT` (out of scope, and AGENTS.md says keep core changes small), the entrypoint writes `port: $PORT` into the active config before exec:

- If a bind-mounted `/CLIProxyAPI/config.yaml` exists (user provided secrets/config via Railway volumes), insert/overwrite its `port:` scalar with `$PORT` (default `8080`) using a minimal YAML scalar edit — mirror `internal/config`'s `SaveConfigPreserveCommentsUpdateNestedScalar` semantics at the shell level is overkill; instead, if the file is empty/missing, write a minimal valid config from a **`config.railway.example.yaml`** template with `port: ${PORT}` already set and required keys (`remote-management.secret-key`, `allow-remote`, a non-template `api-keys` entry).
- Prefer: support a base config provided via Railway env `CONFIG_YAML` (base64) written to `/CLIProxyAPI/config.yaml`, then force `port:` = `$PORT`.

Concretely the entrypoint logic (POSIX sh):
1. `PORT="${PORT:-8080}"`.
2. If `/CLIProxyAPI/config.yaml` does not exist → if `$CONFIG_YAML` is set, `echo "$CONFIG_YAML" | base64 -d > /CLIProxyAPI/config.yaml`; else copy `config.railway.example.yaml` to `config.yaml`.
3. Ensure `port: $PORT` is present/overwritten in `config.yaml` (awk/sed on the top-level `port:` line; if absent, prepend `port: $PORT`). This is a single top-level scalar, safe to edit with line substitution.
4. `exec ./CLIProxyAPI --config /CLIProxyAPI/config.yaml`.

### `railway.toml` (root) — tells Railway to build the Railway image

```toml
[build]
builder = "DOCKERFILE"
dockerfilePath = "Dockerfile.railway"

[deploy]
# App must listen on $PORT (default 8080); entrypoint enforces it.
healthcheckPath = "/healthz"
healthcheckTimeout = 30
restartPolicyType = "ON_FAILURE"
restartPolicyMaxRetries = 5
```

### New files

- `webui/Dockerfile.railway` — the three-stage all-in-one image above.
- `webui/docker-entrypoint.sh` — the port-shim entrypoint.
- `webui/config.railway.example.yaml` — minimal starter config with `host: ""`, `port: 8080` (overwritten at runtime), `remote-management: {allow-remote: true, secret-key: "", disable-auto-update-panel: true}`, a clearly-marked placeholder `api-keys` entry, no `auth-dir` surprises; instructs the user to set `secret-key` + real `api-keys` via Railway env `CONFIG_YAML` or a volume.
- `webui/railway.toml` — Railway config-as-code.
- `webui/RAILWAY.md` — short deploy guide: set `CONFIG_YAML` (or Railway Volume with config.yaml), set `secret-key`/`api-keys`, open the Railway public URL → `/management.html` (panel, same origin) and `/v1/...` (API). Document `allow-remote: true` requirement and `DEPLOY` (leave unset unless using Postgres/Home store).

### Files NOT changed

- `webui/docker-compose.yml`, `webui/Dockerfile` (none), `ui/Dockerfile`, `ui/nginx.conf`, `CLIProxyAPI/Dockerfile` — all left as-is. This is an **additive** Railway path; local 2-service dev and the existing pushable repos are unaffected.

### Why not (alternatives considered)

- **Run the 2-service compose on Railway** — not supported; Railway is single-container.
- **Patch Go to read `PORT` env** — out of scope / discouraged by AGENTS.md; the entrypoint shim achieves the same with zero Go changes.
- **Use Railway's "two services" by adding two Dockerfiles + two Railway services in one repo** — Railway supports multiple services per repo via `railway.json` services, but the panel+backend split needs the panel (nginx) to proxy to the backend over the Railway internal network, which is more fragile than the all-in-one single process; and it splits the public URL across two Railway services, losing the "same origin" goal. The all-in-one image keeps one public URL with panel + API same-origin — strictly better for Railway.

## Data flow

```
Railway router → $PORT (default 8080) → single container
  docker-entrypoint.sh writes port:$PORT into config.yaml, execs CLIProxyAPI
  Go server on 0.0.0.0:$PORT serves:
    GET /                -> API info JSON
    GET /management.html -> baked SPA (single-file)  ← management panel
    /v0/management/*     -> Management API (secret-key + allow-remote)
    /v1 /v1beta /openai/v1 /backend-api/codex -> AI provider API
    /{anthropic,codex,antigravity}/callback -> OAuth callbacks (reuse main port)
    /healthz             -> health (railway healthcheck)
```
Browser opens `https://<railway-app>.up.railway.app/management.html`; the SPA auto-detects its backend base from `window.location` (same origin) → connects to `/v0/management` with zero config, no CORS.

## Error handling

- `config.yaml` missing and no `CONFIG_YAML`: entrypoint writes `config.railway.example.yaml` → server starts in **example-API-key safe mode** (proxy API disabled, management API available once `secret-key` set) — logs the warning, serves panel + `/healthz`, so Railway healthcheck passes and the user can configure via the panel.
- Bad `CONFIG_YAML` (invalid YAML): server logs load error and exits non-zero → Railway restarts per `restartPolicy`. Document that `CONFIG_YAML` must be valid base64-encoded YAML.
- Port mismatch: entrypoint guarantees `port: $PORT`, so Railway's TCP probe reaches it; if `PORT` unset, `8080` matches Railway's default.

## Testing / verification

1. **Build locally:** `docker build -f Dockerfile.railway -t cliproxy-railway:local .` from `webui/` root.
2. **Run with Railway-like env:**
   `docker run --rm -p 8080:8080 -e PORT=8080 -e CONFIG_YAML="$(base64 -w0 config.railway.example.yaml)" cliproxy-railway:local`
   - `curl -s localhost:8080/healthz` → `{"status":"ok"}`.
   - `curl -s localhost:8080/` → SPA HTML (served by nginx in 2-svc; here served by backend at `/management.html`). `curl -sI localhost:8080/management.html` → 200 `text/html`.
   - `curl -s localhost:8080/v0/management/config -H "Authorization: Bearer <secret-key>"` → 200 JSON.
   - OAuth callback paths 200.
3. **Port shim test:** `docker run --rm -p 9090:9090 -e PORT=9090 ... && curl localhost:9090/healthz` → 200 (proves server bound `$PORT`, not a hardcoded 8317).
4. **Railway deploy:** push branch, Railway builds `Dockerfile.railway`, set `CONFIG_YAML` env (or Volume), open public URL `/management.html`. No local signature presence required.

## Out of scope

- Modifying Go to natively read `PORT`.
- Postgres/git/object-store cloud config (`DEPLOY=cloud` + `PGSTORE_*`). Documented in `RAILWAY.md` as optional; not wired by default.
- The local 2-service `docker-compose.yml` (untouched).
