-- Enable pgvector extension for vector embeddings
CREATE EXTENSION IF NOT EXISTS vector;

-- Graph nodes table with embeddings (complete version)
DROP TABLE IF EXISTS graph_nodes CASCADE;
CREATE TABLE graph_nodes (
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

CREATE INDEX idx_graph_nodes_type ON graph_nodes(node_type);
CREATE INDEX idx_graph_nodes_node_id ON graph_nodes(node_id);
CREATE INDEX idx_graph_nodes_embedding ON graph_nodes USING hnsw (embedding vector_cosine_ops);

-- Graph edges table
DROP TABLE IF EXISTS graph_edges CASCADE;
CREATE TABLE graph_edges (
    id SERIAL PRIMARY KEY,
    source_node_id INTEGER REFERENCES graph_nodes(id) ON DELETE CASCADE,
    target_node_id INTEGER REFERENCES graph_nodes(id) ON DELETE CASCADE,
    edge_type TEXT NOT NULL,
    properties JSONB,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX idx_graph_edges_source ON graph_edges(source_node_id);
CREATE INDEX idx_graph_edges_target ON graph_edges(target_node_id);
CREATE INDEX idx_graph_edges_type ON graph_edges(edge_type);

-- Candidate scoring results
DROP TABLE IF EXISTS candidate_scores CASCADE;
CREATE TABLE candidate_scores (
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

CREATE INDEX idx_candidate_scores_candidate_id ON candidate_scores(candidate_id);
CREATE INDEX idx_candidate_scores_query_id ON candidate_scores(query_id);
CREATE INDEX idx_candidate_scores_total_score ON candidate_scores(total_score DESC);
CREATE INDEX idx_candidate_scores_created_at ON candidate_scores(created_at);

-- Communities with embeddings
DROP TABLE IF EXISTS graph_communities CASCADE;
CREATE TABLE graph_communities (
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

CREATE INDEX idx_communities_embedding ON graph_communities USING hnsw (embedding vector_cosine_ops);

-- Community members
DROP TABLE IF EXISTS community_members CASCADE;
CREATE TABLE community_members (
    id SERIAL PRIMARY KEY,
    community_id INTEGER REFERENCES graph_communities(id) ON DELETE CASCADE,
    node_id INTEGER REFERENCES graph_nodes(id) ON DELETE CASCADE,
    membership_strength FLOAT DEFAULT 1.0,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    UNIQUE(community_id, node_id)
);

CREATE INDEX idx_community_members_community ON community_members(community_id);
CREATE INDEX idx_community_members_node ON community_members(node_id);

-- View for easy querying
CREATE OR REPLACE VIEW community_details AS
SELECT 
    c.id,
    c.level,
    c.community_id,
    c.title,
    c.summary,
    c.node_count,
    COUNT(cm.node_id) as actual_member_count,
    ARRAY_AGG(DISTINCT gn.node_type) as member_types,
    ARRAY_AGG(gn.properties->>'name') as member_names
FROM graph_communities c
LEFT JOIN community_members cm ON c.id = cm.community_id
LEFT JOIN graph_nodes gn ON cm.node_id = gn.id
GROUP BY c.id, c.level, c.community_id, c.title, c.summary, c.node_count;

COMMENT ON TABLE graph_communities IS 'Detected communities from Leiden algorithm';
COMMENT ON TABLE community_members IS 'Nodes belonging to each community';
COMMENT ON COLUMN graph_nodes.embedding IS 'Vector embedding for semantic search (1536-dim)';
