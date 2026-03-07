package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"cv-search/internal/graphrag"
	"cv-search/internal/llm"
	"cv-search/internal/storage"
)

func main() {
	var dryRun bool
	var onlyCandidateID int
	flag.BoolVar(&dryRun, "dry-run", true, "only report")
	flag.IntVar(&onlyCandidateID, "candidate-id", 0, "specific candidate")
	flag.Parse()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL required")
	}
	openaiKey := os.Getenv("OPENAI_API_KEY")
	if openaiKey == "" {
		log.Fatal("OPENAI_API_KEY required")
	}
	llmProvider := os.Getenv("LLM_PROVIDER")
	if llmProvider == "" {
		llmProvider = "openai"
	}
	llmModel := os.Getenv("LLM_MODEL")
	if llmModel == "" {
		llmModel = "gpt-4o-mini"
	}
	var llmAPIKey string
	if llmProvider == "groq" {
		llmAPIKey = os.Getenv("GROQ_API_KEY")
	} else {
		llmAPIKey = os.Getenv("OPENAI_API_KEY")
	}
	if llmAPIKey == "" {
		log.Fatalf("API key not set for provider %q (set GROQ_API_KEY or OPENAI_API_KEY)", llmProvider)
	}

	db, err := storage.NewDB(dbURL)
	if err != nil {
		log.Fatalf("DB: %v", err)
	}
	defer db.Close()

	llmSvc := llm.NewService(llmProvider, llmAPIKey, llmModel)
	graphBuilder := graphrag.NewGraphBuilder(db.GetConnection())
	embeddingSvc := graphrag.NewEmbeddingService(openaiKey, db.GetConnection())

	ctx := context.Background()

	whereExtra := ""
	if onlyCandidateID > 0 {
		whereExtra = fmt.Sprintf(" AND c.id = %d", onlyCandidateID)
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
		log.Fatalf("query failed: %v", err)
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

	var broken []brokenCandidate
	for rows.Next() {
		var bc brokenCandidate
		var cvIDStr *string
		if err := rows.Scan(&bc.CandID, &bc.Name, &bc.GraphNodeID, &bc.GraphNodeStr, &cvIDStr, &bc.EdgeCount, &bc.SkillsLen); err != nil {
			log.Printf("row scan error: %v", err)
			continue
		}
		if cvIDStr != nil {
			bc.CVIDStr = *cvIDStr
		}
		broken = append(broken, bc)
	}
	rows.Close()

	if len(broken) == 0 {
		log.Println("No broken candidates found.")
		return
	}

	log.Printf("Found %d broken candidate(s):", len(broken))
	for _, bc := range broken {
		log.Printf("  [cand=%d] %-25s node=%-12s edges=%d skills=%d cv_id=%q",
			bc.CandID, bc.Name, bc.GraphNodeStr, bc.EdgeCount, bc.SkillsLen, bc.CVIDStr)
	}

	if dryRun {
		log.Println("\n[DRY RUN] Re-run with -dry-run=false to apply fixes.")
		return
	}

	for _, bc := range broken {
		log.Printf("\n== [cand=%d] %s ==", bc.CandID, bc.Name)

		if bc.CVIDStr == "" {
			log.Printf("  SKIP: no cv_id in graph_node — needs manual re-upload")
			continue
		}
		cvFileID, err := strconv.ParseInt(bc.CVIDStr, 10, 64)
		if err != nil {
			log.Printf("  SKIP: invalid cv_id %q", bc.CVIDStr)
			continue
		}

		var parsedText string
		var currentCandidateID *int
		err = db.GetConnection().QueryRowContext(ctx,
			"SELECT COALESCE(parsed_text,''), candidate_id FROM cv_files WHERE id=$1",
			cvFileID,
		).Scan(&parsedText, &currentCandidateID)
		if err != nil {
			log.Printf("  SKIP: cv_files id=%d not found: %v", cvFileID, err)
			continue
		}
		if parsedText == "" {
			log.Printf("  SKIP: no parsed_text for cv_files id=%d — needs re-upload", cvFileID)
			continue
		}
		log.Printf("  cv_files id=%d (%d chars) current candidate_id=%v", cvFileID, len(parsedText), currentCandidateID)

		log.Printf("  Running LLM extraction...")
		extraction, err := llmSvc.ExtractEntities(parsedText)
		if err != nil {
			log.Printf("  SKIP: extraction failed: %v", err)
			continue
		}
		log.Printf("  Got: %d skills, %d companies, %d education",
			len(extraction.Skills), len(extraction.Companies), len(extraction.Education))

		log.Printf("  Building graph...")
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
		if err := graphBuilder.BuildFromLLMExtraction(ctx, int(cvFileID), extractMap); err != nil {
			log.Printf("  graph build failed: %v", err)
			continue
		}
		log.Printf("  Graph OK")

		resolvedName := extraction.Candidate.Name
		if resolvedName == "" {
			resolvedName = bc.Name
		}
		graphNodeID, err := db.GetPersonGraphNodeIDByName(ctx, resolvedName)
		if err != nil || graphNodeID == 0 {
			graphNodeID = bc.GraphNodeID
			log.Printf("  Using existing node %d (name lookup failed for %q)", graphNodeID, resolvedName)
		}

		candidateID, err := db.UpsertCandidateForGraphNode(ctx, graphNodeID, resolvedName)
		if err != nil {
			log.Printf("  UpsertCandidateForGraphNode failed: %v", err)
			continue
		}
		log.Printf("  Candidate id=%d", candidateID)

		if currentCandidateID == nil {
			if err := db.UpdateCVFileCandidateID(ctx, cvFileID, candidateID); err != nil {
				log.Printf("  WARNING: link cv_files failed: %v", err)
			} else {
				log.Printf("  cv_files id=%d -> candidate %d linked", cvFileID, candidateID)
			}
		}

		if err := db.SyncCandidateTextFields(ctx, candidateID, graphNodeID); err != nil {
			log.Printf("  WARNING: sync failed: %v", err)
		} else {
			log.Printf("  BM25 fields synced")
		}

		log.Printf("  Re-embedding %s...", bc.GraphNodeStr)
		embedCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := embeddingSvc.EmbedNode(embedCtx, bc.GraphNodeStr); err != nil {
			log.Printf("  WARNING: embed failed: %v", err)
		} else {
			log.Printf("  Embedded OK")
		}
		cancel()

		log.Printf("  DONE [cand=%d] %s", candidateID, bc.Name)
	}

	log.Println("\nreprocess_cvs completed.")
}
