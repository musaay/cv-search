-- Extended Features: CV Files and Entity Extraction
-- This migration adds:
-- 1. CV file storage and parsing
-- 2. Entity extraction from CVs

-- =====================================================
-- CV Files and Entity Extraction
-- =====================================================

-- CV Files table
CREATE TABLE IF NOT EXISTS cv_files (
    id SERIAL PRIMARY KEY,
    candidate_id INTEGER REFERENCES candidates(id) ON DELETE CASCADE,
    filename TEXT NOT NULL,
    file_path TEXT NOT NULL,
    file_type TEXT NOT NULL, -- pdf, docx, txt
    file_size INTEGER,
    parsed_text TEXT, -- Full text content
    uploaded_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    parsed_at TIMESTAMP WITH TIME ZONE
);

-- Index for faster lookups
CREATE INDEX IF NOT EXISTS idx_cv_files_candidate_id ON cv_files(candidate_id);
CREATE INDEX IF NOT EXISTS idx_cv_files_uploaded_at ON cv_files(uploaded_at);

-- Extracted entities from CVs
CREATE TABLE IF NOT EXISTS cv_entities (
    id SERIAL PRIMARY KEY,
    cv_file_id INTEGER REFERENCES cv_files(id) ON DELETE CASCADE,
    entity_type TEXT NOT NULL, -- skill, company, education, certification
    entity_value TEXT NOT NULL,
    confidence FLOAT DEFAULT 0.0, -- LLM extraction confidence
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_cv_entities_cv_file_id ON cv_entities(cv_file_id);
CREATE INDEX IF NOT EXISTS idx_cv_entities_type ON cv_entities(entity_type);
