# syntax=docker/dockerfile:1
#
# Backend-only image for CLIProxyAPI.
#
# The management control panel (webui/ui) is no longer baked into this binary.
# It is built separately as the `panel` image (see ../docker-compose.yml and
# ../ui/Dockerfile) and served by nginx on the same API port (8317), which
# reverse-proxies the management API, OAuth callbacks, and upstream AI API
# routes back to this backend container. This keeps the Go image small and free
# of a Node.js build stage, while presenting a single port to the browser.

# Stage: build the Go server.
FROM golang:1.26-bookworm AS builder

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends build-essential git && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./

RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=1 GOOS=linux go build -buildvcs=false -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" -o ./CLIProxyAPI ./cmd/server/

FROM debian:bookworm

RUN apt-get update && apt-get install -y --no-install-recommends tzdata ca-certificates && rm -rf /var/lib/apt/lists/*

RUN mkdir -p /CLIProxyAPI/writable

COPY --from=builder ./app/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI

# Bundled config template; mount a real config.yaml over this for production.
COPY config.example.yaml /CLIProxyAPI/config.example.yaml

WORKDIR /CLIProxyAPI

# Writable tree used by the Go server for runtime assets (not the panel here).
ENV WRITABLE_PATH=/CLIProxyAPI/writable

EXPOSE 8317

ENV TZ=Asia/Shanghai

RUN cp /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

CMD ["./CLIProxyAPI"]
