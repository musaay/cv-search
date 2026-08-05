# Güvenlik Açığı (Security) Analizi ve Çözüm Planı

Uygulamanın mimarisini ve kod tabanını (veritabanı, dosya işlemleri, API yönlendirmeleri) güvenlik standartları (OWASP Top 10) açısından detaylıca inceledim. Sistemde tespit ettiğim **Kritik (Critical)** seviyede 2 adet ve **Orta (Medium)** seviyede 1 adet güvenlik zafiyeti bulunmaktadır. 

(Not: Önceki analizdeki SQL Injection şüphemi detaylıca araştırdım. Uygulamada `ORDER BY` parametreleri ve IN sorguları (placeholder'lar) çok düzgün bir şekilde validate edildiği için SQL Injection açısından **sistem güvenli**.)

Aşağıda tespit edilen zafiyetler ve çözüm planı yer almaktadır:

## 🚨 Kritik Güvenlik Açıkları (Critical)

### 1. Path Traversal (Dizin Aşımı / Sunucu Dosyalarını Üzerine Yazma)
- **Sorun:** `internal/cv/parser.go` içindeki `ParseFile` fonksiyonunda, kullanıcıdan (tarayıcıdan) gelen dosya adı (`header.Filename`) hiçbir filtrelemeye tabi tutulmadan doğrudan sunucu diskine yazılıyor (`filepath.Join(p.uploadsDir, filename)`). 
- **Risk:** Kötü niyetli bir saldırgan dosya adını `../../../.env` veya `../../../../etc/passwd` olarak değiştirerek sunucudaki hassas dosyaların üzerine yazabilir (RCE veya tam sunucu erişimine yol açar).
- **Çözüm Planı:** `cv_handler.go` ve `parser.go` güncellenecek. Dosya isimleri sunucuya kaydedilirken orjinal isimleri yerine rastgele üretilmiş güvenli bir benzersiz isim (UUID veya Hash) ile kaydedilecek.

### 2. Broken Access Control (Eksik Kimlik Doğrulama / Tamamen Açık API)
- **Sorun:** `internal/api/router.go` dosyasında görüldüğü üzere API tamamen herkese açık. Herhangi bir kullanıcı (veya bot), `/api/candidates/merge`, `/api/cv/upload` veya `/api/candidates/{id}` uç noktalarına istek atarak tüm veritabanını silebilir, adayları birleştirebilir veya sahte CV'ler yükleyebilir.
- **Risk:** Tam veri kaybı, yetkisiz erişim ve veri sızıntısı.
- **Çözüm Planı:** Sistem dahili bir uygulama ise, en hızlı ve güvenli çözüm **API Key Middleware** eklemektir. `.env` dosyasına `API_KEY` eklenecek ve `router.go` üzerinden tüm kritik POST/PUT/DELETE ve GET istekleri `X-API-Key` başlığı (header) ile korunacak.

## ⚠️ Orta Seviye Güvenlik Açıkları (Medium)

### 3. Açık CORS Politikası
- **Sorun:** `.env` dosyasında `CORS_ORIGINS=*` tanımlandığı durumlarda herhangi bir web sitesi sizin API'nizi kendi sitesi üzerinden çağırabilir.
- **Çözüm Planı:** Üretime (Production) çıkarken `CORS_ORIGINS` değişkenine sadece kendi önyüz (frontend) domain'inizin girildiğinden emin olunmalıdır (Şu an müdahaleye gerek yok, sadece konfigürasyon uyarısı).

---

## User Review Required

> [!CAUTION]
> API uç noktalarının (Endpoints) şu an korumasız olması en büyük risktir.
> **Soru:** API uç noktalarını korumak için `X-API-Key` bazlı basit ve sağlam bir koruma (Middleware) eklememi onaylıyor musunuz? (Frontend / İstemci tarafında bu API Key'in istek başlıklarına eklenmesi gerekecektir).

Planı ve API Key korumasını onaylarsanız, hem **Path Traversal (Dosya Zafiyeti)** hem de **API Güvenliği** kodlamasını başlatacağım.
