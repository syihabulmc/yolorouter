#!/bin/sh
set -e

CONFIG_DIR="/yolorouter/configs"
CONFIG_FILE="$CONFIG_DIR/config.yaml"

# Generate config from environment variables on every start.
# This ensures all 5 Railway instances produce identical config
# (same provider_master_key, same DB connection) regardless of
# individual volume state.
#
# Required env vars (set in Railway):
#   DATABASE_URL          — auto-injected by Railway PostgreSQL service
#   PROVIDER_MASTER_KEY   — generate once, set on ALL 5 instances
#
# Generate a key: openssl rand -base64 32

if [ -z "$DATABASE_URL" ]; then
  echo "ERROR: DATABASE_URL is not set. Connect a Railway PostgreSQL service." >&2
  exit 1
fi

if [ -z "$PROVIDER_MASTER_KEY" ]; then
  echo "ERROR: PROVIDER_MASTER_KEY is not set. Generate with: openssl rand -base64 32" >&2
  echo "Then set it as a variable on ALL 5 Railway services." >&2
  exit 1
fi

mkdir -p "$CONFIG_DIR"

# Parse DATABASE_URL: postgresql://user:password@host:port/dbname?sslmode=...
DB_USER=$(echo "$DATABASE_URL" | sed -E 's|.*://([^:]+):.*|\1|')
DB_PASS=$(echo "$DATABASE_URL" | sed -E 's|.*://[^:]+:([^@]+)@.*|\1|')
DB_HOST=$(echo "$DATABASE_URL" | sed -E 's|.*@([^:/]+).*|\1|')
DB_PORT=$(echo "$DATABASE_URL" | sed -E 's|.*:([0-9]+)/.*|\1|')
DB_NAME=$(echo "$DATABASE_URL" | sed -E 's|.*/([^?]+).*|\1|')
DB_SSL=$(echo "$DATABASE_URL" | grep -o 'sslmode=[^&]*' | cut -d= -f2)
[ -z "$DB_SSL" ] && DB_SSL="require"

cat > "$CONFIG_FILE" <<EOF
database:
  driver: postgres
  host: "$DB_HOST"
  port: $DB_PORT
  user: "$DB_USER"
  password: "$DB_PASS"
  dbname: "$DB_NAME"
  sslmode: $DB_SSL
security:
  provider_master_key: "$PROVIDER_MASTER_KEY"
EOF

exec yolorouter serve
