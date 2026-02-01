package graphrag

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// QueryAnalyzer extracts structured criteria from natural language queries
type QueryAnalyzer struct {
	llmClient LLMClient
}

func NewQueryAnalyzer(llmClient LLMClient) *QueryAnalyzer {
	return &QueryAnalyzer{llmClient: llmClient}
}

// AnalyzeQuery converts natural language to structured search criteria
func (a *QueryAnalyzer) AnalyzeQuery(ctx context.Context, query string) (*SearchCriteria, error) {
	log.Printf("[GraphRAG] Analyzing query: %s", query)

	prompt := fmt.Sprintf(`You are a talent search query analyzer. Extract structured search criteria from the user's natural language query.

User Query: "%s"

Extract and return ONLY valid JSON with this structure:
{
  "skills": ["skill names in canonical form"],
  "companies": ["company names"],
  "positions": ["job titles"],
  "seniority": "Junior|Mid-level|Senior|Lead|Architect",
  "education": ["institution names or degree types"],
  "min_experience": null,
  "max_experience": null,
  "location": ["city or country names"]
}

Rules:
- Normalize skill names (e.g., "JS" → "JavaScript", "K8s" → "Kubernetes")
- Extract implicit requirements (e.g., "senior Java dev" → skills: ["Java"], seniority: "Senior")
- "developer", "engineer", "architect" are job titles/positions, NOT skills
- For experience: "5+ years" → min_experience: 5, "3-5 years" → min_experience: 3, max_experience: 5
- Return empty arrays for missing criteria, not null
- If no specific seniority mentioned, leave it empty string ""

Now analyze this query and return ONLY the JSON:`, query)

	response, err := a.llmClient.Generate(prompt)
	if err != nil {
		return nil, fmt.Errorf("LLM query analysis failed: %w", err)
	}

	log.Printf("[GraphRAG] LLM analysis response: %s", response)

	var criteria SearchCriteria
	if err := json.Unmarshal([]byte(response), &criteria); err != nil {
		return nil, fmt.Errorf("failed to parse LLM response: %w\nResponse: %s", err, response)
	}

	log.Printf("[GraphRAG] Extracted criteria: %+v", criteria)
	return &criteria, nil
}

// FilterCandidatesWithLLM asks the LLM to filter the candidate list according to the
// original natural language query and the extracted criteria. It returns the subset
// of candidates the LLM deems matching (preserves original order). On LLM failure,
// it returns the original candidates and an error.
func (a *QueryAnalyzer) FilterCandidatesWithLLM(ctx context.Context, query string, criteria *SearchCriteria, candidates []CandidateResult) ([]CandidateResult, error) {
	var b strings.Builder
	b.WriteString("You are a recruiter assistant. Given the user's query and structured criteria, decide which of the following candidates match the request. Return ONLY a JSON object:\n")
	b.WriteString("{\"keep\": [\"person_id_1\", \"person_id_2\"]}\n")
	b.WriteString("Rules and mappings:\n")
	b.WriteString("- Use the structured criteria if present (skills, positions, seniority, companies, experience).\n")
	b.WriteString("- When criteria.Positions contains a job title, require that the candidate's current_position OR any company-held position contains that title (case-insensitive substring).\n")
	b.WriteString("- Do NOT add hard-coded special-case rules for a single title (e.g., do not treat 'Analist' as an exceptional case). If synonyms are relevant, prefer generic synonym reasoning based on the query/context.\n")
	b.WriteString("- If the candidate has no current_position and no company position, treat them as NOT matching a strict position filter.\n")
	b.WriteString("- If the query asks broadly for a position title, the LLM may consider common synonyms or language variants, but avoid special-casing a single title.\n")
	b.WriteString("- If unsure, be conservative and exclude borderline candidates.\n\n")

	b.WriteString("User Query:\n" + query + "\n\n")
	if critJSON, err := json.Marshal(criteria); err == nil {
		b.WriteString("Structured Criteria:\n" + string(critJSON) + "\n\n")
	}

	b.WriteString("Candidates:\n")
	for i, c := range candidates {
		b.WriteString(fmt.Sprintf("%d) person_id: %s\n", i+1, c.PersonID))
		b.WriteString(fmt.Sprintf("   name: %s\n", c.Name))
		if c.CurrentPosition != "" {
			b.WriteString(fmt.Sprintf("   current_position: %s\n", c.CurrentPosition))
		}
		if len(c.Companies) > 0 {
			comps := []string{}
			for _, co := range c.Companies {
				if co.Position != "" {
					comps = append(comps, fmt.Sprintf("%s (%s)", co.Name, co.Position))
				} else {
					comps = append(comps, co.Name)
				}
			}
			b.WriteString(fmt.Sprintf("   companies: %s\n", strings.Join(comps, ", ")))
		}
		if len(c.Skills) > 0 {
			skills := []string{}
			for _, s := range c.Skills {
				skills = append(skills, s.Name)
			}
			b.WriteString(fmt.Sprintf("   skills: %s\n", strings.Join(skills, ", ")))
		}
		b.WriteString("\n")
	}

	prompt := b.String()

	// Use a deterministic LLM call (low temperature ideally set in the provider config)
	resp, err := a.llmClient.Generate(prompt)
	if err != nil {
		return candidates, fmt.Errorf("LLM filtering failed: %w", err)
	}

	var out struct {
		Keep []string `json:"keep"`
	}
	if err := json.Unmarshal([]byte(resp), &out); err != nil {
		return candidates, fmt.Errorf("failed to parse LLM filter response: %w; resp: %s", err, resp)
	}

	keepSet := map[string]struct{}{}
	for _, id := range out.Keep {
		keepSet[id] = struct{}{}
	}

	filtered := make([]CandidateResult, 0, len(out.Keep))
	for _, c := range candidates {
		if _, ok := keepSet[c.PersonID]; ok {
			filtered = append(filtered, c)
		}
	}

	return filtered, nil
}
