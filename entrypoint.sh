#!/bin/sh
set -e

# ─── Auto-apply database migrations ─────────────────────
# Waits for PostgreSQL to accept connections, then runs any
# .up.sql files that haven't been applied yet.
#
# This script uses a simple tracking table `schema_migrations`
# to avoid re-running migrations on subsequent starts.

echo "⏳ Waiting for PostgreSQL to be ready..."

# Build the connection string from environment variables.
export PGHOST="${POSTGRES_HOST:-localhost}"
export PGPORT="${POSTGRES_PORT:-5432}"
export PGUSER="${POSTGRES_USER:-hintro}"
export PGPASSWORD="${POSTGRES_PASSWORD:-hintro_secret}"
export PGDATABASE="${POSTGRES_DB:-hintro_db}"

# Wait loop — retries for up to 30 seconds.
MAX_RETRIES=30
RETRY=0
until pg_isready -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" > /dev/null 2>&1; do
  RETRY=$((RETRY + 1))
  if [ "$RETRY" -ge "$MAX_RETRIES" ]; then
    echo "❌ PostgreSQL not ready after ${MAX_RETRIES}s — aborting"
    exit 1
  fi
  sleep 1
done

echo "✓ PostgreSQL is ready"

# Create the migration tracking table if it doesn't exist.
psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -c "
  CREATE TABLE IF NOT EXISTS schema_migrations (
    filename TEXT PRIMARY KEY,
    applied_at TIMESTAMPTZ DEFAULT NOW()
  );
" > /dev/null 2>&1

# Run each .up.sql migration in order.
MIGRATIONS_DIR="/app/migrations"
if [ -d "$MIGRATIONS_DIR" ]; then
  for f in "$MIGRATIONS_DIR"/*.up.sql; do
    [ -f "$f" ] || continue
    BASENAME=$(basename "$f")

    # Skip if already applied.
    APPLIED=$(psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -tAc \
      "SELECT 1 FROM schema_migrations WHERE filename = '$BASENAME';" 2>/dev/null)

    if [ "$APPLIED" = "1" ]; then
      echo "⏭  $BASENAME (already applied)"
    else
      echo "▶  Applying $BASENAME ..."
      psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -f "$f"
      psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -c \
        "INSERT INTO schema_migrations (filename) VALUES ('$BASENAME');" > /dev/null 2>&1
      echo "✓  $BASENAME applied"
    fi
  done
else
  echo "⚠  No migrations directory found at $MIGRATIONS_DIR"
fi

echo "🚀 Starting application..."

# Hand off to the main application binary.
exec server "$@"
