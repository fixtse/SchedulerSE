# Build and verification targets are for GitHub Actions, not the deployment server.
.PHONY: format format-check check test build compose-check
format:
	gofmt -w cmd internal
format-check:
	@test -z "$$(gofmt -l cmd internal)" || (gofmt -l cmd internal; exit 1)
check:
	go vet ./...
test:
	# Vet runs separately in the check target. Keep race detection and Go test caching.
	go test -vet=off -race -timeout 60s ./...
build:
	mkdir -p bin
	CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o bin/schedulerse ./cmd/schedulerse
compose-check:
	docker compose config --no-env-resolution --quiet
