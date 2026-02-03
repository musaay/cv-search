package api

import (
	"encoding/json"
	"net/http"
	"os"

	"cv-search/internal/cv"
	"cv-search/internal/graphrag"
	"cv-search/internal/llm"
	"cv-search/internal/storage"
)

type API struct {
	db                   *storage.DB
	cvParser             *cv.CVParser
	llmService           *llm.Service
	graphBuilder         *graphrag.GraphBuilder
	llmSearchEngine      *graphrag.LLMSearchEngine      // LLM-only semantic search
	enhancedSearchEngine *graphrag.EnhancedSearchEngine // Vector + Community + LLM search (Microsoft GraphRAG)
	hybridSearchEngine   *graphrag.HybridSearchEngine   // BM25 + Vector + Graph + LLM reranking
	cvProcessingQueue    chan CVProcessingJob           // Background queue for async CV processing (LLM + Graph)
	embeddingQueue       chan EmbeddingJob              // Background queue for async embedding generation
}

func NewAPI(db *storage.DB) *API {
	// Initialize CV parser
	uploadsDir := os.Getenv("UPLOADS_DIR")
	if uploadsDir == "" {
		uploadsDir = "./uploads"
	}
	cvParser := cv.NewCVParser(uploadsDir)

	// Initialize LLM service (if configured)
	var llmSvc *llm.Service
	llmProvider := os.Getenv("LLM_PROVIDER")

	if llmProvider != "" && llmProvider != "none" {
		llmModel := os.Getenv("LLM_MODEL")
		llmAPIKey := ""

		// Get API key based on provider
		if llmProvider == "groq" {
			llmAPIKey = os.Getenv("GROQ_API_KEY")
		} else if llmProvider == "openai" {
			llmAPIKey = os.Getenv("OPENAI_API_KEY")
		}

		if llmAPIKey != "" {
			llmSvc = llm.NewService(llmProvider, llmAPIKey, llmModel)
		}
	}

	// Initialize graph builder
	graphBuilder := graphrag.NewGraphBuilder(db.GetConnection())

	// Initialize GraphRAG search engines
	var llmSearchEngine *graphrag.LLMSearchEngine           // LLM-only search
	var enhancedSearchEngine *graphrag.EnhancedSearchEngine // Vector + Community + LLM
	var hybridSearchEngine *graphrag.HybridSearchEngine     // Multi-source fusion + LLM

	if llmSvc != nil {
		llmAdapter := graphrag.NewLLMAdapter(llmSvc)
		llmSearchEngine = graphrag.NewLLMSearchEngine(db.GetConnection(), llmAdapter)

		// Initialize Enhanced Search Engine with OpenAI embeddings
		openaiAPIKey := os.Getenv("OPENAI_API_KEY")
		if openaiAPIKey != "" && openaiAPIKey != "your_openai_api_key_here" {
			enhancedSearchEngine = graphrag.NewEnhancedSearchEngine(db.GetConnection(), llmAdapter, openaiAPIKey)
			// Initialize Hybrid Search Engine (BM25 + Vector + Graph + LLM)
			hybridSearchEngine = graphrag.NewHybridSearchEngine(db.GetConnection(), llmAdapter, openaiAPIKey)
		}
	}

	api := &API{
		db:                   db,
		cvParser:             cvParser,
		llmService:           llmSvc,
		graphBuilder:         graphBuilder,
		llmSearchEngine:      llmSearchEngine,
		enhancedSearchEngine: enhancedSearchEngine,
		hybridSearchEngine:   hybridSearchEngine,
		cvProcessingQueue:    make(chan CVProcessingJob, 50), // Buffer for 50 CV processing jobs
		embeddingQueue:       make(chan EmbeddingJob, 100),   // Buffer for 100 embedding jobs
	}

	// Start background workers
	api.StartBackgroundWorkers()

	return api
}

// SearchHandler searches for candidates in database
// @Summary Search candidates
// @Description Search for candidates based on criteria (name, location, skills)
// @Tags candidates
// @Accept json
// @Produce json
// @Param criteria body storage.Criteria true "Search criteria"
// @Success 200 {array} storage.Candidate
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /search [post]
func (a *API) SearchHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var crit storage.Criteria
	if err := json.NewDecoder(r.Body).Decode(&crit); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	candidates, err := a.db.SearchCandidates(r.Context(), &crit)
	if err != nil {
		http.Error(w, "search error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(candidates)
}
