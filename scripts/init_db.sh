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
echo "📝 Running migrations..."
echo "================================================"

# Get script directory
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
MIGRATIONS_DIR="$SCRIPT_DIR/../migrations"

# Run each migration in order
for migration in "$MIGRATIONS_DIR"/*.sql; do
    filename=$(basename "$migration")
    echo "⚙️  Running: $filename"
    psql "$DB_NAME" -f "$migration"
    if [ $? -eq 0 ]; then
        echo "✅ Completed: $filename"
    else
        echo "❌ Failed: $filename"
        exit 1
    fi
done

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
