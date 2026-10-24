#!/usr/bin/env bash
# The project's test entry point, run by .github/workflows/ci.yml and locally.
# Must pass with no secrets, no network and no paid APIs (kenari is faked in tests).
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="$HOME/.local/go-root/go/bin:$PATH"

unformatted=$(gofmt -l cmd internal)
if [ -n "$unformatted" ]; then echo "gofmt needed:"; echo "$unformatted"; exit 1; fi
go vet ./...
go test -count=1 ./...
if command -v node >/dev/null; then node --check internal/admin/ui/app.js; fi
echo "ci ok"
