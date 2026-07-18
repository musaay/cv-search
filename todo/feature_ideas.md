# Özellik Fikirleri 🎯

> cv-search'ün arama sonuçlarını zenginleştirmek ve işveren deneyimini iyileştirmek için özellik fikirleri.

---

## Adapte Edilebilecek Fikirler

### 🟢 1. Aday Skor Dağılımı Dashboard'u (Hemen Yapılabilir)
Hybrid search sonuçlarında dönen LLM skorunu **kategorilere böl**:

```
Arama: "Senior Python Developer"
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Aday: Ahmet Yılmaz          Skor: 87/100

  Skills Match    ████████████████████░  92%
  Title Match     ███████████████████░░  85%
  Experience      ████████████████░░░░░  78%
  Community Fit   ██████████████████░░░  88%
```

**Nasıl:** `llm_scorer.go`'daki reranking prompt'una 4 sub-score döndürmesi istenir. Zaten 3 kriter var (skills/title/experience) + community context. Mevcut JSON response'a `breakdown` field'ı eklenir.

---

### 🟢 2. Skill Gap Analizi — İşveren Perspektifi (Hemen Yapılabilir)
Arama sonuçlarında her aday için **"rol ile uyuşmayan skill'ler"** göster:

```json
{
  "candidate": "Mehmet Kaya",
  "score": 72,
  "matching_skills": ["Python", "Django", "PostgreSQL"],
  "missing_skills": [
    {"skill": "Kubernetes", "priority": "high"},
    {"skill": "CI/CD", "priority": "medium"},
    {"skill": "AWS", "priority": "low"}
  ]
}
```

**Nasıl:** `SearchCriteria.Skills` zaten query'den çıkarılıyor. Adayın `HAS_SKILL` edge'leri ile karşılaştırılıp `missing_skills` + `matching_skills` listesi oluşturulabilir.

---

### 🟡 3. Market Percentile — Aday Sıralaması (Orta Efor)
Arama yapılınca, sonuçlardaki **tüm adayların skor dağılımını** göster:

```
"Bu aday, bu role başvuran 45 kişi arasında
 ilk %15'te yer alıyor"
```

**Nasıl:** `candidate_scores` tablosu zaten var ama aktif kullanılmıyor. Geçmiş aramaları kayıt edip percentile hesaplanabilir.

---

### 🟡 4. Aday Profil Raporu (Orta Efor)
`GET /api/candidates/{id}/report?role=Python+Developer` endpoint'i — skor breakdown + skill gap + community bilgisi tek bir rapor olarak.

**Nasıl:** 1 + 2'nin birleşimi + yeni endpoint.

---

### 🔴 5. JD-Based Matching — Reverse Search (Büyük Efor)
İşveren bir JD (Job Description) yükler, sistem otomatik olarak en uygun adayları bulur:

```
POST /api/jobs/match
Body: { "job_description": "... full JD text ..." }
```

**Nasıl:** JD text'ini `QueryAnalyzer` ile parse et → `SearchCriteria` çıkar → Hybrid search çalıştır.

---

### 🔴 6. Aday Karşılaştırma (Büyük Efor)
İki veya daha fazla adayı yan yana karşılaştır — ortak/farklı skill'ler, deneyim, LLM özet.

---

## Öncelik Sıralaması

| # | Özellik | Efor | Etki | Mevcut Altyapı |
|---|---------|------|------|----------------|
| 1 | **Skor Dağılımı (Breakdown)** | 🟢 Düşük | Yüksek | LLM scorer'a prompt değişikliği |
| 2 | **Skill Gap Analizi** | 🟢 Düşük | Yüksek | SearchCriteria + HAS_SKILL edge var |
| 3 | **Market Percentile** | 🟡 Orta | Orta | candidate_scores tablosu var |
| 4 | **Aday Profil Raporu** | 🟡 Orta | Yüksek | 1 + 2'nin birleşimi |
| 5 | **JD-Based Matching** | 🔴 Yüksek | Çok Yüksek | QueryAnalyzer genişletilir |
| 6 | **Aday Karşılaştırma** | 🔴 Yüksek | Orta | Yeni endpoint + LLM call |

> **İlk sprint önerisi:** 1 + 2 → ~1-2 gün, mevcut altyapının doğal uzantısı.
