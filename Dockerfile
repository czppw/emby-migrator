FROM golang:1.22-alpine AS build

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/emby-migrator ./cmd/server

FROM alpine:3.20

RUN adduser -D -H -u 10001 app
WORKDIR /app
COPY --from=build /out/emby-migrator /app/emby-migrator
COPY web /app/web
RUN mkdir -p /data /config && chown -R app:app /data /config /app
USER app

ENV EMBY_MIGRATOR_ADDR=:8787 \
    EMBY_MIGRATOR_DATA=/data \
    EMBY_MIGRATOR_CONFIG=/config \
    EMBY_MIGRATOR_PASSWORD=password \
    TZ=Asia/Shanghai

EXPOSE 8787
VOLUME ["/data", "/config"]
ENTRYPOINT ["/app/emby-migrator"]
