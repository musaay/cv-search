## cv-search — quick agent guide

This file gives focused, actionable knowledge for an AI coding agent to be immediately productive in this repository.

Keep it short: read these files first, then open the specific module you're changing.

### Big picture (2–3 lines)
- This is a Go 1.24 backend implementing a GraphRAG-style CV search: CVs are parsed, stored in a PostgreSQL knowledge graph (pgvector for vectors), and served via a REST API with hybrid search (vector + graph + optional BM25) and LLM reranking.
- Key runtime: `cmd/api/main.go` boots the server, `internal/api` implements HTTP handlers, `internal/graphrag` contains search/embedding/community logic, and `internal/storage` is the DB layer.

### Key files / responsibilities (open these first)
- `cmd/api/main.go` — server startup, env checks, HTTP timeouts (WriteTimeout 15m to allow long LLM ops).
- `internal/api/*` — API wiring and handlers (router.go, handler.go, cv_handler.go, embedding_handler.go, hybrid_handler.go).
- `internal/graphrag/*` — search engines (LLMSearchEngine, EnhancedSearchEngine, HybridSearchEngine), community detection, embedding glue.
- `internal/llm/service.go` — LLM provider adapter (Groq/OpenAI selection logic is here).
- `internal/storage/db.go` and `internal/storage/models.go` — DB connection and SQL helpers; migrations live in `migrations/`.
- `scripts/init_db.sh` and `migrations/complete_setup.sql` — DB initialization/migration flow.

### How to run & debug locally (explicit)
- Install deps: `go mod download`.
- Init DB: run `./scripts/init_db.sh` or `psql < migrations/complete_setup.sql` (requires PostgreSQL + pgvector enabled).
- Provide env vars (or copy `.env.example` to `.env`): `DATABASE_URL`, `OPENAI_API_KEY` (for vectors), `LLM_PROVIDER` + provider key (`GROQ_API_KEY` or `OPENAI_API_KEY`).
- Start server: `go run cmd/api/main.go` (server listens on `:8080` by default). Swagger is at `/swagger/index.html`.
- Note: `UPLOADS_DIR` defaults to `./uploads`. `PORT` can override `:8080`.

### Important runtime patterns & conventions
- Background processing: uploads are accepted synchronously but processed asynchronously via channels in `internal/api/handler.go` (`cvProcessingQueue`, `embeddingQueue`). Handlers return 202 Accepted with a `job_id` and `GET /api/cv/job/{id}` checks status.
- Duplicate detection: CV text SHA-256 hash computed in `internal/api/cv_handler.go` and `FindCVByHash` is used to avoid duplicates — keep this flow if you touch upload logic.
- Long LLM ops: server `WriteTimeout` is large (15m) to allow long LLM calls; prefer background workers for multi-step work.
- Hybrid search weights: default config is in `graphrag.DefaultHybridConfig()`; handlers validate that BM25+Vector+Graph ≈ 1.0. If you change defaults, update `internal/api/hybrid_handler.go`'s validation.

### Integration points & external dependencies
- Database: PostgreSQL 16+ with `pgvector` extension (vectors stored in `graph_nodes.embedding`). Migrations in `migrations/` must be kept in sync.
- Embeddings: OpenAI `text-embedding-3-small` (env `OPENAI_API_KEY`) — see `internal/graphrag/embeddings.go` for exact call sites.
- LLMs: `LLM_PROVIDER` env selects `groq` or `openai`. Keys: `GROQ_API_KEY`, `OPENAI_API_KEY`. See `internal/llm/service.go` for adapter behavior.

### Useful API examples (copy-pasteable)
- Upload CV (multipart): `POST /api/cv/upload` — returns `job_id` (202 Accepted). Implementations must call `a.queueCVProcessingJob(...)` to enqueue.
- Hybrid search: `POST /api/search/hybrid` with JSON `{ "query":"Senior Java developer", "vector_weight":0.6, "graph_weight":0.4 }`.
- Trigger embeddings: `POST /api/graphrag/embeddings/generate` — queues missing-node embeddings (handler calculates node list and enqueues background jobs).

### Common pitfalls & debugging tips
- If search features are unavailable, check env keys: LLM and OPENAI keys are required to enable `EnhancedSearchEngine` and `HybridSearchEngine` (see `internal/api/handler.go` initialization).
- Slow or failing embeddings: embedding generator assumes a safe rate (~0.2s per node). See `GenerateEmbeddingsHandler` which estimates time and queues work.
- DB errors: many handlers call `a.db.GetConnection()` directly for ad-hoc queries — search logs for `QueryRow` usage when debugging SQL.
- When changing long-running logic, prefer background-queue + job status updates rather than blocking the HTTP handler.

### Where to add tests & small improvements
- Unit tests: place under the package's folder (e.g. `internal/graphrag/*_test.go`). Focus on `HybridSearchEngine` fusion logic and `cv_parser` extraction edge-cases.
- Integration: `scripts/integration_test.sh` exists — follow it for end-to-end expectations.

### Quick mapping: what to change if... (1-liners)
- You need to change upload size/type validation → edit `internal/api/cv_handler.go` (ParseMultipartForm size and extension checks).
- You need to change hybrid weights default or validation → edit `graphrag.DefaultHybridConfig()` and `internal/api/hybrid_handler.go`.
- You need to add a new external LLM provider → implement adapter in `internal/llm/service.go` and wire in `internal/api/handler.go`.

If any section is unclear or you want examples expanded (e.g., key SQL schema points, background worker flow, or exact embedding call sequences), tell me which area to expand and I'll update this file.
