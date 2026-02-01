package graphrag

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
)

// CommunityDetector implements Leiden algorithm for community detection
type CommunityDetector struct {
	db  *sql.DB
	llm LLMClient
}

func NewCommunityDetector(db *sql.DB, llm LLMClient) *CommunityDetector {
	return &CommunityDetector{
		db:  db,
		llm: llm,
	}
}

// Graph represents the network structure
type Graph struct {
	Nodes map[int]*Node
	Edges []*Edge
}

type Node struct {
	ID         int
	NodeID     string
	NodeType   string
	Properties map[string]interface{}
	Community  int
	Neighbors  []int
}

type Edge struct {
	Source int
	Target int
	Weight float64
}

// DetectCommunities runs Leiden algorithm and stores results
func (cd *CommunityDetector) DetectCommunities(ctx context.Context, level int) error {
	log.Printf("[Community Detection] Starting Leiden algorithm at level %d", level)

	// 1. Load graph from database
	graph, err := cd.loadGraph(ctx)
	if err != nil {
		return fmt.Errorf("failed to load graph: %w", err)
	}

	log.Printf("[Community Detection] Loaded graph: %d nodes, %d edges",
		len(graph.Nodes), len(graph.Edges))

	// 2. Run Leiden algorithm
	communities := cd.leiden(graph)

	log.Printf("[Community Detection] Found %d communities", len(communities))

	// 3. Store communities in database
	if err := cd.storeCommunities(ctx, level, communities, graph); err != nil {
		return fmt.Errorf("failed to store communities: %w", err)
	}

	// 4. Generate summaries for each community
	if err := cd.generateCommunitySummaries(ctx, level); err != nil {
		return fmt.Errorf("failed to generate summaries: %w", err)
	}

	log.Printf("[Community Detection] Completed successfully")
	return nil
}

// loadGraph loads the graph structure from database
func (cd *CommunityDetector) loadGraph(ctx context.Context) (*Graph, error) {
	graph := &Graph{
		Nodes: make(map[int]*Node),
		Edges: []*Edge{},
	}

	// Load nodes
	rows, err := cd.db.QueryContext(ctx, `
		SELECT id, node_id, node_type, properties 
		FROM graph_nodes
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var id int
		var nodeID, nodeType string
		var propsJSON []byte

		if err := rows.Scan(&id, &nodeID, &nodeType, &propsJSON); err != nil {
			continue
		}

		var props map[string]interface{}
		json.Unmarshal(propsJSON, &props)

		graph.Nodes[id] = &Node{
			ID:         id,
			NodeID:     nodeID,
			NodeType:   nodeType,
			Properties: props,
			Community:  id, // Initially each node is its own community
			Neighbors:  []int{},
		}
	}

	// Load edges
	rows, err = cd.db.QueryContext(ctx, `
		SELECT source_node_id, target_node_id 
		FROM graph_edges
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var source, target int
		if err := rows.Scan(&source, &target); err != nil {
			continue
		}

		graph.Edges = append(graph.Edges, &Edge{
			Source: source,
			Target: target,
			Weight: 1.0,
		})

		// Add to neighbors
		if node, ok := graph.Nodes[source]; ok {
			node.Neighbors = append(node.Neighbors, target)
		}
		if node, ok := graph.Nodes[target]; ok {
			node.Neighbors = append(node.Neighbors, source)
		}
	}

	return graph, nil
}

// leiden implements the Leiden algorithm (simplified version)
func (cd *CommunityDetector) leiden(graph *Graph) map[int][]int {
	// Simplified Leiden: Start with Louvain-style modularity optimization

	maxIterations := 100
	improved := true
	iteration := 0

	for improved && iteration < maxIterations {
		improved = false
		iteration++

		// Shuffle nodes for randomness
		nodeIDs := make([]int, 0, len(graph.Nodes))
		for id := range graph.Nodes {
			nodeIDs = append(nodeIDs, id)
		}
		rand.Shuffle(len(nodeIDs), func(i, j int) {
			nodeIDs[i], nodeIDs[j] = nodeIDs[j], nodeIDs[i]
		})

		// Try to move each node to neighbor's community
		for _, nodeID := range nodeIDs {
			node := graph.Nodes[nodeID]
			currentCommunity := node.Community
			bestCommunity := currentCommunity
			bestGain := 0.0

			// Try each neighbor's community
			neighborCommunities := make(map[int]bool)
			for _, neighborID := range node.Neighbors {
				if neighbor, ok := graph.Nodes[neighborID]; ok {
					neighborCommunities[neighbor.Community] = true
				}
			}

			for communityID := range neighborCommunities {
				if communityID == currentCommunity {
					continue
				}

				gain := cd.modularityGain(graph, node, communityID)
				if gain > bestGain {
					bestGain = gain
					bestCommunity = communityID
				}
			}

			// Move to best community if improvement
			if bestCommunity != currentCommunity {
				node.Community = bestCommunity
				improved = true
			}
		}

		if !improved {
			break
		}
	}

	// Group nodes by community
	communities := make(map[int][]int)
	for id, node := range graph.Nodes {
		communities[node.Community] = append(communities[node.Community], id)
	}

	return communities
}

// modularityGain calculates the gain in modularity by moving node to community
func (cd *CommunityDetector) modularityGain(graph *Graph, node *Node, targetCommunity int) float64 {
	// Simplified: count connections to target community
	connectionsToTarget := 0.0

	for _, neighborID := range node.Neighbors {
		if neighbor, ok := graph.Nodes[neighborID]; ok {
			if neighbor.Community == targetCommunity {
				connectionsToTarget++
			}
		}
	}

	// Normalize by total neighbors
	if len(node.Neighbors) > 0 {
		return connectionsToTarget / float64(len(node.Neighbors))
	}

	return 0.0
}

// storeCommunities saves detected communities to database
func (cd *CommunityDetector) storeCommunities(ctx context.Context, level int, communities map[int][]int, graph *Graph) error {
	// Clear existing communities at this level
	_, err := cd.db.ExecContext(ctx, `
		DELETE FROM graph_communities WHERE level = $1
	`, level)
	if err != nil {
		return err
	}

	// Insert new communities
	for communityID, memberIDs := range communities {
		// Skip tiny communities
		if len(memberIDs) < 2 {
			continue
		}

		// Create community title from members
		title := cd.generateCommunityTitle(memberIDs, graph)

		// Insert community
		var dbCommunityID int
		err := cd.db.QueryRowContext(ctx, `
			INSERT INTO graph_communities (level, community_id, title, node_count)
			VALUES ($1, $2, $3, $4)
			RETURNING id
		`, level, fmt.Sprintf("community_%d_%d", level, communityID), title, len(memberIDs)).Scan(&dbCommunityID)

		if err != nil {
			log.Printf("[Community Detection] Failed to insert community: %v", err)
			continue
		}

		// Insert members
		for _, nodeID := range memberIDs {
			_, err := cd.db.ExecContext(ctx, `
				INSERT INTO community_members (community_id, node_id, membership_strength)
				VALUES ($1, $2, $3)
			`, dbCommunityID, nodeID, 1.0)

			if err != nil {
				log.Printf("[Community Detection] Failed to insert member: %v", err)
			}
		}
	}

	return nil
}

// generateCommunityTitle creates a descriptive title from member nodes
func (cd *CommunityDetector) generateCommunityTitle(memberIDs []int, graph *Graph) string {
	// Count node types
	typeCounts := make(map[string]int)

	for _, id := range memberIDs {
		if node, ok := graph.Nodes[id]; ok {
			typeCounts[node.NodeType]++
		}
	}

	// Find dominant type
	maxCount := 0
	dominantType := "Mixed"
	for nodeType, count := range typeCounts {
		if count > maxCount {
			maxCount = count
			dominantType = nodeType
		}
	}

	return fmt.Sprintf("%s Community (%d members)", dominantType, len(memberIDs))
}

// generateCommunitySummaries uses LLM to create summaries for each community
func (cd *CommunityDetector) generateCommunitySummaries(ctx context.Context, level int) error {
	rows, err := cd.db.QueryContext(ctx, `
		SELECT c.id, c.community_id, c.title,
		       ARRAY_AGG(gn.node_type) as types,
		       ARRAY_AGG(gn.properties) as properties
		FROM graph_communities c
		JOIN community_members cm ON c.id = cm.community_id
		JOIN graph_nodes gn ON cm.node_id = gn.id
		WHERE c.level = $1
		GROUP BY c.id, c.community_id, c.title
	`, level)

	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var id int
		var communityID, title string
		var types []string
		var properties [][]byte

		if err := rows.Scan(&id, &communityID, &title, &types, &properties); err != nil {
			continue
		}

		// Generate summary with LLM
		summary := cd.generateSummaryWithLLM(title, types, properties)

		// Update community with summary
		_, err := cd.db.ExecContext(ctx, `
			UPDATE graph_communities 
			SET summary = $1, updated_at = NOW()
			WHERE id = $2
		`, summary, id)

		if err != nil {
			log.Printf("[Community Detection] Failed to update summary: %v", err)
		}
	}

	return nil
}

// generateSummaryWithLLM creates a natural language summary using LLM
func (cd *CommunityDetector) generateSummaryWithLLM(title string, types []string, properties [][]byte) string {
	// Build entity list
	entities := make([]string, 0, len(properties))
	for i, propBytes := range properties {
		var props map[string]interface{}
		if err := json.Unmarshal(propBytes, &props); err != nil {
			continue
		}

		if name, ok := props["name"].(string); ok && i < 10 { // Limit to first 10
			entities = append(entities, fmt.Sprintf("%s (%s)", name, types[i]))
		}
	}

	if len(entities) == 0 {
		return fmt.Sprintf("A community of %d related entities", len(properties))
	}

	prompt := fmt.Sprintf(`You are analyzing a community of related entities in a knowledge graph.

COMMUNITY: %s
SIZE: %d entities

SAMPLE ENTITIES:
%s

Provide a 2-3 sentence summary describing:
1. What this community represents
2. Common themes or patterns
3. Key relationships

Summary:`, title, len(properties), entities[:int(math.Min(float64(len(entities)), 10))])

	summary, err := cd.llm.Generate(prompt)
	if err != nil {
		return fmt.Sprintf("Community of %d %s entities", len(properties), title)
	}

	return summary
}
