# 🚀 Deployment Guide

This guide shows how to deploy CV Search & GraphRAG to **Railway.app** with **Neon PostgreSQL**.

## Prerequisites

- GitHub account
- Railway.app account (free tier available)
- Neon.tech account (free tier: 500MB storage)
- OpenAI API key (for embeddings)
- Groq API key (optional, for free LLM)

---

## Step 1: Database Setup (Neon)

### 1.1 Create Neon Project

1. Go to https://neon.tech
2. Sign in with GitHub
3. Click **New Project**
4. Name: `cv-search`
5. Region: Choose closest to your Railway region
6. Click **Create Project**

### 1.2 Get Connection String

1. In Neon Dashboard → **Connection string**
2. Copy the PostgreSQL connection string:
   ```
   postgresql://user:password@ep-xxx.region.aws.neon.tech/neondb?sslmode=require
   ```

### 1.3 Run Migration

From your local machine:

```bash
psql "your-neon-connection-string" -f migrations/complete_setup.sql
```

✅ This creates all tables, indexes, and enables pgvector extension.

---

## Step 2: Application Deployment (Railway)

### 2.1 Install Railway CLI

```bash
# macOS
brew install railway

# Or using npm
npm install -g @railway/cli

# Login
railway login
```

### 2.2 Initialize Project

```bash
cd /path/to/cv-search
railway init
```

Select workspace and create new project (e.g., `cv-search-production`).

### 2.3 Connect GitHub Repository

Railway Dashboard:
1. Go to your project
2. Click **+ New** → **GitHub Repo**
3. Select `your-username/cv-search`
4. Railway will auto-detect `Dockerfile` and start building

### 2.4 Set Environment Variables

```bash
railway service cv-search

# Database (Neon)
railway variables set DATABASE_URL="your-neon-connection-string"

# OpenAI (required for embeddings)
railway variables set OPENAI_API_KEY="sk-proj-..."

# LLM Provider (choose one)
railway variables set LLM_PROVIDER="groq"          # Free tier
railway variables set GROQ_API_KEY="gsk_..."
railway variables set LLM_MODEL="llama-3.3-70b-versatile"

# Or use OpenAI for LLM
# railway variables set LLM_PROVIDER="openai"
# railway variables set LLM_MODEL="gpt-4o-mini"

# Optional
railway variables set USE_LLM="true"
railway variables set PORT="8080"
railway variables set UPLOADS_DIR="/app/uploads"
```

### 2.5 Generate Public Domain

```bash
railway domain
```

You'll get a URL like: `https://cv-search-production.up.railway.app`

---

## Step 3: Verify Deployment

### Health Check

```bash
curl https://your-app.up.railway.app/health
```

Expected response:
```json
{"status":"healthy"}
```

### Swagger UI

Visit: `https://your-app.up.railway.app/swagger/index.html`

### Test Search

```bash
curl -X POST https://your-app.up.railway.app/api/search/hybrid \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Senior Python developer with 5 years experience",
    "final_top_n": 5
  }'
```

---

## Architecture

```
┌─────────────────┐
│   Railway.app   │
│  (Application)  │
│                 │
│ • Go 1.24       │
│ • Docker        │
│ • Port 8080     │
└────────┬────────┘
         │
         │ DATABASE_URL
         │
         ▼
┌─────────────────┐
│   Neon.tech     │
│  (PostgreSQL)   │
│                 │
│ • pgvector      │
│ • 500MB free    │
│ • Auto-suspend  │
└─────────────────┘
```

---

## Cost Breakdown

### Free Tier (Sufficient for small projects)

**Railway:**
- 512 MB RAM
- 1 GB Disk
- 100 GB Network/month
- **Cost:** $0/month (with trial credits)

**Neon:**
- 500 MB Storage
- Unlimited compute (auto-suspend after 5 min)
- 3 GB data transfer/month
- **Cost:** $0/month

**Total:** ~$0/month (free tier)

### Paid Plans (For production)

**Railway Developer Plan:**
- 8 GB RAM
- 100 GB Disk
- **Cost:** $20/month

**Neon Launch Plan:**
- 3 GB Storage
- Always active
- **Cost:** $19/month

**Total:** ~$39/month (production ready)

---

## Monitoring

### Railway Logs

```bash
railway logs --tail 100
```

### Neon Monitoring

1. Go to Neon Dashboard
2. Click on your project
3. **Monitoring** tab shows:
   - Query performance
   - Active connections
   - Storage usage

---

## Troubleshooting

### Application won't start

Check logs:
```bash
railway logs
```

Common issues:
- DATABASE_URL not set
- OPENAI_API_KEY missing
- Port not set (default 8080)

### Database connection error

1. Check DATABASE_URL format:
   ```
   postgresql://user:pass@host/db?sslmode=require
   ```

2. Verify Neon database is active (auto-resumes on connection)

### 502 Bad Gateway

1. Check if deployment is running:
   ```bash
   railway status
   ```

2. Verify PORT environment variable (should be 8080)

3. Check health endpoint:
   ```bash
   curl https://your-app.up.railway.app/health
   ```

---

## Scaling Tips

### Railway

1. Increase replicas in Railway Dashboard → Settings
2. Add more RAM/CPU if needed
3. Use Railway's built-in metrics

### Neon

1. Upgrade to Launch plan for always-active database
2. Enable connection pooling for better performance
3. Use read replicas for scaling reads

### Caching

Current setup includes:
- In-memory LLM cache (5-minute TTL)
- 40% cache hit rate on repeated queries

For production, consider:
- Redis for distributed caching
- CDN for static assets (Swagger UI)

---

## Security Checklist

- ✅ Environment variables set (not in code)
- ✅ DATABASE_URL uses SSL (`sslmode=require`)
- ✅ API keys stored securely in Railway
- ✅ Neon auto-suspend prevents idle usage
- ✅ HTTPS enabled by default (Railway)

---

## Next Steps

1. **Upload CVs:** Use `/api/cv/upload` endpoint
2. **Generate Embeddings:** Run `/api/embeddings/generate`
3. **Build Graph:** Use background jobs
4. **Test Search:** Try hybrid search with sample queries
5. **Monitor Performance:** Check Railway/Neon dashboards

---

## Support

- **Railway Docs:** https://docs.railway.app
- **Neon Docs:** https://neon.tech/docs
- **Project README:** [README.md](README.md)
- **API Docs:** https://your-app.up.railway.app/swagger/index.html
