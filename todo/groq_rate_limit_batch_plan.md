# Plan: Groq Rate-Limit Fix + Bulk CV Batch Pipeline

## Context / Root cause findings

- Single shared `llm.Service` instance (`internal/api/handler.go:109`) is used by search (analyzer.go, llm_scorer.go) AND the CV extraction background worker. All Groq calls go through `callGroq()` in `internal/llm/service.go:330-440`.
- **Critical bug**: in `callGroq`, on HTTP 429, if `Retry-After` (or computed backoff) > 3s, the function **aborts immediately** with an error instead of waiting (`internal/llm/service.go` ~line 395: `if waitDur > 3*time.Second { return "", err }`). This was written for interactive search latency, but the SAME function is used by the async CV background worker, where a longer wait would be totally fine. This is very likely the direct cause of the reported rate-limit errors.
- Model in use: `groq / llama-3.3-70b-versatile`. Groq's published limits for this model (console.groq.com/docs/rate-limits): **RPM 30, RPD 1,000, TPM 12,000, TPD 100,000**. TPM=12K is shared org-wide across search + CV parsing + tools — very easy to exhaust with bulk uploads (a CV extraction prompt+completion can be ~3-5K tokens → only ~3-4 CVs/minute sustainable via TPM alone).
- Pricing (groq.com/pricing): llama-3.3-70b-versatile = $0.59/M input tokens, $0.79/M output tokens. Groq **Batch API**: 50% cheaper, and **does not consume standard per-model rate limit budget at all** (separate pool), but results land within 24h-7d (usually much faster), not instantly.
- `cvProcessingWorker` (`internal/api/background_jobs.go`) is a single goroutine processing `cvProcessingQueue` (buffer 50) strictly sequentially — good (no concurrency burst), but has **no throttle/sleep** between jobs, so a bulk upload of N CVs fires N Groq calls back-to-back as fast as Groq responds.
- `cmd/tools/reprocess_cvs/main.go` has **zero rate limiting** — loops calling `llmSvc.ExtractEntities` with no sleep. High risk tool given the size of the DB.
- `cmd/tools/backfill_positions/main.go` and `detect_communities/main.go` already have naive `time.Sleep(300ms)` between calls — works today but not adaptive to real limits.
- `cv_upload_jobs` table already has unused `retry_count` / `max_retries` columns (schema exists, logic doesn't use them) — background worker marks job "failed" permanently on any LLM error, no system-level retry.
- Embeddings (OpenAI) currently use fixed `200ms` sleep in `embeddingWorker` — likely fine at current volume, unconfirmed at 100-CV bulk scale. Deferred to Phase 4 (monitor first, don't pre-build).
- Bulk upload currently capped at 20 (`MAX_BULK_FILE_COUNT`); goal is to support up to 100 files at once.
- reprocess_cvs / backfill_positions do NOT require re-uploading files — they already read `cv_files.parsed_text` from Postgres. Migrating them to Batch API only changes *how* the LLM call is made, not the data source.

## Verified production impact (queried live Railway DB on 2026-07-20)

- Total `cv_files`: 2,787. `parsed_text` present: 2,786 (99.9%) — text extraction (docconv, local, no external API) unaffected by Groq issues, as expected.
- `cv_entities` present (extraction succeeded): only **282 (10%)**. Missing entities: **2,505 (90%)**.
- `cv_upload_jobs` status: failed **2,480**, completed 284, pending 21, processing 2.
- Upload timeline: Feb-Jun 2026 combined = 102 files; **July 2026 alone = 2,685 files** (the bulk import that triggered this).
- Failed job error category breakdown: **rate_limit 2,246 (90.6%)**, queue_full 222 (9%), other 12 (0.5%).
- Conclusion: this is not occasional rate-limiting — a large July bulk upload (2,685 files) overwhelmed llama-3.3-70b-versatile's 30 RPM / 12K TPM budget, causing ~90% of CVs to fail entity extraction entirely (queue_full 222 confirms the 50-job buffer also overflowed, per `queueCVProcessingJob`). All affected CVs already have `parsed_text` stored — no re-upload needed, only re-running extraction. **This elevates Phase 3 (reprocess_cvs → Groq Batch API) to high priority**: retrying ~2,505 CVs synchronously would hit the same rate limit; Batch API (separate quota, 50% cheaper) is the safe way to clear this backlog.

## Architecture: where LLMs are used today

- **CV parse (upload)**: `ExtractEntities()` → `callGroq()` — extraction, not scoring.
- **Search: query analysis**: `QueryAnalyzer.AnalyzeQuery()` → `Generate()` — Groq.
- **Search: candidate scoring**: `LLMScorer.ScoreCandidates()` → `Generate()` — Groq (this is the actual "scoring" step).
- **Embeddings** (CV nodes, query, community summaries): `GenerateEmbedding()` — OpenAI, everywhere.
- **Offline tools** (`reprocess_cvs`, `backfill_positions`): `ExtractEntities()` — Groq.
- **`detect_communities`**: `Generate()` (Groq) + embeddings (OpenAI) for community summaries.
- All Groq call sites share the same org-level TPM/RPM budget — CV parsing bursts can starve search, and vice versa.

## Phases

### Phase 1 — Critical fix + safety net (ship first, no schema changes)
1. Fix `callGroq` wait-cap bug: differentiate interactive (search: analyzer.go, llm_scorer.go — keep fail-fast ~3s so the user's HTTP request doesn't hang) vs background (CV extraction, batch tools — allow honoring Retry-After up to a higher ceiling, e.g. 60s, since these run async off a queue).
2. Add a shared proactive rate limiter on `llm.Service` (e.g. `golang.org/x/time/rate`), tuned to llama-3.3-70b-versatile's real limits (30 RPM floor; rough TPM budgeting via token estimate `len(prompt)/4`). Throttle *before* sending, not just react to 429 after the fact. Since one `llm.Service` instance is shared app-wide, this protects all callers within the process.
3. Implement system-level job retry using existing `retry_count`/`max_retries` columns in `cv_upload_jobs`: on `ExtractEntities` failure in `cvProcessingWorker`, increment retry_count; if below max, requeue with backoff instead of permanent "failed".
4. Apply the same fix to `cmd/tools/reprocess_cvs/main.go` (currently has NO throttling) by reusing `llm.Service`'s new self-throttling behavior.

### Phase 2 — Bulk import via Groq Batch API (supports up to ~100 files/run) — depends on Phase 1
1. Add batch capability behind `llm.Service` (new `internal/llm/batch.go`): build JSONL (`custom_id` = cv_file_id), upload via Files API, create batch job (`completion_window: "24h"`), poll status, download+parse results. Kept behind the same Service abstraction so callers don't know/care whether sync or batch was used — mitigates lock-in concern (batch failure always falls back to the sync path; no caller is hard-dependent on Groq Batch specifically).
2. DB: new table `llm_batch_jobs` (groq_batch_id, status, input_file_id, output_file_id, created_at, completed_at) + link cv_upload_jobs to a batch via batch_id/custom_id.
3. New background poller goroutine (registered in `StartBackgroundWorkers`) checking in-flight batches every few minutes; on completion, downloads results and reuses the existing post-extraction pipeline logic from `cvProcessingWorker` (factor into a shared helper) — SaveCVEntity, BuildFromLLMExtraction, candidate linking, embedding queueing.
4. Routing: new config `MAX_REALTIME_CV_COUNT` (default ~20-25, same ballpark as current `MAX_BULK_FILE_COUNT`). File count ≤ threshold → existing real-time queue (unchanged, fast). Above threshold → submit as Groq batch; `cv_upload_jobs` rows keep existing job_id polling contract (`pending`→`batch_submitted`→`processing`→`completed`/`failed`), so frontend polling doesn't need to change.
5. Self-healing fallback: if a batch expires or specific lines fail, automatically requeue those specific CV file IDs into the normal real-time `cvProcessingQueue` (bounded retries) — batch is never a hard dependency.
6. Raise `MAX_BULK_FILE_COUNT` default (e.g. to 100) once batch path is in place.

### Phase 3 — Migrate offline tools to Batch API — depends on Phase 2, high priority given the 2,505-CV backlog
1. `reprocess_cvs`: replace synchronous per-item loop with — fetch all broken CVs' `parsed_text` (already in DB, no re-upload needed) → build one batch (chunk into ≤1000 requests per Groq's guidance) → submit via Phase 2's batch helper → poll → apply results via existing graph-rebuild logic.
2. Same treatment for `backfill_positions`.
3. Benefit: 50% cheaper, doesn't compete with live traffic's standard rate-limit budget (Batch API tokens are a separate pool per Groq docs), much safer for clearing the existing backlog.

### Phase 4 — Embeddings scaling (optional, monitor first — do not pre-build)
- After Phase 1/2 ship, run a real ~100-CV bulk upload test and watch logs for OpenAI 429s.
- If they appear: apply same two patterns — (a) proactive token-bucket limiter shared across `embeddingWorker` + tools, (b) consider OpenAI's own Batch API (~50% cheaper, up to 24h) mirroring the Groq batch design.
- Explicitly deferred per "avoid over-engineering" project rule — don't build until there's evidence of a real problem.

## Relevant files
- `internal/llm/service.go` — `callGroq()` (~line 330), wait-cap bug, add rate limiter, add batch methods
- `internal/api/background_jobs.go` — `cvProcessingWorker()`, `embeddingWorker()`, `StartBackgroundWorkers()`, add retry + batch poller
- `internal/api/handler.go` — shared `llmService` instance (line 109), wiring for new config/poller
- `internal/config/config.go` — add `MAX_REALTIME_CV_COUNT`, raise `MAX_BULK_FILE_COUNT` default
- `internal/storage/db.go` / `internal/storage/models.go` — `CVUploadJob` retry_count/max_retries usage, new `llm_batch_jobs` table
- `migrations/complete_setup.sql` — schema for new batch tracking table
- `cmd/tools/reprocess_cvs/main.go` — no throttling today; Phase 1 quick fix + Phase 3 batch migration
- `cmd/tools/backfill_positions/main.go` — has naive sleep; Phase 3 batch migration
- `internal/graphrag/analyzer.go`, `internal/graphrag/llm_scorer.go` — interactive Groq callers (keep fail-fast behavior)
- `internal/graphrag/embeddings.go` — Phase 4 candidate

## Verification
- Trigger bulk upload of >20 dummy CVs locally; confirm no immediate rate-limit failures in logs; jobs reach "completed" (or batch states).
- `./scripts/search_tests.sh` MUST pass before any push (mandatory per repo rules).
- Manual curl test of upload endpoint with N files + poll job status endpoint to completion.
- Phase 2/3: confirm a submitted Groq batch reaches `status: completed` (via GET /batches or console), output_file_id parses correctly into graph_nodes/cv_entities.
- Confirm retry_count/max_retries increments and stops retrying after max reached (force a failure to test).

## Decisions
- Groq Batch API wrapped behind `llm.Service` interface — not a hard dependency; sync path remains the fallback for any batch failure/expiry.
- reprocess_cvs/backfill_positions need NO file re-uploads — text already in `cv_files.parsed_text`.
- Real-time/batch threshold defaults to current `MAX_BULK_FILE_COUNT` ballpark (~20-25), configurable, tune after observing real TPM consumption per CV.
- Embeddings rate limiting deferred to Phase 4, monitored empirically, not built preemptively.

## Further considerations
1. Confirm current OpenAI project tier/limits (console.openai.com) before committing to Phase 4.
2. Finalize exact real-time/batch threshold number — recommend starting at current default (20), tune via config without code changes.
