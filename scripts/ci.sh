#!/usr/bin/env bash
# The project's test entry point, run by .github/workflows/ci.yml and locally.
# Must pass with no secrets, no network and no paid APIs (kenari is faked in tests).
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="$HOME/.local/go-root/go/bin:$PATH"

# The panel is built into the Go binary, so it is built first: a panel that
# does not compile must fail the build, not the service.
if command -v npm >/dev/null; then
  ( cd web && npm ci --no-audit --no-fund >/dev/null 2>&1 || npm install --no-audit --no-fund >/dev/null )
  ( cd web && npm run build >/dev/null )
fi

unformatted=$(gofmt -l cmd internal)
if [ -n "$unformatted" ]; then echo "gofmt needed:"; echo "$unformatted"; exit 1; fi
go vet ./...
go test -count=1 ./...
echo "ci ok"
