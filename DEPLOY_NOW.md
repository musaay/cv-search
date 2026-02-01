# 🚀 Quick Railway Deployment Steps

## 1. Initialize Project (Manual - do this in your terminal)

```bash
cd /Users/may/dev/projects/demo-scraper/cv-search
railway init
```

When prompted:
- **Workspace**: Select "Musa Ay's Projects"
- **Project Name**: Enter `cv-search-graphrag` or leave blank for random name

## 2. Add PostgreSQL Database

```bash
railway add --database postgresql
```

This will:
- Create a PostgreSQL instance
- Automatically set `DATABASE_URL` environment variable
- Link it to your service

## 3. Run Database Migration

**Railway Dashboard (Recommended):**
1. Go to PostgreSQL service in Railway
2. Click **"Query"** tab
3. Copy content from `migrations/complete_setup.sql`
4. Paste and click **Execute**
5. Done! ✅ All tables, indexes, and extensions created

**Or via CLI:**
```bash
railway run psql $DATABASE_URL < migrations/complete_setup.sql
```

## 4. Set Environment Variables

```bash
# Set variables via CLI
railway variables set OPENAI_API_KEY="sk-proj-..."
railway variables set LLM_PROVIDER="groq"
railway variables set GROQ_API_KEY="gsk_..."
railway variables set LLM_MODEL="llama-3.3-70b-versatile"
railway variables set USE_LLM="true"
```

Or use Railway Dashboard:
1. Go to https://railway.app/project/your-project
2. Click on your service
3. Go to "Variables" tab
4. Add variables one by one

## 5. Deploy

```bash
railway up
```

Railway will:
- Build using Dockerfile
- Deploy the container
- Generate a public URL

## 6. Get Your URL

```bash
railway open
```

Or check in dashboard for your deployment URL like:
`https://cv-search-graphrag-production.up.railway.app`

## 7. Test Deployment

```bash
# Health check
curl https://your-url.railway.app/health

# Test search
curl -X POST https://your-url.railway.app/api/search/hybrid \
  -H "Content-Type: application/json" \
  -d '{"query": "Senior developer", "final_top_n": 5}'
```

## Quick Commands Reference

```bash
# View logs
railway logs

# Check status
railway status

# Redeploy
railway up

# Open dashboard
railway open

# List services
railway service

# Environment variables
railway variables

# Connect to database
railway run psql $DATABASE_URL
```

## Troubleshooting

### If build fails:
```bash
railway logs --build
```

### If deployment fails:
```bash
railway logs
```

### To restart service:
```bash
railway restart
```

---

**Ready to deploy? Run these commands in order:**

1. `railway init`
2. `railway add --database postgresql`
3. Set environment variables
4. Run migrations
5. `railway up`
6. `railway open`

Good luck! 🚀
