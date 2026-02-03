package graphrag

import "strings"

// CommunityPattern defines a community matching pattern
type CommunityPattern struct {
	Name      string
	KeySkills []string
}

// DefaultCommunities defines Level 1 communities (manually curated)
var DefaultCommunities = map[string]CommunityPattern{
	"backend": {
		Name:      "Backend Developers",
		KeySkills: []string{"Java", "Python", "Go", "Node.js", "PHP", "Ruby", "C#", ".NET", "Spring", "Django", "FastAPI", "Express"},
	},
	"frontend": {
		Name:      "Frontend Developers",
		KeySkills: []string{"React", "Vue", "Angular", "JavaScript", "TypeScript", "HTML", "CSS", "Next.js", "Svelte"},
	},
	"mobile": {
		Name:      "Mobile Developers",
		KeySkills: []string{"Swift", "Kotlin", "Flutter", "React Native", "iOS", "Android", "Xamarin"},
	},
	"devops": {
		Name:      "DevOps Engineers",
		KeySkills: []string{"Docker", "Kubernetes", "AWS", "Azure", "GCP", "Jenkins", "Terraform", "Ansible", "CI/CD"},
	},
	"data": {
		Name:      "Data Engineers",
		KeySkills: []string{"SQL", "PostgreSQL", "MySQL", "MongoDB", "Redis", "Cassandra", "Spark", "Kafka", "Elasticsearch"},
	},
	"ml-ai": {
		Name:      "ML/AI Engineers",
		KeySkills: []string{"TensorFlow", "PyTorch", "Scikit-learn", "Machine Learning", "Deep Learning", "AI", "NLP", "Computer Vision"},
	},
	"qa-test": {
		Name:      "QA/Test Engineers",
		KeySkills: []string{"QA", "Testing", "Test Automation", "Selenium", "JUnit", "Jest", "Cypress", "Postman", "Quality Assurance", "Manual Testing", "Test Cases"},
	},
	"analyst": {
		Name:      "Business/Data Analysts",
		KeySkills: []string{"Business Analysis", "Data Analysis", "Analytics", "Tableau", "Power BI", "Excel", "Requirements Analysis", "Requirement Analysis", "BA", "Product Analysis", "Stakeholder", "Agile", "Jira"},
	},
}

// CommunityMembership represents a candidate's membership in a community
type CommunityMembership struct {
	CommunityID string
	Score       float64 // Normalized 0-1
}

// FindCommunity determines primary community (for backward compatibility)
// Returns community ID or "general" if no match
func FindCommunity(skills []SkillNode) string {
	primary, _, _ := FindCommunities(skills, 0.3) // 30% threshold
	return primary
}

// FindCommunities determines all communities a candidate belongs to (Microsoft GraphRAG style)
// Returns: primary community, all communities above threshold, community scores
func FindCommunities(skills []SkillNode, threshold float64) (string, []string, map[string]float64) {
	// Track match scores for each community
	rawScores := make(map[string]int)

	for _, skill := range skills {
		skillLower := strings.ToLower(skill.Name)

		for communityID, pattern := range DefaultCommunities {
			for _, keySkill := range pattern.KeySkills {
				if strings.Contains(skillLower, strings.ToLower(keySkill)) ||
					strings.Contains(strings.ToLower(keySkill), skillLower) {
					rawScores[communityID]++
					break // Count each skill only once per community
				}
			}
		}
	}

	// Find max score for normalization
	maxScore := 0
	for _, score := range rawScores {
		if score > maxScore {
			maxScore = score
		}
	}

	if maxScore == 0 {
		return "general", []string{"general"}, map[string]float64{"general": 1.0}
	}

	// Normalize scores to 0-1 range
	normalizedScores := make(map[string]float64)
	var primary string
	var communities []string

	for communityID, score := range rawScores {
		normalizedScore := float64(score) / float64(maxScore)
		normalizedScores[communityID] = normalizedScore

		// Primary is the one with highest score
		if score == maxScore {
			primary = communityID
		}

		// Include in communities list if above threshold
		if normalizedScore >= threshold {
			communities = append(communities, communityID)
		}
	}

	if primary == "" {
		return "general", []string{"general"}, map[string]float64{"general": 1.0}
	}

	return primary, communities, normalizedScores
}

// FindCommunitiesByQuery infers relevant communities from search query
func FindCommunitiesByQuery(query string) []string {
	queryLower := strings.ToLower(query)
	matches := make(map[string]bool)

	for communityID, pattern := range DefaultCommunities {
		for _, keySkill := range pattern.KeySkills {
			if strings.Contains(queryLower, strings.ToLower(keySkill)) {
				matches[communityID] = true
				break
			}
		}
	}

	// Convert to slice
	result := make([]string, 0, len(matches))
	for communityID := range matches {
		result = append(result, communityID)
	}

	// If no specific community matched, return all (broad search)
	if len(result) == 0 {
		return []string{"backend", "frontend", "mobile", "devops", "data", "ml-ai", "qa-test", "analyst", "general"}
	}

	return result
}
