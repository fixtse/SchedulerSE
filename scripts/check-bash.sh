#!/bin/sh
# Check the service's actual execution environment; never install or reconfigure it.
set -eu

case "${1:-}" in
  --native)
    if [ "$#" -ne 2 ]; then
      echo "Usage: sh scripts/check-bash.sh --native /path/to/schedulerse" >&2
      exit 2
    fi
    exec "$2" check-bash
    ;;
  --help|-h)
    echo "Usage: sh scripts/check-bash.sh [--native /path/to/schedulerse]"
    echo "Default: check a temporary container using compose.yaml. No service settings are changed."
    exit 0
    ;;
  "") ;;
  *) echo "Unknown option. Use --help." >&2; exit 2 ;;
esac

if ! command -v docker >/dev/null 2>&1; then
  echo "Docker is not installed. For a native installation use:" >&2
  echo "  sh scripts/check-bash.sh --native /path/to/schedulerse" >&2
  exit 1
fi
if ! docker compose version >/dev/null 2>&1; then
  echo "Docker Compose is unavailable. Install Docker Compose, or use --native." >&2
  exit 1
fi

script_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
cd "$script_dir/.."
exec docker compose run --rm --no-deps -T --entrypoint /app/schedulerse schedulerse check-bash
