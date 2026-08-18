package api

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"cv-search/internal/llm"
	"cv-search/internal/reprocess"
	"cv-search/internal/storage"
)

const communityDetectDebounce = 30 * time.Second
const groqBatchPollInterval = 2 * time.Minute

// EmbeddingJob represents a background embedding task
type EmbeddingJob struct {
	CVID      int64
	NodeIDs   []string
	Timestamp time.Time
}

// CVProcessingJob represents a background CV processing task (LLM + Graph)
type CVProcessingJob struct {
	JobID     int64
	CVFileID  int64
	CVText    string
	Timestamp time.Time
}

// StartBackgroundWorkers initializes background job workers
func (a *API) StartBackgroundWorkers() {
	// CV processing worker (LLM extraction + graph building)
	go a.cvProcessingWorker()

	// Embedding worker
	go a.embeddingWorker()

	// Groq Batch API poller (large bulk uploads / offline reprocessing)
	if a.llmService != nil {
		go a.groqBatchPollWorker()
	}

	// Scheduled Community Detection Worker (Full Rebuild)
	go a.scheduledCommunityDetectionWorker()

	slog.Info("[BackgroundJobs] Workers started (CV processing + embeddings + batch poller)")
}

// embeddingWorker processes embedding jobs from the queue
func (a *API) embeddingWorker() {
	slog.Info("[EmbeddingWorker] Started")

	for job := range a.embeddingQueue {
		slog.Info(fmt.Sprintf("[EmbeddingWorker] Processing job for CV %d (%d nodes)", job.CVID, len(job.NodeIDs)))

		ctx := context.Background()

		// Check if enhanced search engine is available
		if a.enhancedSearchEngine == nil || a.enhancedSearchEngine.GetEmbeddingService() == nil {
			slog.Info(fmt.Sprintf("[EmbeddingWorker] Enhanced search engine not available, skipping embeddings for CV %d", job.CVID))
			continue
		}

		embeddingService := a.enhancedSearchEngine.GetEmbeddingService()

		// Embed each node with rate limiting
		successCount := 0
		failCount := 0

		for i, nodeID := range job.NodeIDs {
			err := embeddingService.EmbedNode(ctx, nodeID)
			if err != nil {
				slog.Error(fmt.Sprintf("[EmbeddingWorker] Failed to embed node %s: %v", nodeID, err))
				failCount++
			} else {
				successCount++
			}

			// Rate limiting: OpenAI API throttling
			// Tier 1 (free): 3 req/min → 20 seconds
			// Tier 2 ($5+): 500 req/min → 5 req/sec (0.2s) is safe
			if i < len(job.NodeIDs)-1 {
				time.Sleep(200 * time.Millisecond)
			}

			// Progress logging every 5 nodes
			if (i+1)%5 == 0 {
				slog.Info(fmt.Sprintf("[EmbeddingWorker] Progress: %d/%d nodes embedded", i+1, len(job.NodeIDs)))
			}
		}

		duration := time.Since(job.Timestamp)
		slog.Info(fmt.Sprintf("[EmbeddingWorker] Completed CV %d: %d success, %d failed (took %v)",
			job.CVID, successCount, failCount, duration))

		// After embeddings are ready, incrementally assign the candidate to the closest community
		// to avoid full re-clustering on every upload.
		var personNodeID string
		for _, id := range job.NodeIDs {
			if strings.HasPrefix(id, "person_") {
				personNodeID = id
				break
			}
		}
		if personNodeID != "" && a.enhancedSearchEngine != nil {
			if err := a.enhancedSearchEngine.GetCommunityDetector().AssignToClosestCommunity(ctx, personNodeID); err != nil {
				slog.Error(fmt.Sprintf("[EmbeddingWorker] Failed to assign %s to community: %v", personNodeID, err))
			}
		}
	}
}

// cvProcessingWorker processes CV upload jobs from the queue
func (a *API) cvProcessingWorker() {
	slog.Info("[CVProcessingWorker] Started")

	for job := range a.cvProcessingQueue {
		slog.Info(fmt.Sprintf("[CVProcessingWorker] Processing job %d (CV file %d)", job.JobID, job.CVFileID))

		ctx := context.Background()

		// Update job status to processing
		if err := a.db.UpdateJobStatus(ctx, job.JobID, "processing", nil); err != nil {
			slog.Error(fmt.Sprintf("[CVProcessingWorker] Failed to update job status: %v", err))
			continue
		}

		// Check if LLM service is available
		if a.llmService == nil {
			errMsg := "LLM service not available"
			slog.Info(fmt.Sprintf("[CVProcessingWorker] Job %d failed: %s", job.JobID, errMsg))
			a.db.UpdateJobStatus(ctx, job.JobID, "failed", &errMsg)
			continue
		}

		// Extract entities using LLM
		slog.Info(fmt.Sprintf("[CVProcessingWorker] Extracting entities for job %d...", job.JobID))
		extraction, err := a.llmService.ExtractEntities(job.CVText)
		if err != nil {
			retryCount, maxRetries, rcErr := a.db.IncrementJobRetryCount(ctx, job.JobID)
			if rcErr == nil && retryCount < maxRetries {
				backoff := time.Duration(retryCount) * 30 * time.Second
				slog.Error(fmt.Sprintf("[CVProcessingWorker] Job %d failed (attempt %d/%d): %v — retrying in %v",
					job.JobID, retryCount, maxRetries, err, backoff))
				if statusErr := a.db.UpdateJobStatus(ctx, job.JobID, "pending", nil); statusErr != nil {
					slog.Info(fmt.Sprintf("[CVProcessingWorker] Failed to reset job %d to pending: %v", job.JobID, statusErr))
				}
				a.requeueCVProcessingJob(job, backoff)
				continue
			}
			errMsg := fmt.Sprintf("LLM extraction failed after %d attempt(s): %v", retryCount, err)
			slog.Info(fmt.Sprintf("[CVProcessingWorker] Job %d permanently failed: %s", job.JobID, errMsg))
			a.db.UpdateJobStatus(ctx, job.JobID, "failed", &errMsg)
			continue
		}

		slog.Info(fmt.Sprintf("[CVProcessingWorker] Job %d: Extracted %d skills, %d companies, %d education entries",
			job.JobID, len(extraction.Skills), len(extraction.Companies), len(extraction.Education)))

		a.applyExtraction(ctx, job.JobID, job.CVFileID, extraction)

		duration := time.Since(job.Timestamp)
		slog.Info(fmt.Sprintf("[CVProcessingWorker] Job %d completed successfully (took %v)", job.JobID, duration))
	}
}

// applyExtraction persists an LLM extraction result (entities, graph,
// candidate linking, embedding queueing) and marks the job completed. Shared
// between the real-time cvProcessingWorker and the Groq Batch API poller so
// both paths apply identical downstream logic regardless of how the
// extraction was obtained.
func (a *API) applyExtraction(ctx context.Context, jobID, cvFileID int64, extraction *llm.CVExtraction) {
	// Save extracted entities to cv_entities table in batch
	var batchItems []storage.CVEntityBatchItem
	for _, skill := range extraction.Skills {
		batchItems = append(batchItems, storage.CVEntityBatchItem{Type: "skill", Value: skill.Name, Confidence: skill.Confidence})
	}
	for _, company := range extraction.Companies {
		batchItems = append(batchItems, storage.CVEntityBatchItem{Type: "company", Value: company.Name, Confidence: company.Confidence})
	}
	for _, edu := range extraction.Education {
		batchItems = append(batchItems, storage.CVEntityBatchItem{Type: "education", Value: edu.Institution, Confidence: 0.9})
	}
	for _, loc := range extraction.Locations {
		batchItems = append(batchItems, storage.CVEntityBatchItem{Type: "location", Value: loc, Confidence: 0.85})
	}
	_ = a.db.SaveCVEntitiesBatch(ctx, int(cvFileID), batchItems)

	// Build graph from extraction
	if a.graphBuilder != nil {
		slog.Info(fmt.Sprintf("[ApplyExtraction] Building knowledge graph for job %d...", jobID))

		extractionMap := map[string]interface{}{
			"candidate": map[string]interface{}{
				"name":                   extraction.Candidate.Name,
				"current_position":       extraction.Candidate.CurrentPosition,
				"seniority":              extraction.Candidate.Seniority,
				"total_experience_years": extraction.Candidate.TotalExperienceYears,
			},
			"skills":    extraction.Skills,
			"companies": extraction.Companies,
			"education": extraction.Education,
		}

		if newNodeIDs, err := a.graphBuilder.BuildFromLLMExtraction(ctx, int(cvFileID), extractionMap); err != nil {
			slog.Error(fmt.Sprintf("[ApplyExtraction] Graph building failed for job %d: %v", jobID, err))
		} else {
			slog.Info(fmt.Sprintf("[ApplyExtraction] Job %d: Graph built successfully", jobID))

			// Link candidate record to the newly built person graph node
			candidateName := extraction.Candidate.Name
			if candidateName != "" {
				personNodeID, lookupErr := a.db.GetPersonGraphNodeIDByName(ctx, candidateName)
				if lookupErr != nil {
					slog.Info(fmt.Sprintf("[ApplyExtraction] Job %d: Failed to look up person node: %v", jobID, lookupErr))
				} else if personNodeID > 0 {
					candidateID, upsertErr := a.db.UpsertCandidateForGraphNode(ctx, personNodeID, candidateName)
					if upsertErr != nil {
						slog.Info(fmt.Sprintf("[ApplyExtraction] Job %d: Failed to upsert candidate: %v", jobID, upsertErr))
					} else {
						if linkErr := a.db.UpdateCVFileCandidateID(ctx, cvFileID, candidateID); linkErr != nil {
							slog.Info(fmt.Sprintf("[ApplyExtraction] Job %d: Failed to link cv_file to candidate: %v", jobID, linkErr))
						}
						// Sync experience + skills into candidates for BM25 search
						if syncErr := a.db.SyncCandidateTextFields(ctx, candidateID, personNodeID); syncErr != nil {
							slog.Info(fmt.Sprintf("[ApplyExtraction] Job %d: Failed to sync candidate text fields: %v", jobID, syncErr))
						}
						slog.Info(fmt.Sprintf("[ApplyExtraction] Job %d: Candidate %d linked to node %d", jobID, candidateID, personNodeID))
					}
				}
			}

			// Queue background embedding job for newly created nodes.
			// Only "person" nodes are embedded — vector search only ever queries
			// graph_nodes WHERE node_type='person' (see embeddings.go SimilaritySearch).
			// Skill/company/education nodes are matched structurally via graph_edges
			// (querier.go), never by embedding similarity, so embedding them wastes
			// OpenAI API calls and bloats the graph_nodes table/HNSW index for no benefit.
			personNodeIDs := filterPersonNodeIDs(newNodeIDs)
			if len(personNodeIDs) > 0 {
				a.QueueEmbeddingJob(cvFileID, personNodeIDs)
				slog.Info(fmt.Sprintf("[ApplyExtraction] Job %d: Queued %d person node(s) for embedding (skipped %d non-person nodes)",
					jobID, len(personNodeIDs), len(newNodeIDs)-len(personNodeIDs)))
			}
		}
	}

	// Mark job as completed
	if err := a.db.UpdateJobStatus(ctx, jobID, "completed", nil); err != nil {
		slog.Error(fmt.Sprintf("[ApplyExtraction] Failed to mark job %d as completed: %v", jobID, err))
	}
}

// queueCVProcessingJob adds a new CV processing job to the background queue.
// Returns true if the job was queued, false if the queue was full.
func (a *API) queueCVProcessingJob(jobID, cvFileID int64, cvText string) bool {
	if a.cvProcessingQueue == nil {
		slog.Info(fmt.Sprintf("[BackgroundJobs] CV processing queue not initialized, skipping job %d", jobID))
		return false
	}

	job := CVProcessingJob{
		JobID:     jobID,
		CVFileID:  cvFileID,
		CVText:    cvText,
		Timestamp: time.Now(),
	}

	// Non-blocking send
	select {
	case a.cvProcessingQueue <- job:
		slog.Info(fmt.Sprintf("[BackgroundJobs] Queued CV processing job %d (CV file %d)", jobID, cvFileID))
		return true
	default:
		slog.Info(fmt.Sprintf("[BackgroundJobs] Queue full! Dropping CV processing job %d", jobID))
		// Update job status to failed
		ctx := context.Background()
		errMsg := "Queue full, job dropped"
		a.db.UpdateJobStatus(ctx, jobID, "failed", &errMsg)
		return false
	}
}

// requeueCVProcessingJob re-queues a job after a backoff delay (system-level
// retry, distinct from Groq's own internal retry inside callGroq). Runs in a
// separate goroutine so the worker isn't blocked while waiting.
func (a *API) requeueCVProcessingJob(job CVProcessingJob, delay time.Duration) {
	go func() {
		time.Sleep(delay)

		select {
		case a.cvProcessingQueue <- job:
			slog.Info(fmt.Sprintf("[BackgroundJobs] Requeued CV processing job %d after %v backoff", job.JobID, delay))
		default:
			slog.Info(fmt.Sprintf("[BackgroundJobs] Queue full on requeue! Dropping CV processing job %d", job.JobID))
			ctx := context.Background()
			errMsg := "Queue full on retry, job dropped"
			a.db.UpdateJobStatus(ctx, job.JobID, "failed", &errMsg)
		}
	}()
}

// filterPersonNodeIDs keeps only "person_*" node IDs, dropping skill/company/education
// nodes which are never read via vector similarity search (they're matched via graph_edges
// instead — see GraphQuerier.buildQuery in querier.go).
func filterPersonNodeIDs(nodeIDs []string) []string {
	filtered := make([]string, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		if strings.HasPrefix(id, "person_") {
			filtered = append(filtered, id)
		}
	}
	return filtered
}

// QueueEmbeddingJob adds a new embedding job to the background queue
func (a *API) QueueEmbeddingJob(cvID int64, nodeIDs []string) {
	if a.embeddingQueue == nil {
		slog.Info(fmt.Sprintf("[BackgroundJobs] Embedding queue not initialized, skipping CV %d", cvID))
		return
	}

	job := EmbeddingJob{
		CVID:      cvID,
		NodeIDs:   nodeIDs,
		Timestamp: time.Now(),
	}

	// Non-blocking send
	select {
	case a.embeddingQueue <- job:
		slog.Info(fmt.Sprintf("[BackgroundJobs] Queued embedding job for CV %d (%d nodes)", cvID, len(nodeIDs)))
	default:
		slog.Info(fmt.Sprintf("[BackgroundJobs] Queue full! Dropping embedding job for CV %d", cvID))
	}
}

// RunReprocessJob runs the shared CV backlog reprocessing pass using this
// API's already-constructed graphBuilder/embeddingService. By default it
// reuses the SAME llm.Service instance (and therefore the SAME rate limiter)
// used by search and the real-time CV upload path — avoiding a second,
// uncoordinated rate limiter that could combine with live traffic to exceed
// Groq's per-model RPM limit. Pass a non-nil llmSvcOverride (e.g. an
// OpenAI-backed Service) to run the job against a different provider
// entirely — safe to do since it doesn't share Groq's quota either way.
func (a *API) RunReprocessJob(ctx context.Context, llmSvcOverride *llm.Service, opts reprocess.Options) error {
	llmSvc := a.llmService
	if llmSvcOverride != nil {
		llmSvc = llmSvcOverride
	}
	if llmSvc == nil {
		return fmt.Errorf("LLM service not available")
	}
	if a.enhancedSearchEngine == nil || a.enhancedSearchEngine.GetEmbeddingService() == nil {
		return fmt.Errorf("embedding service not available")
	}
	return reprocess.Run(ctx, a.db, llmSvc, a.graphBuilder, a.enhancedSearchEngine.GetEmbeddingService(), opts)
}

// SubmitCVExtractionBatch submits a set of CV extraction jobs as a single Groq
// Batch API job instead of queuing them individually into the real-time
// worker. Used for bulk uploads above MaxRealtimeCVCount — trades a few
// minutes-to-hours of latency for immunity to the standard per-model rate
// limit (Batch API is a separate quota) at half the cost. Returns the Groq
// batch ID for tracking.
func (a *API) SubmitCVExtractionBatch(ctx context.Context, jobs []CVProcessingJob) (string, error) {
	if a.llmService == nil {
		return "", fmt.Errorf("LLM service not available")
	}
	if len(jobs) == 0 {
		return "", fmt.Errorf("no jobs to submit")
	}

	items := make(map[string]string, len(jobs))
	jobIDs := make([]int64, 0, len(jobs))
	for _, j := range jobs {
		items[fmt.Sprintf("%d", j.CVFileID)] = j.CVText
		jobIDs = append(jobIDs, j.JobID)
	}

	groqBatchID, inputFileID, err := a.llmService.SubmitExtractionBatch(items, "24h")
	if err != nil {
		return "", fmt.Errorf("failed to submit Groq batch: %w", err)
	}

	if _, dbErr := a.db.CreateGroqBatchJob(ctx, groqBatchID, inputFileID, len(items)); dbErr != nil {
		slog.Info(fmt.Sprintf("[GroqBatch] Warning: failed to record batch job %s: %v", groqBatchID, dbErr))
	}
	if dbErr := a.db.LinkJobsToGroqBatch(ctx, groqBatchID, jobIDs); dbErr != nil {
		slog.Info(fmt.Sprintf("[GroqBatch] Warning: failed to link jobs to batch %s: %v", groqBatchID, dbErr))
	}

	slog.Info(fmt.Sprintf("[GroqBatch] Submitted batch %s with %d CVs (input_file=%s)", groqBatchID, len(items), inputFileID))
	return groqBatchID, nil
}

// groqBatchPollWorker periodically checks in-flight Groq Batch API jobs and,
// once a batch completes, applies its results through the same downstream
// pipeline as the real-time worker (applyExtraction). Self-healing: any CV
// missing or failed within the batch falls back to the normal real-time queue
// instead of getting stuck.
func (a *API) groqBatchPollWorker() {
	slog.Info("[GroqBatchPoller] Started")
	ticker := time.NewTicker(groqBatchPollInterval)
	defer ticker.Stop()

	for range ticker.C {
		ctx := context.Background()
		batches, err := a.db.ListOpenGroqBatchJobs(ctx)
		if err != nil {
			slog.Error(fmt.Sprintf("[GroqBatchPoller] Failed to list open batches: %v", err))
			continue
		}
		for _, b := range batches {
			a.pollGroqBatch(ctx, b.GroqBatchID)
		}
	}
}

// pollGroqBatch checks and, if complete, applies the results of a single
// Groq batch job.
func (a *API) pollGroqBatch(ctx context.Context, groqBatchID string) {
	status, err := a.llmService.GetGroqBatchStatus(groqBatchID)
	if err != nil {
		slog.Error(fmt.Sprintf("[GroqBatchPoller] Failed to get status for batch %s: %v", groqBatchID, err))
		return
	}

	var outputFileID, errorFileID *string
	if status.OutputFileID != "" {
		outputFileID = &status.OutputFileID
	}
	if status.ErrorFileID != "" {
		errorFileID = &status.ErrorFileID
	}
	if dbErr := a.db.UpdateGroqBatchJobStatus(ctx, groqBatchID, status.Status, outputFileID, errorFileID); dbErr != nil {
		slog.Info(fmt.Sprintf("[GroqBatchPoller] Failed to update batch %s status: %v", groqBatchID, dbErr))
	}

	slog.Info(fmt.Sprintf("[GroqBatchPoller] Batch %s status=%s (%d/%d completed)",
		groqBatchID, status.Status, status.RequestCounts.Completed, status.RequestCounts.Total))

	terminal := status.Status == "completed" || status.Status == "failed" ||
		status.Status == "expired" || status.Status == "cancelled"
	if !terminal {
		return // still in progress, check again next tick
	}

	jobsByCVFileID, err := a.db.GetJobsByGroqBatchID(ctx, groqBatchID)
	if err != nil {
		slog.Error(fmt.Sprintf("[GroqBatchPoller] Failed to load jobs for batch %s: %v", groqBatchID, err))
		return
	}

	var results map[string]*llm.CVExtraction
	var lineErrors map[string]string
	if status.OutputFileID != "" {
		results, lineErrors, err = a.llmService.FetchExtractionBatchResults(status.OutputFileID)
		if err != nil {
			slog.Error(fmt.Sprintf("[GroqBatchPoller] Failed to fetch results for batch %s: %v", groqBatchID, err))
		}
	}

	for cvFileID, jobID := range jobsByCVFileID {
		customID := fmt.Sprintf("%d", cvFileID)

		if extraction, ok := results[customID]; ok {
			slog.Info(fmt.Sprintf("[GroqBatchPoller] Applying batch result for job %d (CV %d)", jobID, cvFileID))
			a.applyExtraction(ctx, jobID, cvFileID, extraction)
			continue
		}

		// Missing or errored in the batch — self-heal via the real-time queue
		// instead of leaving the job stuck in "batch_submitted".
		if msg, ok := lineErrors[customID]; ok {
			slog.Info(fmt.Sprintf("[GroqBatchPoller] Batch line failed for job %d (CV %d): %s — falling back to real-time queue", jobID, cvFileID, msg))
		} else {
			slog.Info(fmt.Sprintf("[GroqBatchPoller] No result for job %d (CV %d) in batch %s — falling back to real-time queue", jobID, cvFileID, groqBatchID))
		}

		texts, textErr := a.db.GetCVTextsByFileIDs(ctx, []int64{cvFileID})
		if textErr != nil || texts[cvFileID] == "" {
			errMsg := "batch extraction failed and CV text unavailable for fallback"
			a.db.UpdateJobStatus(ctx, jobID, "failed", &errMsg)
			continue
		}
		a.db.UpdateJobStatus(ctx, jobID, "pending", nil)
		a.queueCVProcessingJob(jobID, cvFileID, texts[cvFileID])
	}
}

// scheduledCommunityDetectionWorker runs a full community detection pass once a day at 03:00 AM.
func (a *API) scheduledCommunityDetectionWorker() {
	if a.enhancedSearchEngine == nil {
		return // community detection requires LLM to be configured
	}

	for {
		now := time.Now()
		// Target 03:00 AM local time
		target := time.Date(now.Year(), now.Month(), now.Day(), 3, 0, 0, 0, now.Location())
		if now.After(target) {
			target = target.Add(24 * time.Hour)
		}

		sleepDuration := target.Sub(now)
		slog.Info(fmt.Sprintf("[ScheduledCommunityDetect] Next full rebuild scheduled at %v (in %v)", target.Format(time.RFC3339), sleepDuration))

		time.Sleep(sleepDuration)

		slog.Info(fmt.Sprintf("[ScheduledCommunityDetect] Starting nightly full rebuild..."))
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)

		a.commDetectMu.Lock()
		a.lastCommDetect = time.Now()
		a.commDetectMu.Unlock()

		if err := a.enhancedSearchEngine.GetCommunityDetector().DetectCommunities(ctx, 0); err != nil {
			slog.Error(fmt.Sprintf("[ScheduledCommunityDetect] Failed: %v", err))
		} else {
			slog.Info(fmt.Sprintf("[ScheduledCommunityDetect] Completed successfully"))
		}

		// Force the Go garbage collector to immediately return memory back to the OS
		// after this massive memory-intensive clustering job, keeping our baseline low.
		debug.FreeOSMemory()

		cancel()
	}
}
