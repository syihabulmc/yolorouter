#!/bin/sh
set -e

# Validate required env vars for Railway deployments.
# The Go binary reads these directly — no config file generation needed.
#
# Required env vars (set in Railway):
#   DATABASE_URL          — auto-injected by Railway PostgreSQL service
#   PROVIDER_MASTER_KEY   — generate once, set on ALL instances
#
# Generate a key: openssl rand -base64 32

if [ -z "$DATABASE_URL" ]; then
  echo "ERROR: DATABASE_URL is not set. Connect a Railway PostgreSQL service." >&2
  exit 1
fi

if [ -z "$PROVIDER_MASTER_KEY" ]; then
  echo "ERROR: PROVIDER_MASTER_KEY is not set. Generate with: openssl rand -base64 32" >&2
  echo "Then set it as a variable on ALL Railway services." >&2
  exit 1
fi

exec yolorouter serve
