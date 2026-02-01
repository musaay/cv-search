-- =====================================================
-- Complete Database Setup for CV Search (No Vector)
-- =====================================================
-- For Railway PostgreSQL without pgvector extension
-- Graph and vector search features will be disabled

-- =====================================================
-- 1. CANDIDATES TABLE (Core CV Data)
-- =====================================================

CREATE TABLE IF NOT EXISTS candidates (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    email TEXT,
    phone TEXT,
    location TEXT,
    linkedin_url TEXT,
    skills TEXT,
    experience TEXT,
    education TEXT,
    summary TEXT,
    search_vector tsvector,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- Indexes for candidates
CREATE INDEX IF NOT EXISTS idx_candidates_name ON candidates(name);
CREATE INDEX IF NOT EXISTS idx_candidates_email ON candidates(email);
CREATE INDEX IF NOT EXISTS idx_candidates_location ON candidates(location);
CREATE INDEX IF NOT EXISTS idx_candidates_created_at ON candidates(created_at);
CREATE INDEX IF NOT EXISTS idx_candidates_search_vector ON candidates USING GIN(search_vector);

-- Full-text search trigger function
CREATE OR REPLACE FUNCTION candidates_search_vector_update() 
RETURNS TRIGGER AS $$
BEGIN
    NEW.search_vector := 
        setweight(to_tsvector('english', COALESCE(NEW.name, '')), 'A') ||
        setweight(to_tsvector('english', COALESCE(NEW.skills, '')), 'A') ||
        setweight(to_tsvector('english', COALESCE(NEW.experience, '')), 'B') ||
        setweight(to_tsvector('english', COALESCE(NEW.location, '')), 'C');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Trigger to auto-update search_vector
DROP TRIGGER IF EXISTS tsvector_update ON candidates;
CREATE TRIGGER tsvector_update 
    BEFORE INSERT OR UPDATE ON candidates
    FOR EACH ROW 
    EXECUTE FUNCTION candidates_search_vector_update();

-- Updated_at trigger function
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS update_candidates_updated_at ON candidates;
CREATE TRIGGER update_candidates_updated_at
    BEFORE UPDATE ON candidates
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

-- Comments
COMMENT ON TABLE candidates IS 'Main table for candidate profiles';
COMMENT ON COLUMN candidates.search_vector IS 'tsvector for full-text search (BM25-style)';

-- =====================================================
-- 2. CV FILES & ENTITY EXTRACTION
-- =====================================================

CREATE TABLE IF NOT EXISTS cv_files (
    id SERIAL PRIMARY KEY,
    candidate_id INTEGER REFERENCES candidates(id) ON DELETE CASCADE,
    filename TEXT NOT NULL,
    file_path TEXT NOT NULL,
    file_type TEXT NOT NULL,
    file_size INTEGER,
    parsed_text TEXT,
    uploaded_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    parsed_at TIMESTAMP WITH TIME ZONE
);

CREATE INDEX IF NOT EXISTS idx_cv_files_candidate_id ON cv_files(candidate_id);
CREATE INDEX IF NOT EXISTS idx_cv_files_uploaded_at ON cv_files(uploaded_at);

CREATE TABLE IF NOT EXISTS cv_entities (
    id SERIAL PRIMARY KEY,
    cv_file_id INTEGER REFERENCES cv_files(id) ON DELETE CASCADE,
    entity_type TEXT NOT NULL,
    entity_value TEXT NOT NULL,
    confidence FLOAT DEFAULT 0.0,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_cv_entities_cv_file_id ON cv_entities(cv_file_id);
CREATE INDEX IF NOT EXISTS idx_cv_entities_type ON cv_entities(entity_type);

COMMENT ON TABLE cv_files IS 'Uploaded CV files and parsed text';
COMMENT ON TABLE cv_entities IS 'Extracted entities (skills, companies, etc.)';

-- =====================================================
-- 3. CANDIDATE SCORING (Search Results)
-- =====================================================

CREATE TABLE IF NOT EXISTS candidate_scores (
    id SERIAL PRIMARY KEY,
    candidate_id INTEGER REFERENCES candidates(id) ON DELETE CASCADE,
    query_id TEXT NOT NULL,
    criteria JSONB,
    total_score FLOAT NOT NULL,
    skill_score FLOAT DEFAULT 0.0,
    experience_score FLOAT DEFAULT 0.0,
    location_score FLOAT DEFAULT 0.0,
    education_score FLOAT DEFAULT 0.0,
    match_details JSONB,
    ranked_position INTEGER,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_candidate_scores_candidate_id ON candidate_scores(candidate_id);
CREATE INDEX IF NOT EXISTS idx_candidate_scores_query_id ON candidate_scores(query_id);
CREATE INDEX IF NOT EXISTS idx_candidate_scores_total_score ON candidate_scores(total_score DESC);
CREATE INDEX IF NOT EXISTS idx_candidate_scores_created_at ON candidate_scores(created_at);

COMMENT ON TABLE candidate_scores IS 'Search results with scoring (BM25 + LLM only)';

-- =====================================================
-- SETUP COMPLETE (Without Vector/Graph Features)
-- =====================================================
-- Tables created:
-- - candidates (with full-text search)
-- - cv_files, cv_entities
-- - candidate_scores
--
-- NOTE: Graph features disabled (no pgvector)
-- You can still use BM25 search and LLM scoring
