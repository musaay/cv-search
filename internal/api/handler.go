package api

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"cv-search/internal/config"
	"cv-search/internal/cv"
	"cv-search/internal/graphrag"
	"cv-search/internal/llm"
	"cv-search/internal/storage"
)

// BatchJob holds per-file info within a batch upload.
type BatchJob struct {
	JobID    int64  `json:"job_id"`
	Filename string `json:"filename"`
	CVID     int64  `json:"cv_file_id"`
}

// BatchEntry groups all jobs created in a single bulk upload call.
type BatchEntry struct {
	BatchID   string     `json:"batch_id"`
	Jobs      []BatchJob `json:"jobs"`
	CreatedAt time.Time  `json:"created_at"`
}

// BatchStore is an in-memory store for batch upload state (30-min TTL).
type BatchStore struct {
	mu      sync.RWMutex
	entries map[string]*BatchEntry
	ttl     time.Duration
}

func newBatchStore(ttl time.Duration) *BatchStore {
	bs := &BatchStore{
		entries: make(map[string]*BatchEntry),
		ttl:     ttl,
	}
	go bs.cleanup()
	return bs
}

func (bs *BatchStore) set(entry *BatchEntry) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	bs.entries[entry.BatchID] = entry
}

func (bs *BatchStore) get(batchID string) (*BatchEntry, bool) {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	e, ok := bs.entries[batchID]
	return e, ok
}

func (bs *BatchStore) cleanup() {
	for {
		time.Sleep(10 * time.Minute)
		bs.mu.Lock()
		for k, v := range bs.entries {
			if time.Since(v.CreatedAt) > bs.ttl {
				delete(bs.entries, k)
			}
		}
		bs.mu.Unlock()
	}
}

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
	batchStore           *BatchStore                    // In-memory store for bulk upload batches

	// Community detection debounce — prevents redundant full recomputes when
	// multiple CVs are uploaded in quick succession.
	commDetectMu   sync.Mutex
	lastCommDetect time.Time
}

func NewAPI(db *storage.DB, cfg *config.Config) *API {
	// Initialize CV parser
	uploadsDir := cfg.UploadsDir
	if uploadsDir == "" {
		uploadsDir = "./uploads"
	}
	cvParser := cv.NewCVParser(uploadsDir)

	// Initialize LLM service (if configured)
	var llmSvc *llm.Service

	if cfg.LLMProvider != "" && cfg.LLMProvider != "none" && cfg.LLMAPIKey != "" {
		llmSvc = llm.NewService(cfg.LLMProvider, cfg.LLMAPIKey, cfg.LLMModel)
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

		// Embeddings always require OpenAI key (even when LLM provider is Groq)
		openaiKey := cfg.OpenAIAPIKey
		if openaiKey != "" && openaiKey != "your_openai_api_key_here" {
			enhancedSearchEngine = graphrag.NewEnhancedSearchEngine(db.GetConnection(), llmAdapter, openaiKey)
			hybridSearchEngine = graphrag.NewHybridSearchEngine(db.GetConnection(), llmAdapter, openaiKey, cfg.DisableLLMCache)
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
		batchStore:           newBatchStore(30 * time.Minute),
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
