# cv-search — Project Context

> Bu dosya, her yeni chat oturumunda projeyi hızlıca anlamak için hazırlanmıştır.
> Önemli bir değişiklik yaptığında bu dosyayı güncelle.

---

## Ne Bu Proje?

Go ile yazılmış bir REST API. İşverenler doğal dil sorgusuyla CV arar: `"Python Developer"`, `"Senior Java Developer"`, `"iOS geliştirici"` gibi. CV'ler yüklenince LLM ile parse edilip bir property graph'a dönüştürülüyor. Arama sırasında vector + graph + LLM birlikte çalışıyor.

**Stack:** Go, PostgreSQL, pgvector, OpenAI (embeddings + LLM), GraphRAG pattern.

---

## Dizin Yapısı

```
cmd/api/main.go                     → server entry point
internal/
  api/
    router.go                       → tüm route tanımları
    hybrid_handler.go               → primary search endpoint handler
    cv_handler.go                   → CV upload handler
    graphrag_handler.go             → graph/community endpoint handlers
    embedding_handler.go            → embedding trigger handler
    background_jobs.go              → async CV processing workers
  graphrag/
    hybrid_search.go                → HybridSearchEngine — ana search pipeline
    querier.go                      → GraphQuerier — SQL graph traversal + buildQuery()
    analyzer.go                     → QueryAnalyzer — LLM ile query → SearchCriteria
    llm_scorer.go                   → LLMScorer — LLM reranking prompt + cache
    embeddings.go                   → EmbeddingService — OpenAI embeddings + pgvector search
    bm25_search.go                  → BM25Searcher — candidates full-text (BM25Weight=0.2, aktif)
    communities.go                  → DefaultCommunities map + FindCommunities()
    community.go                    → Leiden community detection
    graph.go                        → GraphBuilder — node/edge CRUD
    search.go                       → GraphRAG SearchEngine (legacy, hybrid kullanılıyor)
    llm_search.go                   → LLMSearchEngine (legacy)
    matcher.go                      → CriteriaMatcher + SearchCriteria struct tanımı
    llm_cache.go                    → LLMCache (in-memory, 30m TTL)
    enhanced_search.go              → unused / experimental
  config/config.go                  → env var parsing
  cv/
    parser.go                       → CV text extraction
    extractor.go                    → LLM ile CV → entities (skills, companies, education)
  llm/service.go                    → LLM client (OpenAI / Groq)
  storage/
    db.go                           → DB connection + legacy SearchCandidates()
    models.go                       → DB model structs
migrations/complete_setup.sql       → tüm tablo tanımları
docs/
  docs.go                           → ⚠️ Swagger UI buradan gelir — swagger.yaml/json değil!
  swagger.yaml / swagger.json       → referans kopyalar (elle güncellenir)
```

> **Swagger güncelleme kuralı:** Yeni endpoint eklenince `docs/docs.go` içindeki
> `docTemplate` string'ini güncelle. `swagger.yaml` ve `swagger.json` de senkron tut.
> `go build ./...` ile kontrol et, sonra push.
```

---

## API Endpoints

| Method | Path | Açıklama |
|--------|------|----------|
| GET | `/health` | `{"status":"healthy"}` |
| GET | `/swagger/` | Swagger UI |
| POST | `/api/search/hybrid` | **Primary search** — hybrid arama |
| POST | `/api/search` | Legacy BM25 search (candidates tablosu) |
| POST | `/api/cv/upload` | Tek CV yükle (async işlenir) |
| POST | `/api/cv/bulk-upload` | Toplu CV yükle (max 10) |
| GET | `/api/cv/batch/{id}` | Batch yükleme durumu |
| GET | `/api/cv/job/{id}` | Tek job durumu |
| GET | `/api/candidates` | Aday listesi (`?limit=50&offset=0`) |
| GET | `/api/candidates/{id}` | Aday detayı + tüm görüşmeler |
| POST | `/api/candidates/{id}/interviews` | Yeni görüşme ekle (re-embed tetikler) |
| PUT | `/api/candidates/{id}/interviews/{iid}` | Görüşme güncelle |
| DELETE | `/api/candidates/{id}/interviews/{iid}` | Görüşme sil |
| GET | `/api/graph/stats` | Node/edge sayıları |
| GET | `/api/graph/skills/popular` | En çok görülen skill'ler |
| POST | `/api/graphrag/search` | Legacy GraphRAG search |
| POST | `/api/graphrag/embeddings/generate` | Embedding üret (tüm person node'ları) |
| POST | `/api/graphrag/communities/detect` | Leiden community tespiti çalıştır |

CORS `CORS_ORIGINS` env var ile kontrol edilir (default `*`).

---

## Veritabanı Tabloları

| Tablo | Amaç |
|-------|------|
| `candidates` | Aday kaydı. `graph_node_id` ile graph_nodes'a bağlı. `experience`, `skills`, `search_vector` tsvector kolonları BM25 için aktif. |
| `cv_files` | Yüklenen ham dosyalar, extract edilmiş text, SHA-256 duplicate kontrolü |
| `cv_entities` | Dosya başına LLM tarafından çıkarılan entity'ler |
| `graph_nodes` | Property graph node'ları: `person`, `skill`, `company`, `education`. `vector` kolonu (1536d) var. |
| `graph_edges` | Typed edge'ler: `HAS_SKILL`, `WORKS_AT`, `WORKED_AT`, `GRADUATED_FROM` |
| `graph_communities` | Leiden algoritması ile tespit edilen topluluklar, `level`, `summary`, `vector` var |
| `community_members` | `graph_nodes ↔ graph_communities` many-to-many, `membership_strength` |
| `interviews` | Aday görüşmeleri — `interview_date`, `team`, `interviewer_name`, `interview_type`, `outcome`, `notes`. Her adayın N görüşmesi olabilir. |
| `candidate_scores` | Geçmiş arama skorları (historik, aktif kullanılmıyor) |
| `cv_upload_jobs` | Async job kuyruğu: `pending → processing → completed/failed`, max 3 retry |

pgvector extension aktif. `graph_nodes.embedding` ve `graph_communities.embedding` üzerinde HNSW index var.

---

## Arama Pipeline'ı (Hybrid Search)

```
POST /api/search/hybrid  {"query": "Python Developer"}
          │
          ▼
1. EMBEDDING
   OpenAI text-embedding-3-small ile query embedding üret (1536d)
   Semantic cache kontrol (cosine ≥ 0.95, 30m TTL) → HIT ise direkt dön
          │
          ▼
2. PARALEL RETRIEVAL (3 goroutine)
   ├── BM25  → candidates tablosunda full-text, OR tsquery (BM25Weight=0.2)
   ├── Vector→ graph_nodes üzerinde pgvector cosine search (TopK=100)
   └── Graph → QueryAnalyzer ile query → SearchCriteria (LLM call)
               → buildQuery() ile SQL traversal
               → SearchCriteria dışarı expose edilir (post-fusion filter için)
          │
          ▼
3. RRF FUSION
   Reciprocal Rank Fusion (k=60) + normalize
   FusionScore = 0.2*BM25 + 0.5*Vector + 0.3*Graph
          │
          ▼
4. ENRICH
   Batch SQL ile person detayları, skills, companies, computed community yükle
          │
          ▼
5. SKILL POST-FILTER
   criteria.Skills doluysa: ilgili skill'i olmayan adayları çıkar
   (vector search semantically benzer ama alakasız CVleri de getirir)
   → Hiç eşleşme yoksa filtre atlanır (boş sonuç yerine)
          │
          ▼
6. COMMUNITY CONTEXT
   Query embedding ile top-3 graph_communities bul → LLM context
          │
          ▼
7. COMMUNITY FILTER (opsiyonel)
   UseCommunityFilter=true VEYA aday sayısı ≥ 50 ise aktif
   Keyword matching ile query → community eşleştirme
          │
          ▼
8. TOP-N TRUNCATE
   FinalTopN=8 → LLM'e sadece 8 aday gönder
          │
          ▼
9. LLM RERANKING (tek call)
   LLM cache kontrol (30m TTL)
   Prompt: role + 3 kriter (skills / title / experience) + 8 aday profili
   JSON parse, skor clamp 0-100
          │
          ▼
10. MERGE + SORT
    LLM skorlarını merge et, LLMScore'a göre sırala
    Boş Name/PersonID olanları filtrele, Rank ata
    Semantic cache'e yaz
```

---

## Önemli Sabitler

| Değer | Dosya | Şu anki değer |
|-------|-------|---------------|
| `FinalTopN` | `hybrid_search.go DefaultHybridConfig()` | **8** |
| `TopK` | `hybrid_search.go DefaultHybridConfig()` | **100** |
| `BM25Weight` | `hybrid_search.go` | **0.2** |
| `VectorWeight` | `hybrid_search.go` | **0.5** |
| `GraphWeight` | `hybrid_search.go` | **0.3** |
| `CommunityThreshold` | `hybrid_search.go` | **50** |
| `llmBatchSize` | `llm_scorer.go` | **8** (tek call, gerçek batch yok — isim yanıltıcı) |
| Semantic cache TTL | `hybrid_search.go` | **30 dakika**, threshold **0.95** |
| LLM cache TTL | `llm_scorer.go` | **30 dakika** |
| Skill cap (prompt) | `llm_scorer.go skillNames()` | **8** skill |

---

## Env Variables

| Değişken | Zorunlu? | Not |
|----------|----------|-----|
| `DATABASE_URL` | ✅ | PostgreSQL DSN |
| `OPENAI_API_KEY` | ✅ | Embeddings her zaman OpenAI'dan gider — Groq kullansa bile gerekli! |
| `LLM_PROVIDER` | hayır | `openai` (default) veya `groq` |
| `LLM_MODEL` | hayır | default: `gpt-4o-mini` |
| `GROQ_API_KEY` | Groq ise ✅ | |
| `PORT` | hayır | default: `8080` |
| `CORS_ORIGINS` | hayır | default: `*` |

Server timeout'ları: `ReadTimeout` 2 dakika, `WriteTimeout` 15 dakika.

---

## SearchCriteria Struct (matcher.go)

```go
type SearchCriteria struct {
    Skills        []string  // "Python", "Java", "React"
    Companies     []string  // "Google", "Meta"
    Positions     []string  // "Python Developer", "Backend Engineer"
    Seniority     string    // "Junior|Mid-level|Senior|Lead|Architect"
    Education     []string  // kurum adı veya derece türü
    MinExperience *int      // yıl
    MaxExperience *int      // yıl
    Location      []string  // şehir/ülke
}
```

QueryAnalyzer bu struct'ı LLM ile query'den çıkarır (`analyzer.go`).

---

## buildQuery Mantığı (querier.go)

SQL dinamik olarak üretilir, `argIndex` artar.

- **Skills:** `HAS_SKILL` edge traverse, `skill_{Name}` node_id exact match
- **Positions:** Generic kelimeler atlanır (`developer`, `engineer`, `software`, `senior`, `junior`, `lead`...). Kalan özel kelimeler AND ile birleştirilir. Birden fazla position OR'lanır.
  - `"Python Developer"` → sadece "Python" arar (Developer generic, atlanır)
  - `"iOS Developer"` → sadece "iOS" arar
  - `"React Frontend Developer"` → "React" AND "Frontend" arar
- **Companies:** ILIKE partial match (WORKS_AT + WORKED_AT edge'lerden)
- **Seniority:** exact match
- **Experience:** `(total_experience_years)::int >= / <=`
- LIMIT 50 (güvenlik sınırı)

---

## LLM Reranking Prompt Felsefesi (llm_scorer.go)

Kasıtlı olarak minimal tutulmuştur. Hardcoded kural yok, community tanımı yok, "Java için şunu yap Python için şunu" yok.

LLM zaten iyi bir recruiter gibi düşünebilir — ona sadece temiz bir aday listesi verip "sırala" demek yeterli.

**3 kriter (önem sırasıyla):**
1. Skills match — tech stack role ile örtüşüyor mu?
2. Title match — mevcut/geçmiş pozisyon role uyuyor mu?
3. Experience — kaç yıl ilgili deneyim var?

---

## Bilinen Sorunlar

| # | Sorun | Etki |
|---|-------|------|
| 1 | `calculateMatch()` dead code — her zaman `0.0` döner | Sıfır etki, temizlenebilir |
| 2 | `llmBatchSize` ismi yanıltıcı — batch logic yok, tek call | Sadece isim karışıklığı |
| 3 | `embeddings.go`: `ORDER BY similarity ASC` — sıralama tersten olabilir | Fusion'da RRF düzeltiyor, tek başına kullanılırsa bozulur |
| 4 | `DEALLOCATE ALL` her aramada çalışır | Yüksek concurrency'de başka isteğin prepared statement'ını öldürebilir |
| 5 | BM25 OR tsquery — çok kısa sorgu (tek kelime < 3 harf) fallback'e düşer | Pratik etkisi yok |
| 6 | Community: keyword-based `DefaultCommunities` + Leiden graph communities ayrı sistemler | İleride birleştirilmeli |
