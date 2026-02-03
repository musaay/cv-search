package api

import (
	"context"
	"fmt"
	"log"
	"time"
)

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

	log.Println("[BackgroundJobs] Workers started (CV processing + embeddings)")
}

// embeddingWorker processes embedding jobs from the queue
func (a *API) embeddingWorker() {
	log.Println("[EmbeddingWorker] Started")

	for job := range a.embeddingQueue {
		log.Printf("[EmbeddingWorker] Processing job for CV %d (%d nodes)", job.CVID, len(job.NodeIDs))

		ctx := context.Background()

		// Check if enhanced search engine is available
		if a.enhancedSearchEngine == nil || a.enhancedSearchEngine.GetEmbeddingService() == nil {
			log.Printf("[EmbeddingWorker] Enhanced search engine not available, skipping embeddings for CV %d", job.CVID)
			continue
		}

		embeddingService := a.enhancedSearchEngine.GetEmbeddingService()

		// Embed each node with rate limiting
		successCount := 0
		failCount := 0

		for i, nodeID := range job.NodeIDs {
			err := embeddingService.EmbedNode(ctx, nodeID)
			if err != nil {
				log.Printf("[EmbeddingWorker] Failed to embed node %s: %v", nodeID, err)
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
				log.Printf("[EmbeddingWorker] Progress: %d/%d nodes embedded", i+1, len(job.NodeIDs))
			}
		}

		duration := time.Since(job.Timestamp)
		log.Printf("[EmbeddingWorker] Completed CV %d: %d success, %d failed (took %v)",
			job.CVID, successCount, failCount, duration)
	}
}

// cvProcessingWorker processes CV upload jobs from the queue
func (a *API) cvProcessingWorker() {
	log.Println("[CVProcessingWorker] Started")

	for job := range a.cvProcessingQueue {
		log.Printf("[CVProcessingWorker] Processing job %d (CV file %d)", job.JobID, job.CVFileID)

		ctx := context.Background()

		// Update job status to processing
		if err := a.db.UpdateJobStatus(ctx, job.JobID, "processing", nil); err != nil {
			log.Printf("[CVProcessingWorker] Failed to update job status: %v", err)
			continue
		}

		// Check if LLM service is available
		if a.llmService == nil {
			errMsg := "LLM service not available"
			log.Printf("[CVProcessingWorker] Job %d failed: %s", job.JobID, errMsg)
			a.db.UpdateJobStatus(ctx, job.JobID, "failed", &errMsg)
			continue
		}

		// Extract entities using LLM
		log.Printf("[CVProcessingWorker] Extracting entities for job %d...", job.JobID)
		extraction, err := a.llmService.ExtractEntities(job.CVText)
		if err != nil {
			errMsg := fmt.Sprintf("LLM extraction failed: %v", err)
			log.Printf("[CVProcessingWorker] Job %d failed: %s", job.JobID, errMsg)
			a.db.UpdateJobStatus(ctx, job.JobID, "failed", &errMsg)
			continue
		}

		log.Printf("[CVProcessingWorker] Job %d: Extracted %d skills, %d companies, %d education entries",
			job.JobID, len(extraction.Skills), len(extraction.Companies), len(extraction.Education))

		// Save extracted entities to cv_entities table
		for _, skill := range extraction.Skills {
			_ = a.db.SaveCVEntity(ctx, int(job.CVFileID), "skill", skill.Name, skill.Confidence)
		}
		for _, company := range extraction.Companies {
			_ = a.db.SaveCVEntity(ctx, int(job.CVFileID), "company", company.Name, company.Confidence)
		}
		for _, edu := range extraction.Education {
			_ = a.db.SaveCVEntity(ctx, int(job.CVFileID), "education", edu.Institution, 0.9)
		}
		for _, loc := range extraction.Locations {
			_ = a.db.SaveCVEntity(ctx, int(job.CVFileID), "location", loc, 0.85)
		}

		// Build graph from extraction
		if a.graphBuilder != nil {
			log.Printf("[CVProcessingWorker] Building knowledge graph for job %d...", job.JobID)

			// Convert extraction to map for graph builder
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

			err = a.graphBuilder.BuildFromLLMExtraction(ctx, int(job.CVFileID), extractionMap)
			if err != nil {
				log.Printf("[CVProcessingWorker] Graph building failed for job %d: %v", job.JobID, err)
			} else {
				log.Printf("[CVProcessingWorker] Job %d: Graph built successfully", job.JobID)

				// Queue background embedding job for newly created nodes
				newNodeIDs := a.collectNewNodeIDs(ctx, job.CVFileID)
				if len(newNodeIDs) > 0 {
					a.QueueEmbeddingJob(job.CVFileID, newNodeIDs)
					log.Printf("[CVProcessingWorker] Job %d: Queued %d nodes for embedding", job.JobID, len(newNodeIDs))
				}
			}
		}

		// Mark job as completed
		if err := a.db.UpdateJobStatus(ctx, job.JobID, "completed", nil); err != nil {
			log.Printf("[CVProcessingWorker] Failed to mark job %d as completed: %v", job.JobID, err)
		}

		duration := time.Since(job.Timestamp)
		log.Printf("[CVProcessingWorker] Job %d completed successfully (took %v)", job.JobID, duration)
	}
}

// queueCVProcessingJob adds a new CV processing job to the background queue
func (a *API) queueCVProcessingJob(jobID, cvFileID int64, cvText string) {
	if a.cvProcessingQueue == nil {
		log.Printf("[BackgroundJobs] CV processing queue not initialized, skipping job %d", jobID)
		return
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
		log.Printf("[BackgroundJobs] Queued CV processing job %d (CV file %d)", jobID, cvFileID)
	default:
		log.Printf("[BackgroundJobs] Queue full! Dropping CV processing job %d", jobID)
		// Update job status to failed
		ctx := context.Background()
		errMsg := "Queue full, job dropped"
		a.db.UpdateJobStatus(ctx, jobID, "failed", &errMsg)
	}
}

// QueueEmbeddingJob adds a new embedding job to the background queue
func (a *API) QueueEmbeddingJob(cvID int64, nodeIDs []string) {
	if a.embeddingQueue == nil {
		log.Printf("[BackgroundJobs] Embedding queue not initialized, skipping CV %d", cvID)
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
		log.Printf("[BackgroundJobs] Queued embedding job for CV %d (%d nodes)", cvID, len(nodeIDs))
	default:
		log.Printf("[BackgroundJobs] Queue full! Dropping embedding job for CV %d", cvID)
	}
}
