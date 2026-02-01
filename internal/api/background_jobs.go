package api

import (
	"context"
	"log"
	"time"
)

// EmbeddingJob represents a background embedding task
type EmbeddingJob struct {
	CVID      int64
	NodeIDs   []string
	Timestamp time.Time
}

// StartBackgroundWorkers initializes background job workers
func (a *API) StartBackgroundWorkers() {
	// Embedding worker
	go a.embeddingWorker()

	log.Println("[BackgroundJobs] Workers started")
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
