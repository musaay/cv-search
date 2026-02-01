# 🧪 Hybrid Search Test Guide

## Quick Start

Test için sisteminizde 4 kişi var:
1. **Emine Yürektürk Ay** - Product Owner & Business Analyst (Mid-level)
2. **Merve Birsin** - Full Stack Software Engineer (Mid-level)
3. **Mehmet Öz** - Senior Software Architect (Architect) - Akbank deneyimi
4. **Mücahit Şahin** - Java Developer (Mid-level)

---

## Test Komutları

### 1️⃣ Basic Test - Banking Experience

```bash
curl -X POST http://localhost:8080/api/search/hybrid \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Bankada çalışmış senior developer"
  }' | jq '.'
```

**Beklenen:** Mehmet Öz #1 olmalı (Akbank architect)

---

### 2️⃣ Full Stack Developer

```bash
curl -X POST http://localhost:8080/api/search/hybrid \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Full stack developer"
  }' | jq '.candidates[] | {rank, name, llm_score, llm_reasoning}'
```

**Beklenen:** Merve Birsin yüksek skorlamalı

---

### 3️⃣ Architect Role

```bash
curl -X POST http://localhost:8080/api/search/hybrid \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Software architect"
  }' | jq '.candidates[] | {rank, name, llm_score}'
```

**Beklenen:** Mehmet Öz #1 (Senior Software Architect)

---

### 4️⃣ Product Owner

```bash
curl -X POST http://localhost:8080/api/search/hybrid \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Product owner"
  }' | jq '.candidates[] | {rank, name, llm_score}'
```

**Beklenen:** Emine Yürektürk Ay #1

---

### 5️⃣ Java Developer

```bash
curl -X POST http://localhost:8080/api/search/hybrid \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Java backend developer"
  }' | jq '.candidates[] | {rank, name, llm_score, llm_reasoning}'
```

**Beklenen:** Mücahit Şahin yüksek skorlamalı

---

## Score Breakdown Testi

Her bir score'u görmek için:

```bash
curl -X POST http://localhost:8080/api/search/hybrid \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Bankada çalışmış kişiler"
  }' | jq '.candidates[] | {
    rank, 
    name, 
    bm25: .bm25_score,
    vector: .vector_score,
    graph: .graph_score,
    fusion: .fusion_score,
    llm: .llm_score,
    reasoning: .llm_reasoning
  }'
```

---

## Custom Weights Test

### BM25 Heavy (Keyword Priority)

```bash
curl -X POST http://localhost:8080/api/search/hybrid \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Architect",
    "bm25_weight": 0.6,
    "vector_weight": 0.2,
    "graph_weight": 0.2
  }' | jq '.candidates[] | {rank, name, bm25_score, llm_score}'
```

### Vector Heavy (Semantic Priority)

```bash
curl -X POST http://localhost:8080/api/search/hybrid \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Experienced professional with leadership",
    "bm25_weight": 0.2,
    "vector_weight": 0.5,
    "graph_weight": 0.3
  }' | jq '.candidates[] | {rank, name, vector_score, llm_score}'
```

### Graph Heavy (Relationship Priority)

```bash
curl -X POST http://localhost:8080/api/search/hybrid \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Developer with banking connections",
    "bm25_weight": 0.2,
    "vector_weight": 0.3,
    "graph_weight": 0.5
  }' | jq '.candidates[] | {rank, name, graph_score, llm_score}'
```

---

## Performance Test

```bash
time curl -X POST http://localhost:8080/api/search/hybrid \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Senior developer",
    "final_top_n": 10
  }' | jq '{processing_time, total_found, method}'
```

---

## Complete Test Suite

Tüm testleri çalıştırmak için:

```bash
./scripts/test_hybrid_search.sh
```

---

## Debugging

### Check Server Logs

```bash
tail -f /tmp/api.log
```

### Check Database

```bash
# Kişileri listele
psql "postgres://may@localhost:5432/linkedin_scraper?sslmode=disable" \
  -c "SELECT node_id, properties->>'name', properties->>'seniority' FROM graph_nodes WHERE node_type='person';"

# Skills kontrol
psql "postgres://may@localhost:5432/linkedin_scraper?sslmode=disable" \
  -c "SELECT n.properties->>'name' as person, array_agg(s.properties->>'name') as skills 
      FROM graph_nodes n 
      JOIN graph_edges e ON n.id = e.source_node_id 
      JOIN graph_nodes s ON e.target_node_id = s.id 
      WHERE n.node_type='person' AND s.node_type='skill' 
      GROUP BY n.properties->>'name';"

# Companies kontrol
psql "postgres://may@localhost:5432/linkedin_scraper?sslmode=disable" \
  -c "SELECT n.properties->>'name' as person, array_agg(c.properties->>'name') as companies 
      FROM graph_nodes n 
      JOIN graph_edges e ON n.id = e.source_node_id 
      JOIN graph_nodes c ON e.target_node_id = c.id 
      WHERE n.node_type='person' AND c.node_type='company' 
      GROUP BY n.properties->>'name';"
```

---

## Expected Results Summary

| Query | Expected #1 | Reason |
|-------|-------------|--------|
| "Bankada çalışmış" | Mehmet Öz | Akbank experience, Architect |
| "Full stack developer" | Merve Birsin | Full Stack Engineer |
| "Software architect" | Mehmet Öz | Senior Software Architect |
| "Product owner" | Emine Yürektürk Ay | Product Owner title |
| "Java developer" | Mücahit Şahin | Java Developer |

---

## Troubleshooting

### No results?
- Check: `curl http://localhost:8080/health`
- Check logs: `tail /tmp/api.log`

### Wrong ranking?
- Check LLM reasoning: Add `| jq '.candidates[] | .llm_reasoning'`
- Check all scores: Use score breakdown command above

### Slow performance?
- Reduce `final_top_n` to 20
- Check: Processing time should be 3-6 seconds

### LLM errors?
- Check: `echo $GROQ_API_KEY`
- Check: `echo $OPENAI_API_KEY`
