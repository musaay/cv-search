# 🚂 Railway Deployment Guide

This guide will help you deploy CV Search & GraphRAG to Railway.app.

## Prerequisites

- Railway.app account (sign up at https://railway.app)
- GitHub account (to connect repository)
- API Keys:
  - OpenAI API key (for embeddings)
  - Groq API key (optional, for LLM)

## Step 1: Install Railway CLI

```bash
# macOS
brew install railway

# Or using npm
npm install -g @railway/cli

# Login
railway login
```

## Step 2: Create New Project

```bash
# In project directory
cd /path/to/cv-search

# Initialize Railway project
railway init
```

Or use Railway Dashboard:
1. Go to https://railway.app/new
2. Click "Deploy from GitHub repo"
3. Connect your GitHub account and select the repository

## Step 3: Add PostgreSQL Database

In Railway Dashboard:
1. Click "New" → "Database" → "Add PostgreSQL"
2. Railway will automatically create `DATABASE_URL` environment variable
3. The database will be linked to your service

## Step 4: Set Environment Variables

In Railway Dashboard → Variables tab, add:

```bash
# Required
OPENAI_API_KEY=sk-proj-...
LLM_PROVIDER=groq
LLM_MODEL=llama-3.3-70b-versatile
GROQ_API_KEY=gsk_...
USE_LLM=true

# Optional (with defaults)
PORT=8080
UPLOADS_DIR=/app/uploads
CACHE_TTL_MINUTES=5
DEFAULT_TOP_N=10
BM25_WEIGHT=0.3
VECTOR_WEIGHT=0.4
GRAPH_WEIGHT=0.3
```

**Note**: `DATABASE_URL` is automatically set by Railway PostgreSQL plugin.

## Step 5: Run Database Migration

Railway Dashboard (Recommended):
1. Go to your PostgreSQL service in Railway
2. Click **"Query"** tab
3. Open `migrations/complete_setup.sql` from your local project
4. Copy ALL content and paste into Query tab
5. Click **Execute** (or press Cmd+Enter)
6. You should see: "CREATE TABLE", "CREATE INDEX", "CREATE EXTENSION" messages

This single file creates:
- ✅ Candidates table (with full-text search)
- ✅ CV files & entities tables
- ✅ Graph nodes & edges (with vector embeddings)
- ✅ Communities tables (Leiden algorithm)
- ✅ Candidate scoring table
- ✅ pgvector extension

Alternative (Railway CLI):
```bash
# Connect to PostgreSQL
railway connect postgres

# Run complete setup
\i migrations/complete_setup.sql
\q
```

## Step 6: Deploy

```bash
# Deploy via CLI
railway up

# Or push to GitHub (if connected)
git push origin main
```

Railway will:
1. Build Docker image using `Dockerfile`
2. Run the container
3. Expose the service on a public URL

## Step 7: Verify Deployment

```bash
# Get deployment URL
railway open

# Or check in dashboard
# You should see: https://your-app.railway.app
```

Test endpoints:
```bash
# Health check
curl https://your-app.railway.app/health

# Swagger docs
open https://your-app.railway.app/swagger/index.html

# Test search
curl -X POST https://your-app.railway.app/api/search/hybrid \
  -H "Content-Type: application/json" \
  -d '{"query": "Senior developer", "final_top_n": 5}'
```

## Troubleshooting

### Build Fails
- Check `railway logs` for errors
- Verify Dockerfile is correct
- Ensure all dependencies in `go.mod`

### Database Connection Error
- Verify `DATABASE_URL` is set
- Check PostgreSQL service is running
- Run migrations manually

### Out of Memory
- Increase memory in `railway.json`:
  ```json
  {
    "deploy": {
      "memory": 1024
    }
  }
  ```

### pgvector Extension Missing
```sql
-- Connect to database and run:
CREATE EXTENSION IF NOT EXISTS vector;
```

## Monitoring

```bash
# View logs
railway logs

# Check status
railway status

# Open dashboard
railway open
```

## Costs

- **Starter Plan**: $5/month
  - 512MB RAM, 1GB disk
  - Good for testing

- **Developer Plan**: $20/month
  - 8GB RAM, 100GB disk
  - Suitable for production

## Environment-Specific Tips

### Use Groq (Free LLM)
- Set `LLM_PROVIDER=groq`
- Get free API key from https://console.groq.com
- 30 requests/minute limit (good for small apps)

### Use OpenAI (Paid, Better)
- Set `LLM_PROVIDER=openai`
- Set `LLM_MODEL=gpt-4o-mini` (cheaper)
- Higher rate limits, better quality

## Scaling

For production use:
1. Increase replicas in `railway.json`
2. Add Redis for caching (Railway Redis plugin)
3. Monitor with Railway metrics
4. Set up custom domain

## Next Steps

- [ ] Set up custom domain
- [ ] Configure CI/CD
- [ ] Add monitoring/logging
- [ ] Set up backup strategy
- [ ] Configure autoscaling

---

**Built with ❤️ for Railway.app**
