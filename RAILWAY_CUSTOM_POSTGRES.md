# Railway Custom PostgreSQL + pgvector - Manuel Adımlar

## 1. Railway Dashboard'a Git

https://railway.app/project/robust-luck

## 2. Yeni Service Oluştur

1. **+ New** butonuna tıkla
2. **Empty Service** seç
3. Service adı: `postgres-pgvector` (veya istediğin isim)

## 3. GitHub Repo Bağla

1. Yeni oluşturduğun service'e tıkla
2. **Settings** → **Source**
3. **Connect Repo** → GitHub repo seç (`musaay/linkedin-scraper`)
4. **Root Directory**: `/` (default)
5. **Build Command**: (boş bırak, Dockerfile kullanacağız)

## 4. Dockerfile Ayarla

Settings'te:
- **Dockerfile Path**: `Dockerfile.postgres`
- **Docker Build Context**: `/`

## 5. Environment Variables Ekle

Service → **Variables** tab:
```
POSTGRES_USER=postgres
POSTGRES_PASSWORD=your-strong-password-here
POSTGRES_DB=railway
PGDATA=/var/lib/postgresql/data
```

## 6. Volume Ekle (Önemli!)

**Settings** → **Volumes**:
- Click **+ New Volume**
- Mount Path: `/var/lib/postgresql/data`
- Click **Add**

## 7. Deploy Et

Değişiklikleri kaydet → Otomatik deploy başlayacak

Build logs'ta şunu göreceksin:
```
Building Dockerfile.postgres...
Installing postgresql-16-pgvector...
Creating extension...
```

## 8. Private Network Aktif Et

**Settings** → **Networking**:
- **Generate Domain** → PostgreSQL'e dışarıdan erişim için
- **Private Networking** → Railway internal network için

## 9. DATABASE_URL Güncelle

Ana application service'inde:

**Variables** → `DATABASE_URL`:
```
postgresql://postgres:your-password@postgres-pgvector.railway.internal:5432/railway
```

## 10. Migration Çalıştır

PostgreSQL deploy olduktan sonra:

```bash
# Public URL üzerinden (eğer generate domain yaptıysan)
psql "postgresql://postgres:your-password@postgres-pgvector-production.up.railway.app:5432/railway" < migrations/complete_setup.sql

# Veya Railway CLI ile
railway run --service postgres-pgvector psql -U postgres -d railway < migrations/complete_setup.sql
```

## 11. Application'ı Deploy Et

```bash
railway up
```

## Kontrol Et

```bash
# PostgreSQL'e bağlan
railway run psql "postgresql://postgres:your-password@postgres-pgvector.railway.internal:5432/railway"

# pgvector var mı kontrol et
\dx

# Tablolar var mı
\dt

# Çık
\q
```

---

## Hızlı Alternatif (Dashboard Kullanmadan)

Eğer CLI'dan yapmak istersen:

```bash
# 1. Git commit & push (Dockerfile.postgres ekledik)
git add Dockerfile.postgres
git commit -m "Add PostgreSQL with pgvector"
git push origin main

# 2. Railway dashboard'dan manual olarak service ekle
# (CLI ile service oluşturma biraz karmaşık)
```

Şimdi Railway dashboard'a git ve yukarıdaki adımları takip et! 🚀
