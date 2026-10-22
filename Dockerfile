# syntax=docker/dockerfile:1
FROM golang:1.27.3-alpine AS dependencies
WORKDIR /src
RUN apk add --no-cache build-base
COPY go.mod go.sum ./
# Keep module downloads and the expensive SQLite C compilation in exported layers.
# GitHub's cache exporter does not persist RUN cache mounts between runners.
RUN go mod download
RUN CGO_ENABLED=1 go build -trimpath github.com/mattn/go-sqlite3

FROM dependencies AS build
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/schedulerse ./cmd/schedulerse

FROM build AS test
RUN go test -race -timeout 180s ./...

FROM alpine:3.24 AS runtime
LABEL org.opencontainers.image.licenses="MIT"
LABEL org.opencontainers.image.source="https://github.com/fixtse/SchedulerSE"
LABEL org.opencontainers.image.description="SchedulerSE: lightweight scheduling with authenticated job management"
# Common tools for user-authored Bash automations (no compiler toolchain).
RUN apk add --no-cache \
        ca-certificates bubblewrap bash coreutils findutils grep sed gawk diffutils \
        curl wget jq openssl \
        tar gzip bzip2 xz zip unzip \
        bind-tools iproute2 netcat-openbsd \
        git openssh-client rsync util-linux \
    && addgroup -g 1000 schedulerse && adduser -D -u 1000 -G schedulerse schedulerse \
    && mkdir -p /data /app && chown schedulerse:schedulerse /data
COPY --from=build /out/schedulerse /app/schedulerse
COPY LICENSE /usr/share/licenses/schedulerse/LICENSE
COPY internal/adapters/web/vendor/htmx-LICENSE /usr/share/licenses/htmx/LICENSE
COPY internal/adapters/web/vendor/mcp-go-sdk/LICENSE /usr/share/licenses/mcp-go-sdk/LICENSE
ENV DATA_DIR=/data LISTEN_ADDR=:8789
USER 1000:1000
EXPOSE 8789
HEALTHCHECK --interval=60s --timeout=5s --start-period=5s --retries=3 CMD ["/app/schedulerse", "healthcheck"]
ENTRYPOINT ["/app/schedulerse"]
CMD ["serve"]
