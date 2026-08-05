package graphrag

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// CandidateResult represents a candidate found in graph search
type CandidateResult struct {
	CVID            int             `json:"cv_id"`
	PersonID        string          `json:"person_id"`
	Name            string          `json:"name"`
	CurrentPosition string          `json:"current_position"`
	Seniority       string          `json:"seniority"`
	TotalExperience interface{}     `json:"total_experience_years"`
	Skills          []SkillNode     `json:"skills"`
	Companies       []CompanyNode   `json:"companies"`
	Education       []EducationNode `json:"education"`
	MatchScore      float64         `json:"match_score"`
	MatchReasons    []string        `json:"match_reasons"`
}

type SkillNode struct {
	Name              string `json:"name"`
	Proficiency       string `json:"proficiency"`
	YearsOfExperience int    `json:"years_of_experience,omitempty"`
}

type CompanyNode struct {
	Name          string      `json:"name"`
	Position      string      `json:"position"`
	IsCurrent     bool        `json:"is_current"`
	StartYear     interface{} `json:"start_year,omitempty"`
	EndYear       interface{} `json:"end_year,omitempty"`
	DurationYears interface{} `json:"duration_years,omitempty"`
}

type EducationNode struct {
	Institution string `json:"institution"`
	Degree      string `json:"degree"`
	Field       string `json:"field"`
}

// SearchCriteria holds structured search parameters extracted from a natural language query.
type SearchCriteria struct {
	Skills        []string `json:"skills"`         // Required + preferred skills combined
	Companies     []string `json:"companies"`      // Company names
	Positions     []string `json:"positions"`      // Job titles
	Seniority     string   `json:"seniority"`      // Junior|Mid-level|Senior|Lead|Architect
	Education     []string `json:"education"`      // Institution or degree
	MinExperience *int     `json:"min_experience"` // Minimum years
	MaxExperience *int     `json:"max_experience"` // Maximum years
	Location      []string `json:"location"`       // Cities/countries
	ExpandedQuery string   `json:"expanded_query"` // TR/EN translations and synonyms for better BM25/Vector matching

	// Legacy fields kept for backward compatibility
	RequiredSkills  []string               `json:"required_skills,omitempty"`
	PreferredSkills []string               `json:"preferred_skills,omitempty"`
	Weights         ScoringWeights         `json:"weights,omitempty"`
	CustomFilters   map[string]interface{} `json:"custom_filters,omitempty"`
}

// ScoringWeights configures relative importance of match dimensions (unused in LLM-scored path).
type ScoringWeights struct {
	SkillWeight      float64 `json:"skill_weight"`
	ExperienceWeight float64 `json:"experience_weight"`
	LocationWeight   float64 `json:"location_weight"`
	EducationWeight  float64 `json:"education_weight"`
}

// GraphQuerier performs graph traversal based on search criteria
type GraphQuerier struct {
	db *sql.DB
}

func NewGraphQuerier(db *sql.DB) *GraphQuerier {
	return &GraphQuerier{db: db}
}

// QueryGraph searches the graph based on criteria
func (q *GraphQuerier) QueryGraph(ctx context.Context, criteria *SearchCriteria) ([]CandidateResult, error) {
	slog.Info(fmt.Sprintf("[GraphRAG] Querying graph with criteria: %+v", criteria))

	// Build SQL query dynamically based on criteria
	query, args := q.buildQuery(criteria)

	slog.Info(fmt.Sprintf("[GraphRAG] Executing SQL: %s", query))
	slog.Info(fmt.Sprintf("[GraphRAG] With args: %v", args))

	// Use db.Query (non-context) to avoid prepared statement reuse
	rows, err := q.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("graph query failed: %w", err)
	}
	defer rows.Close()

	var results []CandidateResult
	for rows.Next() {
		var result CandidateResult
		var propsJSON []byte

		err := rows.Scan(&result.PersonID, &propsJSON)
		if err != nil {
			slog.Error(fmt.Sprintf("[GraphRAG] Scan error: %v", err))
			continue
		}

		// Parse properties
		var props map[string]interface{}
		if err := json.Unmarshal(propsJSON, &props); err != nil {
			slog.Error(fmt.Sprintf("[GraphRAG] JSON unmarshal error: %v", err))
			continue
		}

		// Extract basic info
		if cvID, ok := props["cv_id"].(float64); ok {
			result.CVID = int(cvID)
		}
		if name, ok := props["name"].(string); ok {
			result.Name = name
		}
		if pos, ok := props["current_position"].(string); ok {
			result.CurrentPosition = pos
		}
		if sen, ok := props["seniority"].(string); ok {
			result.Seniority = sen
		}
		result.TotalExperience = props["total_experience_years"]

		// MatchScore left at 0.0 — all scoring is handled by LLM in hybrid_search.go
		results = append(results, result)
	}

	// Fetch related nodes (skills, companies, education) in batch
	q.enrichCandidatesBatch(ctx, results)

	// Sort by match score descending (all zeros currently, but preserves order stability)
	sort.Slice(results, func(i, j int) bool {
		return results[i].MatchScore > results[j].MatchScore
	})

	slog.Info(fmt.Sprintf("[GraphRAG] Found %d candidates", len(results)))
	return results, nil
}

func (q *GraphQuerier) buildQuery(criteria *SearchCriteria) (string, []interface{}) {
	// Add unique comment to prevent prepared statement cache collision
	queryID := fmt.Sprintf("/* graphquery_%d */", time.Now().UnixNano())

	baseQuery := queryID + `
		SELECT DISTINCT p.node_id, p.properties
		FROM graph_nodes p
		WHERE p.node_type = 'person'
	`

	var conditions []string
	var args []interface{}
	argIndex := 1

	// Filter by seniority
	if criteria.Seniority != "" {
		conditions = append(conditions, fmt.Sprintf("p.properties->>'seniority' = $%d", argIndex))
		args = append(args, criteria.Seniority)
		argIndex++
	}

	// Filter by skills
	if len(criteria.Skills) > 0 {
		for _, skill := range criteria.Skills {
			skillID := fmt.Sprintf("skill_%s", skill)
			conditions = append(conditions, fmt.Sprintf(`
				EXISTS (
					SELECT 1 FROM graph_edges e
					JOIN graph_nodes s ON e.target_node_id = s.id
					WHERE e.source_node_id = p.id
					  AND e.edge_type = 'HAS_SKILL'
					  AND s.node_id = $%d
				)
			`, argIndex))
			args = append(args, skillID)
			argIndex++
		}
	}

	// Filter by companies (partial match with LIKE for better matching)
	if len(criteria.Companies) > 0 {
		companyConditions := []string{}
		for _, company := range criteria.Companies {
			// Try both exact match and partial match
			companyConditions = append(companyConditions, fmt.Sprintf(`
				EXISTS (
					SELECT 1 FROM graph_edges e
					JOIN graph_nodes c ON e.target_node_id = c.id
					WHERE e.source_node_id = p.id
					  AND e.edge_type IN ('WORKS_AT', 'WORKED_AT')
					  AND (c.node_id LIKE $%d OR c.properties->>'name' ILIKE $%d)
				)
			`, argIndex, argIndex+1)) // Fixed: use argIndex+1 for second placeholder
			args = append(args, fmt.Sprintf("%%company_%s%%", company), fmt.Sprintf("%%%s%%", company))
			argIndex += 2
		}
		conditions = append(conditions, "("+strings.Join(companyConditions, " OR ")+")")
	}

	// NOTE: Positions filter intentionally omitted from SQL.
	// A "Java Developer" query should also return Java Architects, Tech Leads, Consultants.
	// Skill post-filter (hybrid_search.go) and LLM reranking handle relevance filtering.

	// Filter by education
	if len(criteria.Education) > 0 {
		eduConditions := []string{}
		for _, edu := range criteria.Education {
			eduID := fmt.Sprintf("education_%s", edu)
			eduConditions = append(eduConditions, fmt.Sprintf(`
				EXISTS (
					SELECT 1 FROM graph_edges e
					JOIN graph_nodes ed ON e.target_node_id = ed.id
					WHERE e.source_node_id = p.id
					  AND e.edge_type = 'GRADUATED_FROM'
					  AND ed.node_id = $%d
				)
			`, argIndex))
			args = append(args, eduID)
			argIndex++
		}
		conditions = append(conditions, "("+strings.Join(eduConditions, " OR ")+")")
	}

	// Filter by minimum experience years
	if criteria.MinExperience != nil && *criteria.MinExperience > 0 {
		conditions = append(conditions, fmt.Sprintf(
			"(p.properties->>'total_experience_years')::numeric >= $%d", argIndex))
		args = append(args, *criteria.MinExperience)
		argIndex++
	}

	// Filter by maximum experience years
	if criteria.MaxExperience != nil && *criteria.MaxExperience > 0 {
		conditions = append(conditions, fmt.Sprintf(
			"(p.properties->>'total_experience_years')::numeric <= $%d", argIndex))
		args = append(args, *criteria.MaxExperience)
		argIndex++
	}

	if len(conditions) > 0 {
		baseQuery += " AND " + strings.Join(conditions, " AND ")
	}

	baseQuery += " LIMIT 200" // Increased from 50 to capture more candidates for broad queries

	return baseQuery, args
}

func (q *GraphQuerier) enrichCandidatesBatch(ctx context.Context, results []CandidateResult) {
	if len(results) == 0 {
		return
	}

	// Create mapping from PersonID to result index for fast lookup
	personIdxMap := make(map[string]int)
	personIDs := make([]interface{}, len(results))
	placeholders := make([]string, len(results))

	for i, r := range results {
		personIdxMap[r.PersonID] = i
		personIDs[i] = r.PersonID
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	inClause := strings.Join(placeholders, ",")

	// Fetch skills
	skillQuery := fmt.Sprintf(`
		SELECT p.node_id, s.node_id, s.properties
		FROM graph_nodes p
		JOIN graph_edges e ON p.id = e.source_node_id
		JOIN graph_nodes s ON e.target_node_id = s.id
		WHERE p.node_id IN (%s)
		  AND e.edge_type = 'HAS_SKILL'
		  AND s.node_type = 'skill'
	`, inClause)

	skillRows, err := q.db.QueryContext(ctx, skillQuery, personIDs...)
	if err == nil {
		defer skillRows.Close()
		for skillRows.Next() {
			var personNodeID, skillNodeID string
			var propsJSON []byte
			if err := skillRows.Scan(&personNodeID, &skillNodeID, &propsJSON); err == nil {
				if idx, ok := personIdxMap[personNodeID]; ok {
					var props map[string]interface{}
					if err := json.Unmarshal(propsJSON, &props); err == nil {
						skill := SkillNode{
							Name:        props["name"].(string),
							Proficiency: props["proficiency"].(string),
						}
						results[idx].Skills = append(results[idx].Skills, skill)
					}
				}
			}
		}
	}

	// Fetch companies
	companyQuery := fmt.Sprintf(`
		SELECT p.node_id, c.node_id, c.properties, e.properties, e.edge_type
		FROM graph_nodes p
		JOIN graph_edges e ON p.id = e.source_node_id
		JOIN graph_nodes c ON e.target_node_id = c.id
		WHERE p.node_id IN (%s)
		  AND e.edge_type IN ('WORKS_AT', 'WORKED_AT')
		  AND c.node_type = 'company'
	`, inClause)

	companyRows, err := q.db.QueryContext(ctx, companyQuery, personIDs...)
	if err == nil {
		defer companyRows.Close()
		for companyRows.Next() {
			var personNodeID, companyNodeID, edgeType string
			var companyPropsJSON, edgePropsJSON []byte
			if err := companyRows.Scan(&personNodeID, &companyNodeID, &companyPropsJSON, &edgePropsJSON, &edgeType); err == nil {
				if idx, ok := personIdxMap[personNodeID]; ok {
					var companyProps, edgeProps map[string]interface{}
					if err := json.Unmarshal(companyPropsJSON, &companyProps); err == nil {
						company := CompanyNode{
							Name:      companyProps["name"].(string),
							IsCurrent: edgeType == "WORKS_AT",
						}
						if err := json.Unmarshal(edgePropsJSON, &edgeProps); err == nil {
							if pos, ok := edgeProps["position"].(string); ok {
								company.Position = pos
							}
							if start, ok := edgeProps["start_year"]; ok {
								company.StartYear = start
							}
							if end, ok := edgeProps["end_year"]; ok {
								company.EndYear = end
							}
							if duration, ok := edgeProps["duration_years"]; ok {
								company.DurationYears = duration
							}
						}
						results[idx].Companies = append(results[idx].Companies, company)
					}
				}
			}
		}
	}

	// Fetch education
	eduQuery := fmt.Sprintf(`
		SELECT p.node_id, ed.node_id, ed.properties
		FROM graph_nodes p
		JOIN graph_edges e ON p.id = e.source_node_id
		JOIN graph_nodes ed ON e.target_node_id = ed.id
		WHERE p.node_id IN (%s)
		  AND e.edge_type = 'GRADUATED_FROM'
		  AND ed.node_type = 'education'
	`, inClause)

	eduRows, err := q.db.QueryContext(ctx, eduQuery, personIDs...)
	if err == nil {
		defer eduRows.Close()
		for eduRows.Next() {
			var personNodeID, eduNodeID string
			var propsJSON []byte
			if err := eduRows.Scan(&personNodeID, &eduNodeID, &propsJSON); err == nil {
				if idx, ok := personIdxMap[personNodeID]; ok {
					var props map[string]interface{}
					if err := json.Unmarshal(propsJSON, &props); err == nil {
						edu := EducationNode{
							Institution: props["institution"].(string),
						}
						if deg, ok := props["degree"].(string); ok {
							edu.Degree = deg
						}
						if field, ok := props["field"].(string); ok {
							edu.Field = field
						}
						results[idx].Education = append(results[idx].Education, edu)
					}
				}
			}
		}
	}
}
