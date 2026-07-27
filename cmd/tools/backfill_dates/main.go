package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

type CompanyRel struct {
	EdgeID      int
	CompanyName string
	Position    string
	Properties  string // JSON properties
}

var (
	// Regex to find 4-digit years
	yearRegex = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
)

func main() {
	// Load .env file if present
	if err := godotenv.Load(); err != nil {
		log.Println("Warning: .env file not found, using environment variables")
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatalf("Error opening db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// Query all candidates with cv text
	rows, err := db.QueryContext(ctx, `
		SELECT c.id, c.name, f.parsed_text, c.graph_node_id
		FROM candidates c
		JOIN cv_files f ON f.candidate_id = c.id
		WHERE c.graph_node_id IS NOT NULL 
		  AND f.parsed_text IS NOT NULL AND length(f.parsed_text) > 50
	`)
	if err != nil {
		log.Fatalf("Error querying candidates: %v", err)
	}
	defer rows.Close()

	fmt.Println("Starting offline date backfilling...")
	totalUpdated := 0
	candidateCount := 0

	for rows.Next() {
		var candID, graphNodeID int
		var name, parsedText string
		if err := rows.Scan(&candID, &name, &parsedText, &graphNodeID); err != nil {
			log.Printf("Scan error: %v", err)
			continue
		}

		candidateCount++

		// Query company edges
		relRows, err := db.QueryContext(ctx, `
			SELECT e.id, t.properties->>'name', e.properties
			FROM graph_edges e
			JOIN graph_nodes t ON e.target_node_id = t.id
			WHERE e.source_node_id = $1 AND t.node_type = 'company';
		`, graphNodeID)
		if err != nil {
			log.Printf("  Error getting company edges for %s: %v", name, err)
			continue
		}

		var rels []CompanyRel
		for relRows.Next() {
			var rel CompanyRel
			if err := relRows.Scan(&rel.EdgeID, &rel.CompanyName, &rel.Properties); err == nil {
				rels = append(rels, rel)
			}
		}
		relRows.Close()

		for _, rel := range rels {
			compName := rel.CompanyName
			if compName == "" {
				continue
			}

			// Parse current edge properties
			var props map[string]interface{}
			_ = json.Unmarshal([]byte(rel.Properties), &props)
			if props == nil {
				props = make(map[string]interface{})
			}

			// If it already has start_year, skip it
			if _, ok := props["start_year"]; ok {
				continue
			}

			// Find company name in CV text
			idx := strings.Index(strings.ToLower(parsedText), strings.ToLower(compName))
			if idx == -1 {
				continue
			}

			// Extract window around the company name
			start := idx - 100
			if start < 0 {
				start = 0
			}
			end := idx + len(compName) + 100
			if end > len(parsedText) {
				end = len(parsedText)
			}
			window := parsedText[start:end]

			// Extract years
			matches := yearRegex.FindAllString(window, -1)
			var years []int
			for _, m := range matches {
				y, _ := strconv.Atoi(m)
				if y >= 1990 && y <= 2026 {
					years = append(years, y)
				}
			}

			isCurrent := strings.Contains(strings.ToLower(window), "present") ||
				strings.Contains(strings.ToLower(window), "günümüz") ||
				strings.Contains(strings.ToLower(window), "active") ||
				strings.Contains(strings.ToLower(window), "devam ediyor") ||
				strings.Contains(strings.ToLower(window), "current") ||
				strings.Contains(strings.ToLower(window), "şimdi")

			if len(years) > 0 {
				minY := years[0]
				maxY := years[0]
				for _, y := range years {
					if y < minY {
						minY = y
					}
					if y > maxY {
						maxY = y
					}
				}

				props["start_year"] = minY
				if isCurrent {
					props["duration_years"] = 2026 - minY
					props["is_current"] = true
				} else if len(years) > 1 {
					props["end_year"] = maxY
					props["duration_years"] = maxY - minY
				} else {
					props["end_year"] = minY
					props["duration_years"] = 0
				}

				// Update edge properties in database
				updatedProps, err := json.Marshal(props)
				if err == nil {
					_, err = db.ExecContext(ctx, "UPDATE graph_edges SET properties = $1 WHERE id = $2", string(updatedProps), rel.EdgeID)
					if err == nil {
						totalUpdated++
					}
				}
			}
		}
	}

	fmt.Printf("\nBackfill complete! Processed %d candidates. Successfully updated %d company experience dates.\n", candidateCount, totalUpdated)
}
