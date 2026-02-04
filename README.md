# 🎯 CV Search & Gra**🚀 Li**📚 Quick Links:**
- [🚀 Deployment Guide](DEPLOYMENT.md) - Railway + Neon setup
- [📖 API Documentation](https://cv-search-production.up.railway.app/swagger/index.html)
- [🔬 Hybrid Search Details](docs/HYBRID_SEARCH.md)
- [🏘️ Community Detection](docs/COMMUNITY_DETECTION.md) - Microsoft GraphRAG-style overlapping communities
- [🧪 Testing Guide](docs/TESTING.md)mo:** [cv-search-production.up.railway.app](https://cv-search-production.up.railway.app/swagger/index.html)

Modern bir Go tabanlı **Microsoft GraphRAG-inspired** aday keşif sistemi. CV dosyalarını parse eder, PostgreSQL knowledge graph'inde saklar ve REST API ile adayları doğal dilde sorgulama imkanı sunar.


## 🧠 Microsoft GraphRAG YaklaşımıQuick Links:**
- [🚀 Deployment Guide](DEPLOYMENT.md) - Railway + Neon setup
- [📖 API Documentation](https://cv-search-production.up.railway.app/swagger/index.html)
- [🔬 Hybrid Search Details](docs/HYBRID_SEARCH.md)
- [📊 GraphRAG Comparison](docs/GRAPHRAG_COMPARISON.md) - Microsoft GraphRAG vs Our Implementation
- [🧪 Testing Guide](docs/TESTING.md)

> AI-powered recruitment platform with **GraphRAG**, **Hybrid Search**, and **LLM-based candidate ranking**

[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16+-336791?style=flat&logo=postgresql)](https://www.postgresql.org/)
[![pgvector](https://img.shields.io/badge/pgvector-0.7+-blue)](https://github.com/pgvector/pgvector)
[![Microsoft GraphRAG](https://img.shields.io/badge/GraphRAG-Inspired-7FBA00?style=flat&logo=microsoft)](https://github.com/microsoft/graphrag)
[![OpenAI](https://img.shields.io/badge/OpenAI-Embeddings-412991?style=flat&logo=openai)](https://openai.com/)
[![Groq](https://img.shields.io/badge/Groq-LLM-FF6B00?style=flat)](https://groq.com/)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![Deploy on Railway](https://railway.app/button.svg)](https://railway.app)

**🚀 Live Demo:** [cv-search-production.up.railway.app](https://cv-search-production.up.railway.app/swagger/index.html)

Modern bir Go tabanlı **Microsoft GraphRAG-inspired** aday keşif sistemi. CV dosyalarını parse eder, PostgreSQL knowledge graph'inde saklar ve REST API ile adayları doğal dilde sorgulama imkanı sunar.

**� Quick Links:**
- [🚀 Deployment Guide](DEPLOYMENT.md) - Railway + Neon setup
- [📖 API Documentation](https://cv-search-production.up.railway.app/swagger/index.html)
- [🔬 Hybrid Search Details](docs/HYBRID_SEARCH.md)
- [🧪 Testing Guide](docs/TESTING.md)


## 🧠 Microsoft GraphRAG Yaklaşımı

Bu proje, Microsoft'un GraphRAG (Graph Retrieval-Augmented Generation) metodolojisini CV recruitment domain'i için uyarlamıştır:

### 🎯 GraphRAG Bileşenleri

| Bileşen | Açıklama | Implementasyon |
|---------|----------|----------------|
| **Knowledge Graph** | Nodes (person, skill, company, education) ve edges (HAS_SKILL, WORKED_AT) | PostgreSQL + pgvector |
| **Vector Embeddings** | Semantic search için 768-dimensional embeddings | OpenAI `text-embedding-3-small` |
| **Community Detection** | Skill clusters ve career patterns | Leiden algorithm |
| **LLM Integration** | Natural language query parsing ve ranking | Groq (llama-3.3-70b-versatile) |
| **Hybrid Search** | Vector + Community + LLM combined retrieval | Custom implementation |

### 🔬 Microsoft GraphRAG vs. Bu Proje

**Microsoft'un Resmi GraphRAG:**
- Python tabanlı research framework
- GPT-4 odaklı (pahalı)
- Genel amaçlı document processing
- GB'larca veri işleme kapasitesi

**Bizim Implementasyonumuz:**
- ✅ Go tabanlı production-ready API
- ✅ Cost-optimized (Groq LLM ücretsiz!)
- ✅ CV recruitment'a özel
- ✅ Lightweight ve hızlı
- ✅ Railway deployment ready

**Ortak Prensipler:**
1. Graph-based knowledge representation
2. Vector embeddings for semantic search  
3. Community detection for context
4. LLM-powered reasoning
5. Hybrid retrieval strategy

---

## 🚀 Özellikler

### Core Capabilities
- 📄 **Async CV Upload** - Instant response (11ms), background LLM processing (318x faster)
- 🔍 **Duplicate Detection** - SHA-256 content hashing prevents duplicate CVs
- 🧠 **GraphRAG Search** - Knowledge graph-based semantic search
- ⚡ **Hybrid Search Engine** - Vector (60%) + Graph (40%) + LLM fusion
- 🎯 **Pure LLM Ranking** - No heuristics, only AI-powered candidate scoring
- 💾 **Smart Caching** - Reduced API costs with intelligent result caching
- 📊 **Job Status Tracking** - Monitor async CV processing progress

### 🧠 GraphRAG Özellikleri

- ✅ **LLM-Powered CV Extraction**: Groq (llama-3.3-70b) ile otomatik CV parsing
- ✅ **Async Background Processing**: CV upload 11ms response, 318x performance improvement
- ✅ **Duplicate Detection**: SHA-256 content hashing ile duplicate CV prevention
- ✅ **Knowledge Graph**: PostgreSQL-based entity ve relationship modeling
- ✅ **Vector Search**: OpenAI embeddings ile semantic similarity search
- ✅ **Community Detection**: Leiden algorithm ile skill clustering
- ✅ **Hybrid Search**: Vector + Community + LLM combined retrieval
- ✅ **Natural Language Queries**: "Go developer with 5+ years experience" gibi sorgular
- ✅ **Job Status Tracking**: Real-time CV processing status monitoring
- ✅ **Entity Normalization**: "K8s" → "Kubernetes", "React.js" → "React"
- ✅ **Proficiency Detection**: Beginner/Intermediate/Advanced/Expert classification

### Search Methods

#### 1. **Hybrid Search** (Recommended)
Combines vector + graph retrieval with LLM reranking:
- **Vector**: Semantic similarity (OpenAI embeddings + pgvector) - **60% weight**
- **Graph**: Relationship traversal (skills, companies, education) - **40% weight**
- **LLM Scoring**: GPT-4o-mini for intelligent candidate ranking
- ~~**BM25**: Disabled (candidates table not populated)~~ - **0% weight**

```bash
POST /api/search/hybrid
{
  "query": "Senior Java developer with banking experience",
  "bm25_weight": 0.0,
  "vector_weight": 0.6,
  "graph_weight": 0.4,
  "final_top_n": 10
}
```

#### 2. **GraphRAG Search**
Microsoft GraphRAG-style community-based search

#### 3. **Semantic Search**
Pure vector similarity with LLM enhancement

## �� Tech Stack

| Category | Technology |
|----------|-----------|
| **Backend** | Go 1.24+ |
| **Database** | PostgreSQL 16+ with pgvector |
| **Vector Store** | pgvector (1536-dim OpenAI embeddings) |
| **LLM Providers** | OpenAI (GPT-4o-mini) |
| **Graph** | Custom Knowledge Graph (PostgreSQL) |
| **API Docs** | Swagger/OpenAPI |

## 🛠️ Installation

### Prerequisites
- Go 1.24+
- PostgreSQL 16+ with pgvector extension
- OpenAI API key (for embeddings)
- Groq API key (optional, for LLM)

### 1. Clone Repository
```bash
git clone https://github.com/musaay/cv-search.git
cd cv-search
```

### 2. Install Dependencies
```bash
go mod download
```

### 3. Setup Database
```bash
# Create database
createdb cv_search

# Enable pgvector extension
psql cv_search -c "CREATE EXTENSION IF NOT EXISTS vector;"

# Run migrations
```bash
psql "your-database-url" < migrations/complete_setup.sql
```

Or use the init script:
```bash
chmod +x scripts/init_db.sh
./scripts/init_db.sh
```

### 4. Configure Environment
```bash
cp .env.example .env
# Edit .env with your credentials
```

Required environment variables:
```env
DATABASE_URL=postgresql://user:pass@localhost:5432/cv_search?sslmode=disable
OPENAI_API_KEY=sk-...          # Required for embeddings
LLM_PROVIDER=groq              # 'openai' or 'groq'
LLM_MODEL=llama-3.3-70b-versatile  # or 'gpt-4o-mini'
GROQ_API_KEY=gsk_...           # If using Groq (free!)
USE_LLM=true
```

### 5. Run Server
```bash
go run cmd/api/main.go
```

Server starts on `http://localhost:8080`

---

## 🚀 Production Deployment

See [DEPLOYMENT.md](DEPLOYMENT.md) for complete Railway + Neon deployment guide.

**Live Demo:** https://cv-search-production.up.railway.app

---

## 📚 API Documentation

### Swagger UI
- **Local:** http://localhost:8080/swagger/index.html
- **Production:** https://cv-search-production.up.railway.app/swagger/index.html

### Key Endpoints

#### Upload CV
```bash
curl -X POST https://cv-search-production.up.railway.app/api/cv/upload \
  -F "file=@resume.pdf" \
  -F "name=John Doe"
```

#### Hybrid Search
```bash
curl -X POST https://cv-search-production.up.railway.app/api/search/hybrid \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Full stack developer with React and Go experience",
    "final_top_n": 5
  }'
```

Response:
```json
{
  "candidates": [
    {
      "person_id": "person_1",
      "name": "John Doe",
      "llm_score": 92.5,
      "llm_reasoning": "Strong full-stack experience with React and Go...",
      "fusion_score": 0.85,
      "rank": 1
    }
  ],
  "processing_time": "1.2s"
}
```

## 🔧 Configuration

### LLM Provider Switching
Switch between OpenAI and Groq in `.env`:

**OpenAI (Reliable, higher limits):**
```env
LLM_PROVIDER=openai
LLM_MODEL=gpt-4o-mini
```

**Groq (Fast, free tier):**
```env
LLM_PROVIDER=groq
LLM_MODEL=llama-3.3-70b-versatile
GROQ_API_KEY=gsk_...
```

### Hybrid Search Weights
Current configuration (BM25 disabled):
```json
{
  "bm25_weight": 0.0,     // Disabled (candidates table not used)
  "vector_weight": 0.6,   // Semantic similarity - PRIMARY
  "graph_weight": 0.4     // Relationship strength - SECONDARY
}
```

**Note**: BM25 is disabled because the `candidates` table is not populated in the current architecture. All data flows through the graph (`graph_nodes`, `graph_edges`). BM25 can be re-enabled if the candidates table is populated.

## 📊 Architecture

```
┌─────────────────────────────────────────────────────────┐
│           Hybrid Search Engine (Vector + Graph)         │
├─────────────────────┬───────────────────┬───────────────┤
│      Vector (60%)   │    Graph (40%)    │  LLM Scoring  │
│     (Semantic)      │    (Relations)    │   (Final)     │
└─────────────────────┴───────────────────┴───────────────┘
             │                   │                │
             ├───────────────────┴────────────────┤
             │   Reciprocal Rank Fusion (RRF)     │
             └────────────────┬───────────────────┘
                              │
                       ┌──────▼──────┐
                       │  LLM Scorer │
                       │ (GPT-4o-mini)│
                       └─────────────┘
                       
Note: BM25 disabled (candidates table not used)
```

## 📈 Performance

- **Average Query Time**: 1-3 seconds
- **Cache Hit Rate**: ~40% (5-minute TTL)
- **Concurrent Requests**: 100+ supported
- **Database**: Handles 1000+ candidates efficiently

## 📁 Proje Yapısı

```
cv-search/
├── cmd/
│   ├── api/
│   │   └── main.go              # REST API server entry point
│   └── tools/
│       └── backfill_positions/
│           └── main.go          # Data migration tool
├── internal/
│   ├── api/
│   │   ├── handler.go           # API endpoint handlers
│   │   ├── router.go            # API routes
│   │   ├── cv_handler.go        # CV upload & processing
│   │   ├── background_jobs.go   # Background embedding worker
│   │   ├── embedding_handler.go # Embedding generation API
│   │   ├── graphrag_handler.go  # GraphRAG endpoints
│   │   └── hybrid_handler.go    # Hybrid search endpoints
│   ├── config/
│   │   └── config.go            # Configuration management
│   ├── cv/
│   │   ├── parser.go            # CV file parsing
│   │   └── extractor.go         # Entity extraction
│   ├── graphrag/
│   │   ├── embeddings.go        # OpenAI embedding service
│   │   ├── enhanced_search.go   # Hybrid search engine
│   │   ├── graph.go             # Knowledge graph construction
│   │   ├── llm_search.go        # LLM-powered semantic search
│   │   ├── community.go         # Community detection
│   │   └── search.go            # Graph-based search
│   ├── llm/
│   │   └── service.go           # LLM service interface
│   └── storage/
│       ├── db.go                # Database layer
│       └── models.go            # Data models
├── migrations/
│   ├── 001_create_candidates.sql
│   ├── 002_extended_features.sql
│   ├── 003_create_graph_data.sql
│   ├── 004_add_vector_support.sql
│   └── 005_add_communities.sql
├── docs/
│   ├── HYBRID_SEARCH.md         # Technical deep dive
│   ├── TESTING.md               # Test scenarios
│   └── TEST_RESULTS.md          # Performance metrics
└── uploads/                     # CV file storage (gitignored)
```

## 🎓 Documentation

- [Hybrid Search Guide](docs/HYBRID_SEARCH.md) - Technical deep dive
- [Testing Guide](docs/TESTING.md) - Test scenarios
- [Test Results](docs/TEST_RESULTS.md) - Performance metrics

## 📄 License

MIT License - see [LICENSE](LICENSE) file

## 📧 Contact

Project Link: [https://github.com/musaay/cv-search](https://github.com/musaay/cv-search)

---

**Built with ❤️ using Go and AI**
