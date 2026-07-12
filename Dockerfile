FROM golang:1.22-alpine AS build

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/emby-migrator ./cmd/server

FROM alpine:3.20

LABEL org.opencontainers.image.title="Emby Migrator" \
      org.opencontainers.image.description="Lightweight Docker Web tool for exporting and importing Emby metadata and artwork" \
      org.opencontainers.image.source="https://github.com/czppw/emby-migrator" \
      org.opencontainers.image.url="https://github.com/czppw/emby-migrator" \
      org.opencontainers.image.documentation="https://github.com/czppw/emby-migrator#readme" \
      org.opencontainers.image.licenses="AGPL-3.0-or-later" \
      org.opencontainers.image.authors="czppw / czppwa"

WORKDIR /app
COPY --from=build /out/emby-migrator /app/emby-migrator
COPY web /app/web
COPY LICENSE NOTICE /app/
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
