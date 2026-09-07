#!/usr/bin/env bash
#
# One deliberate credentialed SSO login per service, with redacted output.
#
# Run this after filling in NTUST_SSO_USERNAME / NTUST_SSO_PASSWORD and before
# starting the service, so a typo is caught by one attempt rather than by the
# scheduler discovering it on a schedule.
#
# This performs a REAL login against NTUST. Repeated failures lock the account,
# which is why this script makes exactly one attempt per service and why the
# running service guards them behind a circuit breaker.

set -euo pipefail

cd "$(dirname "$0")/.."

if [[ ! -f .env ]]; then
  echo "No .env found. Copy .env.example to .env and fill in your credentials." >&2
  exit 2
fi

# Read the credential names only to fail early with a clear message; the values
# are never echoed, and the binary does the actual reading.
if ! grep -qE '^NTUST_SSO_USERNAME=.+' .env || ! grep -qE '^NTUST_SSO_PASSWORD=.+' .env; then
  echo "NTUST_SSO_USERNAME and NTUST_SSO_PASSWORD must be set in .env." >&2
  exit 2
fi

if command -v go >/dev/null 2>&1; then
  exec go run ./cmd/isntustdown -verify-sso
fi

if [[ -x bin/isntustdown ]]; then
  exec ./bin/isntustdown -verify-sso
fi

echo "Neither the Go toolchain nor bin/isntustdown is available." >&2
echo "Install Go, or run: docker compose run --rm app -verify-sso" >&2
exit 2
