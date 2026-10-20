# syntax=docker/dockerfile:1
FROM golang:1.27.2-alpine AS dependencies
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
COPY skills ./skills
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/schedulerse ./cmd/schedulerse

FROM build AS test
RUN go test -race -timeout 60s ./...

FROM alpine:3.24 AS runtime
LABEL org.opencontainers.image.licenses="MIT"
LABEL org.opencontainers.image.source="https://github.com/fixtse/SchedulerSE"
LABEL org.opencontainers.image.description="SchedulerSE: lightweight scheduling with authenticated job management"
RUN apk add --no-cache ca-certificates \
    && addgroup -g 1000 schedulerse && adduser -D -u 1000 -G schedulerse schedulerse \
    && mkdir -p /data /app && chown schedulerse:schedulerse /data
COPY --from=build /out/schedulerse /app/schedulerse
COPY LICENSE /usr/share/licenses/schedulerse/LICENSE
COPY internal/adapters/web/vendor/htmx-LICENSE /usr/share/licenses/htmx/LICENSE
ENV DATA_DIR=/data LISTEN_ADDR=:8789
USER 1000:1000
EXPOSE 8789
HEALTHCHECK --interval=60s --timeout=5s --start-period=5s --retries=3 CMD ["/app/schedulerse", "healthcheck"]
ENTRYPOINT ["/app/schedulerse"]
CMD ["serve"]
