# CODING_RULES.md — cv-search

> Bu dosya AI asistanlar ve geliştiriciler için **bağlayıcı kodlama kurallarını** tanımlar.
> Her yeni oturumda bu dosyayı oku. Mevcut kararları sorgulamadan önce burayı kontrol et.
> Önemli bir karar alındığında bu dosyayı güncelle.

---

## 1. Bilinçli Tasarım Kararları

> Aşağıdaki maddeler **bilerek yapılmış** kararlardır. Bug değildir, "düzelt" denmez.

| Karar | Neden | Dosya |
|-------|-------|-------|
| `BM25Weight = 0.0` | TR sorgular translation layer gerektirir, ileride açılacak | `hybrid_search.go` |
| `DefaultCommunities` keyword map hâlâ aktif | K-means community yeterince veri toplandığında devralacak | `communities.go` |
| Positions filter SQL'de yok | "Java Developer" araması Java Architect'leri de getirmeli, LLM reranking ilgiliyi ayırt eder | `querier.go` |
| `FinalTopN=8` skill aramalarda atlanıyor | Skill-eşleşen adayları kesmemek için bilinçli bypass | `hybrid_search.go` |
| `llmBatchSize=8` ama gerçek batch yok | Tek LLM call yapılır, isim yanıltıcı ama işlevsel | `llm_scorer.go` |
| LLM reranking prompt'u minimal | Kural tabanlı değil, LLM'in muhakemesine güvenilir | `llm_scorer.go` |

---

## 2. Go Kodlama Standartları

### Error Handling
```go
// ✅ Doğru: error wrapping ile %w kullan
return fmt.Errorf("failed to create node %s: %w", nodeID, err)

// ❌ Yanlış: %v sadece log'da kullan, dönüşte %w zorunlu
return fmt.Errorf("failed: %v", err)
```

### Context Propagation
```go
// ✅ Doğru: ctx ilk parametre, her DB ve LLM call'da kullan
func (q *GraphQuerier) QueryGraph(ctx context.Context, criteria *SearchCriteria) ([]CandidateResult, error) {
    rows, err := q.db.QueryContext(ctx, query, args...)
}

// ❌ Yanlış: context olmadan DB call
rows, err := q.db.Query(query, args...)
```

### SQL Queries
```go
// ✅ Doğru: parametrized query ($1, $2, ...)
q.db.QueryContext(ctx, `SELECT * FROM graph_nodes WHERE node_id = $1`, nodeID)

// ❌ Yanlış: string concatenation ile SQL — SQL injection riski
q.db.QueryContext(ctx, "SELECT * FROM graph_nodes WHERE node_id = '" + nodeID + "'")
```

### Batch Operations
```go
// ✅ Doğru: IN clause ile batch query (N+1 önleme)
placeholders := make([]string, len(ids))
for i := range ids {
    placeholders[i] = fmt.Sprintf("$%d", i+1)
}
query := fmt.Sprintf(`SELECT * FROM x WHERE id IN (%s)`, strings.Join(placeholders, ","))

// ❌ Yanlış: loop içinde tekil query (N+1)
for _, id := range ids {
    row := db.QueryRow(`SELECT * FROM x WHERE id = $1`, id)
}
```

---

## 3. LLM Entegrasyon Kuralları

### Prompt Yazımı
- LLM'den **her zaman JSON** iste
- Prompt sonunda `"Return ONLY valid JSON, no markdown."` ekle
- Response parse'ta **her zaman** `extractJSON()` fallback'ini kullan

### LLM Response Parse
```go
// ✅ Doğru: önce raw parse, başarısızsa extractJSON
var result MyStruct
if err := json.Unmarshal([]byte(response), &result); err != nil {
    jsonStr := extractJSON(response)
    json.Unmarshal([]byte(jsonStr), &result)
}
```

### LLM Call Maliyeti
- Her search 2 LLM call: QueryAnalyzer (1) + LLMScorer (1)
- LLM cache (30dk TTL) aktif tutulmalı (`DisableLLMCache=false` prod'da)
- Yeni LLM call eklerken maliyet etkisini düşün (her call ~0.5-2 sn + API maliyeti)

---

## 4. Graph Veri Modeli

### Node Types & ID Conventions
| Type | node_id format | Örnek |
|------|---------------|-------|
| `person` | `person_{cvID}` | `person_42` |
| `skill` | `skill_{Name}` | `skill_Python` |
| `company` | `company_{Name}` | `company_Google` |
| `education` | `education_{Institution}` | `education_Bogazici` |

### Edge Types
| Edge | Direction | Anlamı |
|------|-----------|--------|
| `HAS_SKILL` | person → skill | Aday bu skill'e sahip |
| `WORKS_AT` | person → company | Şu an burada çalışıyor |
| `WORKED_AT` | person → company | Geçmişte burada çalıştı |
| `GRADUATED_FROM` | person → education | Bu okuldan mezun |

### Upsert Pattern
```go
// ✅ Her zaman ON CONFLICT kullan — idempotent olsun
INSERT INTO graph_nodes (node_type, node_id, properties)
VALUES ($1, $2, $3)
ON CONFLICT (node_type, node_id)
DO UPDATE SET properties = EXCLUDED.properties
```

---

## 5. Search Pipeline Kuralları

### Hybrid Search Pipeline Sırası
```
Embedding → Semantic Cache check
    → PARALEL: BM25, Vector, Graph
    → RRF Fusion
    → Enrich (batch SQL)
    → Skill post-filter
    → Community context fetch
    → Community filter (opsiyonel)
    → Interview outcome boost
    → Community score boost
    → Top-N truncate
    → LLM Reranking
    → Merge + Sort + Cache write
```

### Yeni Retrieval Sinyali Ekleme
1. Goroutine ile paralel çalıştır (diğer kaynakları bloklamasın)
2. Result type tanımla, `fuseResults()`'a entegre et
3. `HybridSearchConfig`'e weight ekle (default değer ver)
4. Weight'lerin toplamı 1.0 olmalı

### Yeni Post-Filter Ekleme
- Hard elimination (aday çıkarma) yerine **score modifier** tercih et
- Eğer filter hiç aday bırakmazsa, filter'ı atla (boş sonuç dönme)

---

## 6. API Handler Kuralları

### Response Format
```go
// ✅ Standart hata response'u
w.Header().Set("Content-Type", "application/json")
w.WriteHeader(http.StatusBadRequest)
json.NewEncoder(w).Encode(map[string]string{"error": "açıklayıcı mesaj"})

// ❌ Yanlış: plain text hata
http.Error(w, "bir şey yanlış gitti", 500)
```

### Yeni Endpoint Ekleme
1. Handler'ı `internal/api/` altına yaz
2. Route'u `router.go` → `NewRouter()` içine ekle
3. `docs/docs.go` içindeki `docTemplate`'i güncelle
4. `swagger.yaml` ve `swagger.json`'ı senkron tut
5. `go build ./...` ile derle

---

## 7. Veritabanı Kuralları

### Migration
- Tüm şema `migrations/complete_setup.sql` içinde
- Yeni tablo/kolon eklerken bu dosyayı güncelle
- `IF NOT EXISTS` / `ON CONFLICT` kullan — idempotent olsun

### pgvector
- Embedding boyutu: **1536** (OpenAI text-embedding-3-small)
- Index tipi: **HNSW** (`vector_cosine_ops`)
- Distance operatörü: `<=>` (cosine distance — düşük = benzer)
- Similarity operatörü: `1 - (a <=> b)` — yüksek = benzer

---

## 8. Test & Doğrulama

> ⚠️ Projede şu an test dosyası yok. Yeni özellik eklendikçe test yazılmaya başlanmalı.

### Öncelikli Test Alanları
1. `hybrid_search.go` — fusion mantığı, score hesaplamaları
2. `querier.go` — SQL builder (buildQuery)
3. `analyzer.go` — LLM response parse, criteria extraction
4. `llm_scorer.go` — score clamp, extractJSON, prompt building

### Test Yazma Kuralları
- `_test.go` dosyalarını ilgili paketin içine koy
- LLM call'larını mock'la (LLMClient interface'i kullan)
- DB testleri için test container veya fixture kullan

---

## 9. Dosya Organizasyonu

### Yeni Dosya Nereye?
| İçerik | Konum |
|--------|-------|
| HTTP handler | `internal/api/{domain}_handler.go` |
| Search/graph/embedding logic | `internal/graphrag/{feature}.go` |
| DB CRUD operasyonları | `internal/storage/db.go` (TODO: domain bazlı bölünecek) |
| CV parse/extract | `internal/cv/` |
| LLM client | `internal/llm/service.go` |
| Config | `internal/config/config.go` |
| CLI tools | `cmd/tools/{tool_name}/main.go` |
| Dökümantasyon | `docs/` |

### Dosya Büyüklüğü Limiti
- Bir `.go` dosyası **500 satırı** geçmemeli (ideal)
- Geçiyorsa: ayrı dosyaya veya alt-fonksiyonlara böl
- `hybrid_search.go` (945 LOC), `db.go` (968 LOC) → refactor listesinde
