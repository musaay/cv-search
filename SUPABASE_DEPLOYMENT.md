# 🚀 Supabase Deployment Guide

Supabase has **pgvector built-in** and offers a generous free tier.

## Step 1: Create Supabase Project

1. Go to https://supabase.com
2. Sign in with GitHub
3. Click **"New Project"**
4. Fill in:
   - **Name**: `cv-search-graphrag`
   - **Database Password**: (generate strong password)
   - **Region**: Choose closest to you
   - **Plan**: Free (500MB database, 500MB storage)
5. Click **Create new project** (takes ~2 minutes)

## Step 2: Get Database URL

1. In Supabase Dashboard → **Settings** → **Database**
2. Scroll to **Connection string**
3. Select **URI** format
4. Copy the connection string (looks like):
   ```
   postgresql://postgres:[YOUR-PASSWORD]@db.[PROJECT-REF].supabase.co:5432/postgres
   ```
5. Replace `[YOUR-PASSWORD]` with your database password

## Step 3: Run Database Migration

**Option A: Supabase SQL Editor (Recommended)**
1. Go to **SQL Editor** in Supabase Dashboard
2. Click **New query**
3. Copy content from `migrations/complete_setup.sql`
4. Paste and click **Run** (or press Cmd+Enter)
5. ✅ Done! All tables created with pgvector support

**Option B: Local psql**
```bash
psql "postgresql://postgres:[PASSWORD]@db.[PROJECT-REF].supabase.co:5432/postgres" < migrations/complete_setup.sql
```

## Step 4: Deploy to Railway (with Supabase DB)

Since Railway doesn't support pgvector, we'll use Railway for the app + Supabase for database:

```bash
cd /Users/may/dev/projects/demo-scraper/cv-search

# Initialize Railway (if not done)
railway init

# Set Supabase DATABASE_URL
railway variables set DATABASE_URL="postgresql://postgres:[PASSWORD]@db.[PROJECT-REF].supabase.co:5432/postgres"

# Set other variables
railway variables set OPENAI_API_KEY="sk-proj-..."
railway variables set LLM_PROVIDER="groq"
railway variables set GROQ_API_KEY="gsk_..."
railway variables set LLM_MODEL="llama-3.3-70b-versatile"
railway variables set USE_LLM="true"

# Deploy
railway up
```

## Step 5: Verify

```bash
# Get Railway URL
railway open

# Test endpoints
curl https://your-app.railway.app/health
curl https://your-app.railway.app/swagger/index.html
```

## Why Supabase + Railway?

✅ **Supabase (Database)**
- Free tier: 500MB database
- pgvector extension built-in
- Auto backups
- SQL Editor UI
- Direct PostgreSQL access

✅ **Railway (Application)**
- Easy Docker deployment
- Free tier: 512MB RAM
- Auto SSL
- GitHub integration
- Simple logs

## Costs

**Free Tier:**
- Supabase: Free (500MB DB, 2GB bandwidth)
- Railway: Free (512MB RAM, 100GB network)

**Paid (if needed):**
- Supabase Pro: $25/month (8GB DB, unlimited API)
- Railway Developer: $20/month (8GB RAM)

## Alternative: Supabase Edge Functions

Instead of Railway, you can use Supabase Edge Functions (Deno-based):
- Convert Go code to TypeScript
- Deploy on Supabase platform
- Stay in one ecosystem

But Railway is simpler for existing Go apps!

## Monitoring

**Supabase:**
- Dashboard → Database → Logs
- Check query performance
- Monitor connections

**Railway:**
```bash
railway logs
railway status
```

## Need Help?

- Supabase Docs: https://supabase.com/docs
- Railway Docs: https://docs.railway.app
- pgvector Docs: https://github.com/pgvector/pgvector
