package graphrag

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
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
	Name        string `json:"name"`
	Proficiency string `json:"proficiency"`
}

type CompanyNode struct {
	Name      string `json:"name"`
	Position  string `json:"position"`
	IsCurrent bool   `json:"is_current"`
}

type EducationNode struct {
	Institution string `json:"institution"`
	Degree      string `json:"degree"`
	Field       string `json:"field"`
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
	log.Printf("[GraphRAG] Querying graph with criteria: %+v", criteria)

	// Build SQL query dynamically based on criteria
	query, args := q.buildQuery(criteria)

	log.Printf("[GraphRAG] Executing SQL: %s", query)
	log.Printf("[GraphRAG] With args: %v", args)

	rows, err := q.db.QueryContext(ctx, query, args...)
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
			log.Printf("[GraphRAG] Scan error: %v", err)
			continue
		}

		// Parse properties
		var props map[string]interface{}
		if err := json.Unmarshal(propsJSON, &props); err != nil {
			log.Printf("[GraphRAG] JSON unmarshal error: %v", err)
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

		// Fetch related nodes (skills, companies, education)
		q.enrichCandidate(ctx, &result)

		// Calculate match score
		result.MatchScore, result.MatchReasons = q.calculateMatch(&result, criteria)

		results = append(results, result)
	}

	// Sort by match score (highest first)
	for i := 0; i < len(results)-1; i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].MatchScore > results[i].MatchScore {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	log.Printf("[GraphRAG] Found %d candidates", len(results))
	return results, nil
}

func (q *GraphQuerier) buildQuery(criteria *SearchCriteria) (string, []interface{}) {
	baseQuery := `
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
			`, argIndex, argIndex+1))
			args = append(args, fmt.Sprintf("%%company_%s%%", company), fmt.Sprintf("%%%s%%", company))
			argIndex += 2
		}
		conditions = append(conditions, "("+strings.Join(companyConditions, " OR ")+")")
	}

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
			"(p.properties->>'total_experience_years')::int >= $%d", argIndex))
		args = append(args, *criteria.MinExperience)
		argIndex++
	}

	// Filter by maximum experience years
	if criteria.MaxExperience != nil && *criteria.MaxExperience > 0 {
		conditions = append(conditions, fmt.Sprintf(
			"(p.properties->>'total_experience_years')::int <= $%d", argIndex))
		args = append(args, *criteria.MaxExperience)
		argIndex++
	}

	if len(conditions) > 0 {
		baseQuery += " AND " + strings.Join(conditions, " AND ")
	}

	baseQuery += " LIMIT 50" // Safety limit

	return baseQuery, args
}

func (q *GraphQuerier) enrichCandidate(ctx context.Context, result *CandidateResult) {
	// Fetch skills
	skillRows, err := q.db.QueryContext(ctx, `
		SELECT s.node_id, s.properties
		FROM graph_nodes p
		JOIN graph_edges e ON p.id = e.source_node_id
		JOIN graph_nodes s ON e.target_node_id = s.id
		WHERE p.node_id = $1
		  AND e.edge_type = 'HAS_SKILL'
		  AND s.node_type = 'skill'
	`, result.PersonID)

	if err == nil {
		defer skillRows.Close()
		for skillRows.Next() {
			var nodeID string
			var propsJSON []byte
			if err := skillRows.Scan(&nodeID, &propsJSON); err == nil {
				var props map[string]interface{}
				if err := json.Unmarshal(propsJSON, &props); err == nil {
					skill := SkillNode{
						Name:        props["name"].(string),
						Proficiency: props["proficiency"].(string),
					}
					result.Skills = append(result.Skills, skill)
				}
			}
		}
	}

	// Fetch companies
	companyRows, err := q.db.QueryContext(ctx, `
		SELECT c.node_id, c.properties, e.edge_type
		FROM graph_nodes p
		JOIN graph_edges e ON p.id = e.source_node_id
		JOIN graph_nodes c ON e.target_node_id = c.id
		WHERE p.node_id = $1
		  AND e.edge_type IN ('WORKS_AT', 'WORKED_AT')
		  AND c.node_type = 'company'
	`, result.PersonID)

	if err == nil {
		defer companyRows.Close()
		for companyRows.Next() {
			var nodeID, edgeType string
			var propsJSON []byte
			if err := companyRows.Scan(&nodeID, &propsJSON, &edgeType); err == nil {
				var props map[string]interface{}
				if err := json.Unmarshal(propsJSON, &props); err == nil {
					company := CompanyNode{
						Name:      props["name"].(string),
						IsCurrent: edgeType == "WORKS_AT",
					}
					if pos, ok := props["position"].(string); ok {
						company.Position = pos
					}
					result.Companies = append(result.Companies, company)
				}
			}
		}
	}

	// Fetch education
	eduRows, err := q.db.QueryContext(ctx, `
		SELECT ed.node_id, ed.properties
		FROM graph_nodes p
		JOIN graph_edges e ON p.id = e.source_node_id
		JOIN graph_nodes ed ON e.target_node_id = ed.id
		WHERE p.node_id = $1
		  AND e.edge_type = 'GRADUATED_FROM'
		  AND ed.node_type = 'education'
	`, result.PersonID)

	if err == nil {
		defer eduRows.Close()
		for eduRows.Next() {
			var nodeID string
			var propsJSON []byte
			if err := eduRows.Scan(&nodeID, &propsJSON); err == nil {
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
					result.Education = append(result.Education, edu)
				}
			}
		}
	}
}

// calculateMatch is DEPRECATED - Use LLM-based scoring instead
// Keeping for backward compatibility, but always returns 0
// This function previously used hard-coded heuristics which are now replaced by pure LLM scoring
func (q *GraphQuerier) calculateMatch(result *CandidateResult, criteria *SearchCriteria) (float64, []string) {
	// NO MORE LOCAL SCORING!
	// All scoring is done by LLM in hybrid_search.go -> LLMScorer
	return 0.0, []string{"Scored by LLM"}
}
