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

> **Panel source:** The React panel lives in the separate `webui` repo
> (`github.com/shinmentakezo07/webui`). `Dockerfile.railway` clones it at build
> time, because the root repo's `.gitignore` excludes the `ui/` directory. The
> Dockerfile pins a specific UI commit (`UI_COMMIT`) for reproducible builds.
> To deploy newer panel changes, update `UI_COMMIT` in `Dockerfile.railway` and
> push the root repo again.

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
