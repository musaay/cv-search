package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"cv-search/internal/llm"
	"cv-search/internal/storage"
)

func main() {
	var dryRun bool
	var limit int
	flag.BoolVar(&dryRun, "dry-run", true, "If true, do not persist updates; just print changes")
	flag.IntVar(&limit, "limit", 200, "Max number of candidates to process in one run")
	flag.Parse()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	llmProvider := os.Getenv("LLM_PROVIDER")
	llmAPIKey := os.Getenv("LLM_API_KEY")
	llmModel := os.Getenv("LLM_MODEL")

	if llmProvider == "" || llmProvider == "none" {
		log.Fatal("LLM_PROVIDER must be set (e.g. openai|ollama|groq) and configured")
	}

	log.Printf("Connecting to DB...")
	db, err := storage.NewDB(dbURL)
	if err != nil {
		log.Fatalf("failed to connect to db: %v", err)
	}
	defer db.Close()

	log.Printf("Creating LLM service (provider=%s, model=%s)", llmProvider, llmModel)
	llmSvc := llm.NewService(llmProvider, llmAPIKey, llmModel)

	ctx := context.Background()

	q := `SELECT id, node_id, properties FROM graph_nodes WHERE node_type = 'person' AND (properties->>'current_position' IS NULL OR properties->>'current_position' = '') LIMIT $1`
	rows, err := db.GetConnection().QueryContext(ctx, q, limit)
	if err != nil {
		log.Fatalf("query failed: %v", err)
	}
	defer rows.Close()

	type nodeRow struct {
		id         int
		nodeID     string
		properties json.RawMessage
	}

	var candidates []nodeRow
	for rows.Next() {
		var r nodeRow
		if err := rows.Scan(&r.id, &r.nodeID, &r.properties); err != nil {
			log.Printf("row scan error: %v", err)
			continue
		}
		candidates = append(candidates, r)
	}

	log.Printf("Found %d person nodes with empty current_position (limit %d)", len(candidates), limit)

	for _, nr := range candidates {
		// parse properties to find references to CV or candidate id
		var props map[string]interface{}
		if err := json.Unmarshal(nr.properties, &props); err != nil {
			log.Printf("failed to unmarshal properties for node %s: %v", nr.nodeID, err)
			continue
		}

		// Try multiple keys for cv/candidate linkage
		var parsedText string

		// helper to fetch parsed_text by cv_files.id
		fetchByCVID := func(cvID int) (string, error) {
			var txt sql.NullString
			q := `SELECT parsed_text FROM cv_files WHERE id = $1 ORDER BY uploaded_at DESC LIMIT 1`
			err := db.GetConnection().QueryRowContext(ctx, q, cvID).Scan(&txt)
			if err != nil {
				return "", err
			}
			if txt.Valid {
				return txt.String, nil
			}
			return "", nil
		}

		// helper to fetch parsed_text by candidate_id
		fetchByCandidateID := func(candidateID int) (string, error) {
			var txt sql.NullString
			q := `SELECT parsed_text FROM cv_files WHERE candidate_id = $1 ORDER BY uploaded_at DESC LIMIT 1`
			err := db.GetConnection().QueryRowContext(ctx, q, candidateID).Scan(&txt)
			if err != nil {
				return "", err
			}
			if txt.Valid {
				return txt.String, nil
			}
			return "", nil
		}

		// Check known property keys
		if v, ok := props["cv_id"]; ok && v != nil {
			switch t := v.(type) {
			case float64:
				if txt, err := fetchByCVID(int(t)); err == nil && txt != "" {
					parsedText = txt
				}
			case string:
				if n, err := strconv.Atoi(t); err == nil {
					if txt, err := fetchByCVID(n); err == nil && txt != "" {
						parsedText = txt
					}
				}
			}
		}

		if parsedText == "" {
			if v, ok := props["candidate_id"]; ok && v != nil {
				switch t := v.(type) {
				case float64:
					if txt, err := fetchByCandidateID(int(t)); err == nil && txt != "" {
						parsedText = txt
					}
				case string:
					if n, err := strconv.Atoi(t); err == nil {
						if txt, err := fetchByCandidateID(n); err == nil && txt != "" {
							parsedText = txt
						}
					}
				}
			}
		}

		if parsedText == "" {
			// Try alternative property names
			if v, ok := props["cv_file_id"]; ok && v != nil {
				switch t := v.(type) {
				case float64:
					if txt, err := fetchByCVID(int(t)); err == nil && txt != "" {
						parsedText = txt
					}
				case string:
					if n, err := strconv.Atoi(t); err == nil {
						if txt, err := fetchByCVID(n); err == nil && txt != "" {
							parsedText = txt
						}
					}
				}
			}
		}

		if parsedText == "" {
			log.Printf("No CV found for node %s (id=%d) — skipping", nr.nodeID, nr.id)
			continue
		}

		// Call LLM extraction
		extraction, err := llmSvc.ExtractEntities(parsedText)
		if err != nil {
			log.Printf("LLM extraction failed for node %s: %v", nr.nodeID, err)
			continue
		}

		pos := strings.TrimSpace(extraction.Candidate.CurrentPosition)
		if pos == "" {
			log.Printf("LLM did not extract a current_position for node %s", nr.nodeID)
			continue
		}

		log.Printf("Node %s -> predicted current_position: %s", nr.nodeID, pos)

		if dryRun {
			log.Printf("[dry-run] Would update node %s: set current_position='%s'", nr.nodeID, pos)
			continue
		}

		// Persist into graph_nodes.properties JSONB
		upd := `UPDATE graph_nodes SET properties = jsonb_set(properties, '{current_position}', to_jsonb($1::text), true) WHERE node_id = $2`
		if _, err := db.GetConnection().ExecContext(ctx, upd, pos, nr.nodeID); err != nil {
			log.Printf("failed to update node %s: %v", nr.nodeID, err)
			continue
		}

		// optional small sleep to avoid rate limits
		time.Sleep(300 * time.Millisecond)
	}

	log.Printf("Backfill run complete")
}

// (no helpers needed)
