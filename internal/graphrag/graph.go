package graphrag

import (
	"context"
	"cv-search/internal/llm"
	"database/sql"
	"encoding/json"
	"fmt"
)

type GraphBuilder struct {
	db *sql.DB
}

func NewGraphBuilder(db *sql.DB) *GraphBuilder {
	return &GraphBuilder{db: db}
}

// CreateNodes inserts or updates graph nodes
func (g *GraphBuilder) CreateNodes(ctx context.Context, entities []Entity) error {
	for _, entity := range entities {
		props, err := json.Marshal(entity.Properties)
		if err != nil {
			return fmt.Errorf("failed to marshal properties: %w", err)
		}

		_, err = g.db.ExecContext(ctx, `
			INSERT INTO graph_nodes (node_type, node_id, properties)
			VALUES ($1, $2, $3)
			ON CONFLICT (node_type, node_id) 
			DO UPDATE SET properties = EXCLUDED.properties
		`, entity.Type, entity.Value, props)

		if err != nil {
			return fmt.Errorf("failed to create node %s:%s: %w", entity.Type, entity.Value, err)
		}
	}

	return nil
}

// CreateEdges inserts relationships between nodes
func (g *GraphBuilder) CreateEdges(ctx context.Context, relationships []Relationship) error {
	for _, rel := range relationships {
		// Get source node ID
		var sourceID int
		err := g.db.QueryRowContext(ctx, `
			SELECT id FROM graph_nodes WHERE node_type = $1 AND node_id = $2
		`, rel.SourceType, rel.SourceID).Scan(&sourceID)

		if err != nil {
			// Skip if source node doesn't exist
			continue
		}

		// Get target node ID
		var targetID int
		err = g.db.QueryRowContext(ctx, `
			SELECT id FROM graph_nodes WHERE node_type = $1 AND node_id = $2
		`, rel.TargetType, rel.TargetID).Scan(&targetID)

		if err != nil {
			// Skip if target node doesn't exist
			continue
		}

		// Insert edge
		props, _ := json.Marshal(rel.Properties)
		_, err = g.db.ExecContext(ctx, `
			INSERT INTO graph_edges (source_node_id, target_node_id, edge_type, properties)
			VALUES ($1, $2, $3, $4)
		`, sourceID, targetID, rel.EdgeType, props)

		if err != nil {
			return fmt.Errorf("failed to create edge: %w", err)
		}
	}

	return nil
}

// QueryGraph performs graph traversal queries
func (g *GraphBuilder) QueryGraph(ctx context.Context, nodeType, nodeID string, depth int) ([]Entity, []Relationship, error) {
	// Start with the initial node
	var startNodeDBID int
	err := g.db.QueryRowContext(ctx, `
		SELECT id FROM graph_nodes WHERE node_type = $1 AND node_id = $2
	`, nodeType, nodeID).Scan(&startNodeDBID)

	if err != nil {
		return nil, nil, fmt.Errorf("start node not found: %w", err)
	}

	// Traverse graph up to specified depth
	// This is a simplified version - for production, use recursive CTEs
	entities := []Entity{}
	relationships := []Relationship{}

	rows, err := g.db.QueryContext(ctx, `
		SELECT 
			sn.node_type as source_type, sn.node_id as source_id, sn.properties as source_props,
			tn.node_type as target_type, tn.node_id as target_id, tn.properties as target_props,
			e.edge_type, e.properties as edge_props
		FROM graph_edges e
		INNER JOIN graph_nodes sn ON e.source_node_id = sn.id
		INNER JOIN graph_nodes tn ON e.target_node_id = tn.id
		WHERE e.source_node_id = $1 OR e.target_node_id = $1
	`, startNodeDBID)

	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	seen := make(map[string]bool)

	for rows.Next() {
		var (
			sourceType, sourceID, sourceProps string
			targetType, targetID, targetProps string
			edgeType, edgeProps               string
		)

		if err := rows.Scan(&sourceType, &sourceID, &sourceProps, &targetType, &targetID, &targetProps, &edgeType, &edgeProps); err != nil {
			continue
		}

		// Add source entity if not seen
		if !seen[sourceType+":"+sourceID] {
			var props map[string]interface{}
			json.Unmarshal([]byte(sourceProps), &props)
			entities = append(entities, Entity{
				Type:       sourceType,
				Value:      sourceID,
				Properties: props,
			})
			seen[sourceType+":"+sourceID] = true
		}

		// Add target entity if not seen
		if !seen[targetType+":"+targetID] {
			var props map[string]interface{}
			json.Unmarshal([]byte(targetProps), &props)
			entities = append(entities, Entity{
				Type:       targetType,
				Value:      targetID,
				Properties: props,
			})
			seen[targetType+":"+targetID] = true
		}

		// Add relationship
		var relProps map[string]interface{}
		json.Unmarshal([]byte(edgeProps), &relProps)
		relationships = append(relationships, Relationship{
			SourceType: sourceType,
			SourceID:   sourceID,
			TargetType: targetType,
			TargetID:   targetID,
			EdgeType:   edgeType,
			Properties: relProps,
		})
	}

	return entities, relationships, nil
}

// BuildFromLLMExtraction creates graph from LLM-extracted CV data
func (g *GraphBuilder) BuildFromLLMExtraction(ctx context.Context, cvID int, extraction interface{}) error {
	// Type assert to map structure from handler
	ext, ok := extraction.(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid extraction format")
	}

	var entities []Entity
	var relationships []Relationship

	// 1. Create Person node from candidate
	if candidate, ok := ext["candidate"].(map[string]interface{}); ok {
		personID := fmt.Sprintf("person_%d", cvID)
		entities = append(entities, Entity{
			Type:  "person",
			Value: personID,
			Properties: map[string]interface{}{
				"cv_id":                  cvID,
				"name":                   candidate["name"],
				"current_position":       candidate["current_position"],
				"seniority":              candidate["seniority"],
				"total_experience_years": candidate["total_experience_years"],
			},
		})

		// 2. Create Skill nodes and HAS_SKILL relationships
		if skillsInterface, ok := ext["skills"]; ok {
			// Skills are passed as []llm.Skill, convert to []interface{}
			skills, ok := skillsInterface.([]llm.Skill)
			if ok {
				for _, skill := range skills {
					skillID := fmt.Sprintf("skill_%s", skill.Name)

					// Create skill node
					entities = append(entities, Entity{
						Type:  "skill",
						Value: skillID,
						Properties: map[string]interface{}{
							"name":        skill.Name,
							"proficiency": skill.Proficiency,
						},
					})

					// Create HAS_SKILL relationship
					relProps := map[string]interface{}{
						"proficiency": skill.Proficiency,
					}
					if skill.Years != nil {
						relProps["years_of_experience"] = *skill.Years
					}
					relationships = append(relationships, Relationship{
						SourceType: "person",
						SourceID:   personID,
						TargetType: "skill",
						TargetID:   skillID,
						EdgeType:   "HAS_SKILL",
						Properties: relProps,
					})
				}
			}
		}

		// 3. Create Company nodes and WORKS_AT/WORKED_AT relationships
		if companiesInterface, ok := ext["companies"]; ok {
			companies, ok := companiesInterface.([]llm.Company)
			if ok {
				for i, company := range companies {
					companyID := fmt.Sprintf("company_%s", company.Name)

					// Create company node
					entities = append(entities, Entity{
						Type:  "company",
						Value: companyID,
						Properties: map[string]interface{}{
							"name": company.Name,
						},
					})

					// Determine edge type based on is_current
					edgeType := "WORKED_AT"
					if company.IsCurrent {
						edgeType = "WORKS_AT"
					} else if i == 0 {
						// Fallback: first company is usually current
						edgeType = "WORKS_AT"
					}

					relationships = append(relationships, Relationship{
						SourceType: "person",
						SourceID:   personID,
						TargetType: "company",
						TargetID:   companyID,
						EdgeType:   edgeType,
						Properties: map[string]interface{}{
							"position": company.Position,
						},
					})
				}
			}
		}

		// 4. Create Education nodes and GRADUATED_FROM relationships
		if educationInterface, ok := ext["education"]; ok {
			education, ok := educationInterface.([]llm.Education)
			if ok {
				for _, edu := range education {
					eduID := fmt.Sprintf("education_%s", edu.Institution)

					// Create education node
					entities = append(entities, Entity{
						Type:  "education",
						Value: eduID,
						Properties: map[string]interface{}{
							"institution":     edu.Institution,
							"degree":          edu.Degree,
							"field":           edu.Field,
							"graduation_year": edu.GraduationYear,
						},
					})

					relationships = append(relationships, Relationship{
						SourceType: "person",
						SourceID:   personID,
						TargetType: "education",
						TargetID:   eduID,
						EdgeType:   "GRADUATED_FROM",
						Properties: map[string]interface{}{
							"degree": edu.Degree,
							"field":  edu.Field,
						},
					})
				}
			}
		}
	}

	// Create all nodes
	if err := g.CreateNodes(ctx, entities); err != nil {
		return fmt.Errorf("failed to create nodes: %w", err)
	}

	// Create all edges
	if err := g.CreateEdges(ctx, relationships); err != nil {
		return fmt.Errorf("failed to create edges: %w", err)
	}

	return nil
}
