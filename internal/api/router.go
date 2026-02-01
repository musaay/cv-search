package api

import (
	"net/http"

	httpSwagger "github.com/swaggo/http-swagger"
)

func NewRouter(a *API) http.Handler {
	mux := http.NewServeMux()

	// Swagger documentation - must be registered first
	mux.Handle("/swagger/", httpSwagger.Handler(
		httpSwagger.URL("http://localhost:8080/swagger/doc.json"),
	))

	// Health check (for Railway, k8s, etc.)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"healthy"}`))
	})

	// API endpoints
	mux.HandleFunc("/api/search", a.SearchHandler)

	// CV & Graph endpoints
	mux.HandleFunc("/api/cv/upload", a.CVUploadHandler)
	mux.HandleFunc("/api/graph/stats", a.GetGraphStatsHandler)
	mux.HandleFunc("/api/graph/skills/popular", a.GetPopularSkillsHandler)

	// GraphRAG endpoints
	mux.HandleFunc("/api/graphrag/search", a.GraphRAGSearchHandler)
	mux.HandleFunc("/api/graphrag/embeddings/generate", a.GenerateEmbeddingsHandler)
	mux.HandleFunc("/api/graphrag/communities/detect", a.DetectCommunitiesHandler)

	// Hybrid Search endpoint (BM25 + Vector + Graph + LLM)
	mux.HandleFunc("/api/search/hybrid", a.HybridSearchHandler)

	return mux
}
