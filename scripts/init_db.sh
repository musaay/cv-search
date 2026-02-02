#!/bin/bash
# CV Search & GraphRAG Database Initialization Script
# This script sets up all necessary database tables and indexes

set -e  # Exit on error

DB_NAME="${1:-cv_search}"

echo "🚀 Initializing CV Search database: $DB_NAME"
echo "================================================"

# Check if database exists
if psql -lqt | cut -d \| -f 1 | grep -qw "$DB_NAME"; then
    echo "✅ Database '$DB_NAME' exists"
else
    echo "📦 Creating database '$DB_NAME'..."
    createdb "$DB_NAME"
    echo "✅ Database created"
fi

echo ""
echo "📝 Running migration..."
echo "================================================"

# Get script directory
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
MIGRATION_FILE="$SCRIPT_DIR/../migrations/complete_setup.sql"

if [ ! -f "$MIGRATION_FILE" ]; then
    echo "❌ Migration file not found: $MIGRATION_FILE"
    exit 1
fi

echo "⚙️  Running: complete_setup.sql"
psql "$DB_NAME" -f "$MIGRATION_FILE"
if [ $? -eq 0 ]; then
    echo "✅ Migration completed successfully!"
else
    echo "❌ Migration failed"
    exit 1
fi

echo ""
echo "================================================"
echo "🎉 Database initialization complete!"
echo ""
echo "📊 Database summary:"
psql "$DB_NAME" -c "\dt" 2>/dev/null | grep -E "candidates|cv_|graph_|community" || echo "No tables found"
echo ""
echo "🔍 To verify pgvector extension:"
echo "   psql $DB_NAME -c '\\dx vector'"
echo ""
echo "🚀 Ready to start the API server!"
