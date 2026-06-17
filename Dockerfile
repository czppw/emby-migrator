FROM golang:1.22-alpine AS build

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/emby-migrator ./cmd/server

FROM alpine:3.20

WORKDIR /app
COPY --from=build /out/emby-migrator /app/emby-migrator
COPY web /app/web
RUN mkdir -p /data /config

ENV EMBY_MIGRATOR_ADDR=:8787 \
    EMBY_MIGRATOR_DATA=/data \
    EMBY_MIGRATOR_CONFIG=/config \
    EMBY_MIGRATOR_PASSWORD=password \
    TZ=Asia/Shanghai

EXPOSE 8787
VOLUME ["/data", "/config"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD port="${EMBY_MIGRATOR_ADDR##*:}"; [ -n "$port" ] || port=8787; wget -qO- "http://127.0.0.1:${port}/api/health" >/dev/null || exit 1
ENTRYPOINT ["/app/emby-migrator"]
