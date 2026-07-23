// Package reprocess contains the shared CV re-processing logic used both by
// the standalone cmd/tools/reprocess_cvs CLI and by the API server's optional
// startup job (gated by the RUN_REPROCESS_JOB env var — see cmd/api/main.go).
//
// It finds two kinds of broken/incomplete records and re-runs LLM extraction
// + graph building for them:
//  1. Existing candidates with weak data (few skills / few graph edges).
//  2. CVs that never got a candidate at all — the common case after a Groq
//     rate-limit backlog: cv_files.parsed_text exists, but nothing downstream
//     (cv_entities, graph_nodes, candidates) was ever built because
//     extraction failed outright.
//
// Safe to re-run: once a record is fixed, it no longer matches the "broken"
// criteria and is skipped on the next pass — so a partial/interrupted run can
// simply be re-run to pick up where it left off.
package reprocess

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"cv-search/internal/graphrag"
	"cv-search/internal/llm"
	"cv-search/internal/storage"
)

// Options controls a single reprocess run.
type Options struct {
	// DryRun, if true, only reports what would be changed — no writes.
	DryRun bool
	// OnlyCandidateID, if > 0, restricts the "weak candidate" pass to a
	// single candidate. Ignored for the backlog (never-processed) pass.
	OnlyCandidateID int
	// LLMProvider decides whether the Groq Batch API path is attempted for
	// large runs (only supported for "groq").
	LLMProvider string
	// BatchThreshold: above this many items, a Groq Batch API submission is
	// attempted before falling back to synchronous processing.
	BatchThreshold int
	// DisableBatchAPI, if true, skips attempting the Groq Batch API entirely
	// and goes straight to synchronous processing. Useful when the account
	// plan doesn't support Batch API — avoids a pointless submit-then-403
	// round trip on every run.
	DisableBatchAPI bool
}

type brokenCandidate struct {
	CandID       int
	Name         string
	GraphNodeID  int
	GraphNodeStr string
	CVIDStr      string
	EdgeCount    int64
	SkillsLen    int
}

// Run executes one reprocess pass. Returns an error only for fatal setup
// problems (e.g. the initial query failing); per-item failures are logged and
// skipped so one bad record can't abort the whole run.
func Run(ctx context.Context, db *storage.DB, llmSvc *llm.Service, graphBuilder *graphrag.GraphBuilder, embeddingSvc *graphrag.EmbeddingService, opts Options) error {
	if opts.BatchThreshold <= 0 {
		opts.BatchThreshold = 15
	}

	whereExtra := ""
	if opts.OnlyCandidateID > 0 {
		whereExtra = fmt.Sprintf(" AND c.id = %d", opts.OnlyCandidateID)
	}

	q := fmt.Sprintf(`
		SELECT
			c.id,
			c.name,
			gn.id            AS graph_node_id,
			gn.node_id       AS graph_node_str,
			gn.properties->>'cv_id' AS cv_id_str,
			(SELECT count(*) FROM graph_edges ge WHERE ge.source_node_id = gn.id) AS edge_count,
			COALESCE(length(c.skills), 0) AS skills_len
		FROM candidates c
		JOIN graph_nodes gn ON gn.id = c.graph_node_id
		WHERE (c.skills IS NULL OR length(c.skills) < 20
		       OR (SELECT count(*) FROM graph_edges ge WHERE ge.source_node_id = gn.id) < 3)
		%s
		ORDER BY c.id`, whereExtra)

	rows, err := db.GetConnection().QueryContext(ctx, q)
	if err != nil {
		return fmt.Errorf("weak-candidate query failed: %w", err)
	}

	var broken []brokenCandidate
	for rows.Next() {
		var bc brokenCandidate
		var cvIDStr *string
		if err := rows.Scan(&bc.CandID, &bc.Name, &bc.GraphNodeID, &bc.GraphNodeStr, &cvIDStr, &bc.EdgeCount, &bc.SkillsLen); err != nil {
			log.Printf("[Reprocess] row scan error: %v", err)
			continue
		}
		if cvIDStr != nil {
			bc.CVIDStr = *cvIDStr
		}
		broken = append(broken, bc)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("weak-candidate query iteration failed: %w", err)
	}

	// Second source: CVs that never got a candidate at all (cv_files.candidate_id
	// IS NULL) — the common case for a rate-limit backlog. Skipped when
	// targeting a single candidate via OnlyCandidateID.
	if opts.OnlyCandidateID == 0 {
		backlogQ := `
			SELECT cf.id, cf.filename
			FROM cv_files cf
			WHERE cf.candidate_id IS NULL
			  AND cf.parsed_text IS NOT NULL AND length(cf.parsed_text) > 0
			ORDER BY cf.id`
		backlogRows, err := db.GetConnection().QueryContext(ctx, backlogQ)
		if err != nil {
			return fmt.Errorf("backlog query failed: %w", err)
		}
		var backlogCount int
		for backlogRows.Next() {
			var cvFileID int
			var filename string
			if err := backlogRows.Scan(&cvFileID, &filename); err != nil {
				log.Printf("[Reprocess] backlog row scan error: %v", err)
				continue
			}
			broken = append(broken, brokenCandidate{
				CandID:  0, // no candidate yet — created fresh during apply
				Name:    filename,
				CVIDStr: strconv.Itoa(cvFileID),
			})
			backlogCount++
		}
		backlogRows.Close()
		if err := backlogRows.Err(); err != nil {
			return fmt.Errorf("backlog query iteration failed: %w", err)
		}
		log.Printf("[Reprocess] Found %d never-processed CV(s) with no candidate (backlog)", backlogCount)
	}

	if len(broken) == 0 {
		log.Println("[Reprocess] No broken/unprocessed candidates found.")
		return nil
	}

	log.Printf("[Reprocess] Found %d broken/unprocessed candidate(s) total:", len(broken))
	// Cap the per-item listing so this doesn't flood the log pipe: printing
	// thousands of lines in under a second can exceed hosting providers'
	// log-rate limits (e.g. Railway's 500 logs/sec) and, since log.Printf
	// writes are serialized on a shared mutex, can even stall the HTTP
	// server long enough to fail health checks and get the container
	// restarted mid-run.
	const maxListedItems = 20
	for i, bc := range broken {
		if i >= maxListedItems {
			log.Printf("[Reprocess]   ... and %d more (sample above, run report only lists the first %d)", len(broken)-maxListedItems, maxListedItems)
			break
		}
		log.Printf("[Reprocess]   [cand=%d] %-25s node=%-12s edges=%d skills=%d cv_id=%q",
			bc.CandID, bc.Name, bc.GraphNodeStr, bc.EdgeCount, bc.SkillsLen, bc.CVIDStr)
	}

	if opts.DryRun {
		log.Println("[Reprocess] [DRY RUN] Set DryRun=false (or -dry-run=false) to apply fixes.")
		return nil
	}

	// Collect items with usable parsed_text before deciding how to process them.
	type reprocessItem struct {
		bc                 brokenCandidate
		cvFileID           int64
		parsedText         string
		currentCandidateID *int
	}
	var items []reprocessItem
	skippedLogged, skippedTotal := 0, 0
	logSkip := func(format string, args ...interface{}) {
		skippedTotal++
		if skippedLogged < maxListedItems {
			log.Printf(format, args...)
			skippedLogged++
		}
	}
	for _, bc := range broken {
		if bc.CVIDStr == "" {
			logSkip("[Reprocess] [cand=%d] %s SKIP: no cv_id in graph_node — needs manual re-upload", bc.CandID, bc.Name)
			continue
		}
		cvFileID, err := strconv.ParseInt(bc.CVIDStr, 10, 64)
		if err != nil {
			logSkip("[Reprocess] [cand=%d] %s SKIP: invalid cv_id %q", bc.CandID, bc.Name, bc.CVIDStr)
			continue
		}

		var parsedText string
		var currentCandidateID *int
		err = db.GetConnection().QueryRowContext(ctx,
			"SELECT COALESCE(parsed_text,''), candidate_id FROM cv_files WHERE id=$1",
			cvFileID,
		).Scan(&parsedText, &currentCandidateID)
		if err != nil {
			logSkip("[Reprocess] [cand=%d] %s SKIP: cv_files id=%d not found: %v", bc.CandID, bc.Name, cvFileID, err)
			continue
		}
		if parsedText == "" {
			logSkip("[Reprocess] [cand=%d] %s SKIP: no parsed_text for cv_files id=%d — needs re-upload", bc.CandID, bc.Name, cvFileID)
			continue
		}

		items = append(items, reprocessItem{bc: bc, cvFileID: cvFileID, parsedText: parsedText, currentCandidateID: currentCandidateID})
	}
	if skippedTotal > skippedLogged {
		log.Printf("[Reprocess] ... %d more skipped (not logged individually)", skippedTotal-skippedLogged)
	}

	if len(items) == 0 {
		log.Println("[Reprocess] No CVs with usable parsed_text to reprocess.")
		return nil
	}

	// Above this many CVs, submit as a single Groq Batch API job instead of
	// looping synchronously — the Batch API doesn't count against the
	// standard per-model rate limit (separate quota) and is 50% cheaper.
	// Small runs stay synchronous (already self-throttled by llm.Service's
	// built-in rate limiter).
	//
	// Each successful extraction is applied (graph/candidate/embedding) to
	// the DB immediately, one item at a time, instead of collecting all
	// extractions first and writing everything at the end. For a run this
	// size (thousands of items, potentially hours), applying incrementally
	// means a mid-run restart/crash only loses the item in flight — not all
	// progress made so far.
	itemsByCVFileID := make(map[int64]reprocessItem, len(items))
	for _, it := range items {
		itemsByCVFileID[it.cvFileID] = it
	}
	applied := make(map[int64]bool, len(items))
	fixed, failed := 0, 0

	applyOne := func(it reprocessItem, extraction *llm.CVExtraction) {
		applied[it.cvFileID] = true

		log.Printf("[Reprocess] == [cand=%d] %s ==", it.bc.CandID, it.bc.Name)
		log.Printf("[Reprocess]   Got: %d skills, %d companies, %d education",
			len(extraction.Skills), len(extraction.Companies), len(extraction.Education))

		extractMap := map[string]interface{}{
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
		if err := graphBuilder.BuildFromLLMExtraction(ctx, int(it.cvFileID), extractMap); err != nil {
			log.Printf("[Reprocess]   graph build failed: %v", err)
			failed++
			return
		}
		log.Printf("[Reprocess]   Graph OK")

		resolvedName := extraction.Candidate.Name
		if resolvedName == "" {
			resolvedName = it.bc.Name
		}
		graphNodeID, err := db.GetPersonGraphNodeIDByName(ctx, resolvedName)
		if err != nil || graphNodeID == 0 {
			graphNodeID = it.bc.GraphNodeID
			log.Printf("[Reprocess]   Using existing node %d (name lookup failed for %q)", graphNodeID, resolvedName)
		}

		candidateID, err := db.UpsertCandidateForGraphNode(ctx, graphNodeID, resolvedName)
		if err != nil {
			log.Printf("[Reprocess]   UpsertCandidateForGraphNode failed: %v", err)
			failed++
			return
		}
		log.Printf("[Reprocess]   Candidate id=%d", candidateID)

		if it.currentCandidateID == nil {
			if err := db.UpdateCVFileCandidateID(ctx, it.cvFileID, candidateID); err != nil {
				log.Printf("[Reprocess]   WARNING: link cv_files failed: %v", err)
			} else {
				log.Printf("[Reprocess]   cv_files id=%d -> candidate %d linked", it.cvFileID, candidateID)
			}
		}

		if err := db.SyncCandidateTextFields(ctx, candidateID, graphNodeID); err != nil {
			log.Printf("[Reprocess]   WARNING: sync failed: %v", err)
		} else {
			log.Printf("[Reprocess]   BM25 fields synced")
		}

		// Fetch the actual node_id string fresh (rather than relying on a
		// possibly-stale value from before the rebuild — matters especially
		// for backlog items, which never had a graph node before this run).
		var nodeIDStr string
		if err := db.GetConnection().QueryRowContext(ctx,
			"SELECT node_id FROM graph_nodes WHERE id = $1", graphNodeID,
		).Scan(&nodeIDStr); err != nil {
			log.Printf("[Reprocess]   WARNING: could not resolve node_id for embedding: %v", err)
		} else {
			log.Printf("[Reprocess]   Re-embedding %s...", nodeIDStr)
			embedCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := embeddingSvc.EmbedNode(embedCtx, nodeIDStr); err != nil {
				log.Printf("[Reprocess]   WARNING: embed failed: %v", err)
			} else {
				log.Printf("[Reprocess]   Embedded OK")
			}
			cancel()
		}

		log.Printf("[Reprocess] DONE [cand=%d] %s", candidateID, it.bc.Name)
		fixed++
	}

	if !opts.DisableBatchAPI && opts.LLMProvider == "groq" && len(items) > opts.BatchThreshold {
		log.Printf("[Reprocess] Submitting %d CVs as a Groq Batch API job (threshold=%d)...", len(items), opts.BatchThreshold)
		batchItems := make(map[string]string, len(items))
		for _, it := range items {
			batchItems[fmt.Sprintf("%d", it.cvFileID)] = it.parsedText
		}

		groqBatchID, _, err := llmSvc.SubmitExtractionBatch(batchItems, "24h")
		if err != nil {
			log.Printf("[Reprocess] Groq batch submission failed, falling back to synchronous processing for all %d items: %v", len(items), err)
		} else {
			log.Printf("[Reprocess] Batch submitted: %s — polling every 30s until complete...", groqBatchID)

			var outputFileID string
			for {
				time.Sleep(30 * time.Second)
				status, err := llmSvc.GetGroqBatchStatus(groqBatchID)
				if err != nil {
					log.Printf("[Reprocess]   status check failed: %v (retrying)", err)
					continue
				}
				log.Printf("[Reprocess]   batch status=%s (%d/%d completed)", status.Status, status.RequestCounts.Completed, status.RequestCounts.Total)
				if status.Status == "completed" || status.Status == "failed" || status.Status == "expired" || status.Status == "cancelled" {
					outputFileID = status.OutputFileID
					break
				}
			}

			if outputFileID != "" {
				results, lineErrors, err := llmSvc.FetchExtractionBatchResults(outputFileID)
				if err != nil {
					log.Printf("[Reprocess]   failed to fetch batch results: %v", err)
				}
				for customID, extraction := range results {
					cvFileID, _ := strconv.ParseInt(customID, 10, 64)
					if it, ok := itemsByCVFileID[cvFileID]; ok {
						applyOne(it, extraction)
					}
				}
				loggedLineErrors := 0
				for customID, msg := range lineErrors {
					if loggedLineErrors >= maxListedItems {
						log.Printf("[Reprocess]   ... and %d more batch line failures (will retry synchronously)", len(lineErrors)-loggedLineErrors)
						break
					}
					log.Printf("[Reprocess]   batch line failed for cv_files id=%s: %s (will retry synchronously)", customID, msg)
					loggedLineErrors++
				}
			}
		}
	}

	// Anything not covered by a batch result (small run, non-Groq provider, or
	// a batch line that failed/expired) is processed synchronously — safe even
	// for the leftovers since llm.Service self-throttles. Applied immediately
	// on success, same as the batch path above.
	for _, it := range items {
		if applied[it.cvFileID] {
			continue
		}
		log.Printf("[Reprocess] [cand=%d] %s: running synchronous LLM extraction (cv_files id=%d, %d chars)...",
			it.bc.CandID, it.bc.Name, it.cvFileID, len(it.parsedText))
		extraction, err := llmSvc.ExtractEntities(it.parsedText)
		if err != nil {
			log.Printf("[Reprocess]   SKIP: extraction failed: %v", err)
			failed++
			continue
		}
		applyOne(it, extraction)
	}

	log.Printf("[Reprocess] Completed: %d fixed, %d failed (out of %d candidates found)", fixed, failed, len(broken))
	return nil
}
