-- =====================================================
-- Complete Database Setup for CV Search & GraphRAG
-- =====================================================
-- Run this file once to set up the entire database schema

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
    graph_node_id INT,  -- Linked to graph_nodes(id), populated after graph build
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
COMMENT ON COLUMN candidates.graph_node_id IS 'FK to graph_nodes.id for the person node; added after graph build';

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
    parsed_at TIMESTAMP WITH TIME ZONE,
    content_hash TEXT,  -- SHA-256 hash for duplicate detection
    job_id INTEGER      -- References cv_upload_jobs(id), added later via FK
);

CREATE INDEX IF NOT EXISTS idx_cv_files_candidate_id ON cv_files(candidate_id);
CREATE INDEX IF NOT EXISTS idx_cv_files_uploaded_at ON cv_files(uploaded_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_cv_files_content_hash ON cv_files(content_hash);
CREATE INDEX IF NOT EXISTS idx_cv_files_filename_size ON cv_files(filename, file_size);

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
COMMENT ON COLUMN cv_files.content_hash IS 'SHA-256 hash of parsed CV text for duplicate detection';
COMMENT ON TABLE cv_entities IS 'Extracted entities (skills, companies, etc.)';

-- =====================================================
-- 3. VECTOR EXTENSION
-- =====================================================

CREATE EXTENSION IF NOT EXISTS vector;

-- =====================================================
-- 4. GRAPH DATA (Nodes, Edges, Communities)
-- =====================================================

CREATE TABLE IF NOT EXISTS graph_nodes (
    id SERIAL PRIMARY KEY,
    node_type TEXT NOT NULL,
    node_id TEXT NOT NULL,
    properties JSONB,
    embedding vector(1536),
    embedding_model TEXT,
    embedding_created_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    UNIQUE(node_type, node_id)
);

CREATE INDEX IF NOT EXISTS idx_graph_nodes_type ON graph_nodes(node_type);
CREATE INDEX IF NOT EXISTS idx_graph_nodes_node_id ON graph_nodes(node_id);
CREATE INDEX IF NOT EXISTS idx_graph_nodes_embedding ON graph_nodes USING hnsw (embedding vector_cosine_ops);

CREATE TABLE IF NOT EXISTS graph_edges (
    id SERIAL PRIMARY KEY,
    source_node_id INTEGER REFERENCES graph_nodes(id) ON DELETE CASCADE,
    target_node_id INTEGER REFERENCES graph_nodes(id) ON DELETE CASCADE,
    edge_type TEXT NOT NULL,
    properties JSONB,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_graph_edges_source ON graph_edges(source_node_id);
CREATE INDEX IF NOT EXISTS idx_graph_edges_target ON graph_edges(target_node_id);
CREATE INDEX IF NOT EXISTS idx_graph_edges_type ON graph_edges(edge_type);

COMMENT ON TABLE graph_nodes IS 'Graph nodes for GraphRAG (person, skill, company, etc.)';
COMMENT ON TABLE graph_edges IS 'Graph edges (has_skill, worked_at, etc.)';
COMMENT ON COLUMN graph_nodes.embedding IS 'Vector embedding for semantic search (1536-dim)';

-- =====================================================
-- 5. COMMUNITIES (Leiden Algorithm)
-- =====================================================

CREATE TABLE IF NOT EXISTS graph_communities (
    id SERIAL PRIMARY KEY,
    level INTEGER NOT NULL,
    community_id TEXT NOT NULL,
    title TEXT,
    summary TEXT,
    node_count INTEGER DEFAULT 0,
    embedding vector(1536),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    UNIQUE(level, community_id)
);

CREATE INDEX IF NOT EXISTS idx_communities_embedding ON graph_communities USING hnsw (embedding vector_cosine_ops);

CREATE TABLE IF NOT EXISTS community_members (
    id SERIAL PRIMARY KEY,
    community_id INTEGER REFERENCES graph_communities(id) ON DELETE CASCADE,
    node_id INTEGER REFERENCES graph_nodes(id) ON DELETE CASCADE,
    membership_strength FLOAT DEFAULT 1.0,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    UNIQUE(community_id, node_id)
);

CREATE INDEX IF NOT EXISTS idx_community_members_community ON community_members(community_id);
CREATE INDEX IF NOT EXISTS idx_community_members_node ON community_members(node_id);

-- Community details view
CREATE OR REPLACE VIEW community_details AS
SELECT 
    c.id,
    c.level,
    c.community_id,
    c.title,
    c.summary,
    c.node_count,
    COUNT(cm.node_id) as actual_member_count,
    ARRAY_AGG(DISTINCT gn.node_type) FILTER (WHERE gn.node_type IS NOT NULL) as member_types,
    ARRAY_AGG(gn.properties->>'name') FILTER (WHERE gn.properties->>'name' IS NOT NULL) as member_names
FROM graph_communities c
LEFT JOIN community_members cm ON c.id = cm.community_id
LEFT JOIN graph_nodes gn ON cm.node_id = gn.id
GROUP BY c.id, c.level, c.community_id, c.title, c.summary, c.node_count;

COMMENT ON TABLE graph_communities IS 'Detected communities from Leiden algorithm';
COMMENT ON TABLE community_members IS 'Nodes belonging to each community';

-- =====================================================
-- 6. CANDIDATE SCORING (Search Results)
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

COMMENT ON TABLE candidate_scores IS 'Hybrid search results with scoring breakdown';

-- =====================================================
-- 7. ASYNC CV UPLOAD JOBS (Background Processing)
-- =====================================================

CREATE TABLE IF NOT EXISTS cv_upload_jobs (
    id SERIAL PRIMARY KEY,
    cv_file_id INTEGER NOT NULL REFERENCES cv_files(id) ON DELETE CASCADE,
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    -- Status: pending, processing, completed, failed
    
    error_message TEXT,
    progress JSONB DEFAULT '{}',
    -- Progress tracking: {"step": "extraction|graph|embedding", "entities_count": 10}
    
    created_at TIMESTAMP DEFAULT NOW(),
    started_at TIMESTAMP,
    completed_at TIMESTAMP,
    
    retry_count INTEGER DEFAULT 0,
    max_retries INTEGER DEFAULT 3
);

-- Index for job status queries
CREATE INDEX IF NOT EXISTS idx_cv_upload_jobs_status ON cv_upload_jobs(status);
CREATE INDEX IF NOT EXISTS idx_cv_upload_jobs_cv_file ON cv_upload_jobs(cv_file_id);

COMMENT ON TABLE cv_upload_jobs IS 'Tracks async CV processing jobs (LLM extraction + graph building)';
COMMENT ON COLUMN cv_upload_jobs.status IS 'Job status: pending, processing, completed, failed';
COMMENT ON COLUMN cv_upload_jobs.progress IS 'JSON object tracking processing progress';

-- Add foreign key constraint for cv_files.job_id (now that cv_upload_jobs exists)
DO $$ 
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints 
        WHERE constraint_name = 'cv_files_job_id_fkey' AND table_name = 'cv_files'
    ) THEN
        ALTER TABLE cv_files ADD CONSTRAINT cv_files_job_id_fkey 
            FOREIGN KEY (job_id) REFERENCES cv_upload_jobs(id);
    END IF;
END $$;

-- =====================================================
-- 7b. GROQ BATCH API TRACKING (Bulk CV Extraction)
-- =====================================================
-- Large bulk uploads (and offline reprocessing tools) submit CV extraction
-- requests as a single Groq Batch API job instead of one-by-one synchronous
-- calls, avoiding the standard per-model rate limit entirely (Batch API uses
-- a separate quota) and cutting cost 50%. Results arrive within 24h-7d
-- (usually much faster) and are applied by a background poller.

ALTER TABLE cv_upload_jobs ADD COLUMN IF NOT EXISTS groq_batch_id TEXT;
CREATE INDEX IF NOT EXISTS idx_cv_upload_jobs_groq_batch_id ON cv_upload_jobs(groq_batch_id);
COMMENT ON COLUMN cv_upload_jobs.groq_batch_id IS 'Set when this job was submitted as part of a Groq Batch API job instead of the real-time queue; status becomes batch_submitted until the poller applies results';

CREATE TABLE IF NOT EXISTS llm_batch_jobs (
    id SERIAL PRIMARY KEY,
    groq_batch_id TEXT NOT NULL UNIQUE,
    input_file_id TEXT,
    output_file_id TEXT,
    error_file_id TEXT,
    status TEXT NOT NULL DEFAULT 'submitted',
    -- Status mirrors Groq's batch status: submitted (local only), validating,
    -- in_progress, finalizing, completed, failed, expired, cancelled
    request_count INTEGER DEFAULT 0,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    completed_at TIMESTAMP WITH TIME ZONE
);

CREATE INDEX IF NOT EXISTS idx_llm_batch_jobs_status ON llm_batch_jobs(status);

COMMENT ON TABLE llm_batch_jobs IS 'Tracks Groq Batch API submissions used for large bulk CV extraction / backlog reprocessing';

-- =====================================================
-- 8. INTERVIEWS (Per-Candidate Interview Records)
-- =====================================================

CREATE TABLE IF NOT EXISTS interviews (
    id               SERIAL PRIMARY KEY,
    candidate_id     INT NOT NULL REFERENCES candidates(id) ON DELETE CASCADE,
    interview_date   DATE NOT NULL,
    team             TEXT,
    interviewer_name TEXT,
    interview_type   TEXT CHECK (interview_type IN ('technical', 'hr', 'case_study', 'other')),
    notes            TEXT,
    outcome          TEXT CHECK (outcome IN ('passed', 'failed', 'pending')),
    created_at       TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at       TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_interviews_candidate_id   ON interviews(candidate_id);
CREATE INDEX IF NOT EXISTS idx_interviews_interview_date ON interviews(interview_date);

DROP TRIGGER IF EXISTS interviews_updated_at ON interviews;
CREATE TRIGGER interviews_updated_at
    BEFORE UPDATE ON interviews
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

COMMENT ON TABLE interviews IS 'Interview records per candidate; multiple rounds and teams supported';

-- =====================================================
-- SETUP COMPLETE
-- =====================================================
-- Tables created:
-- - candidates (with full-text search + graph_node_id)
-- - cv_files, cv_entities
-- - graph_nodes, graph_edges (with vector embeddings)
-- - graph_communities, community_members
-- - candidate_scores
-- - cv_upload_jobs (async processing)
-- - interviews (per-candidate interview records)
-- Extensions: pgvector
