# 🐘 Railway PostgreSQL with pgvector

Railway'nin varsayılan PostgreSQL'i pgvector desteklemiyor. Custom PostgreSQL container kullanacağız.

## Adımlar

### 1. Railway PostgreSQL'i Sil

Railway Dashboard'dan:
1. PostgreSQL service'i seç
2. Settings → Delete Service
3. Onayla

### 2. Custom PostgreSQL Deploy Et

**Railway Dashboard:**
1. **New** → **Empty Service**
2. Service adı: `postgres-pgvector`
3. **Settings** → **Source** → **Docker Image**
4. Build context: `/`
5. Dockerfile path: `Dockerfile.postgres`

**Veya Railway CLI:**
```bash
# Create new service
railway service create postgres-pgvector

# Set Dockerfile
railway service postgres-pgvector --dockerfile Dockerfile.postgres

# Deploy
railway up
```

### 3. Environment Variables Ayarla

PostgreSQL service için:
```bash
POSTGRES_USER=postgres
POSTGRES_PASSWORD=<generate-strong-password>
POSTGRES_DB=railway
PGDATA=/var/lib/postgresql/data
```

**Railway Dashboard'da:**
1. postgres-pgvector service → **Variables**
2. Ekle:
   - `POSTGRES_USER` = `postgres`
   - `POSTGRES_PASSWORD` = `your-strong-password`
   - `POSTGRES_DB` = `railway`

### 4. Volume Ekle (Data Persistence)

**Railway Dashboard:**
1. postgres-pgvector service → **Settings**
2. **Volumes** → **Add Volume**
3. Mount path: `/var/lib/postgresql/data`
4. Size: 5GB (free tier limit)

### 5. DATABASE_URL Oluştur

Railway otomatik oluşturmayacak, manuel yapacağız:

```bash
# Application service'inde DATABASE_URL set et
railway variables set DATABASE_URL="postgresql://postgres:<password>@postgres-pgvector.railway.internal:5432/railway"
```

**Not:** Railway internal network kullan: `postgres-pgvector.railway.internal`

### 6. Migration Çalıştır

PostgreSQL hazır olduktan sonra:

```bash
# Connect to custom PostgreSQL
railway run psql "postgresql://postgres:<password>@postgres-pgvector.railway.internal:5432/railway"

# Run migration
\i migrations/complete_setup.sql
\q
```

**Veya:**
```bash
railway run psql "postgresql://postgres:<password>@postgres-pgvector.railway.internal:5432/railway" < migrations/complete_setup.sql
```

### 7. Application Deploy

```bash
railway up
```

Application artık pgvector destekli PostgreSQL'e bağlanacak!

## Sorun Giderme

### "postgres-pgvector.railway.internal" bulunamıyor

Railway private network aktif olmalı:
1. Dashboard → Settings → Networking
2. **Private Networking** → Enable

### Volume mount edilmiyor

```bash
# Check logs
railway logs --service postgres-pgvector
```

### pgvector extension yok

Container'a bağlan ve kontrol et:
```bash
railway run psql "postgresql://postgres:<password>@postgres-pgvector.railway.internal:5432/railway"

# Extension kontrol
\dx

# Varsa şunu görmelisin:
# vector | 0.7.0 | public | vector data type and ivfflat and hnsw access methods
```

## Alternatif (Daha Kolay)

Railway'de custom PostgreSQL karmaşıksa:

1. **Supabase kullan** (pgvector built-in, free tier)
2. **Neon kullan** (pgvector destekli, generous free tier)
3. **Railway + Supabase hybrid** (app Railway'de, DB Supabase'de)

Hangisini istersin?
