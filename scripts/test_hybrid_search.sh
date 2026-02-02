#!/bin/bash
# Test Suite for Hybrid Search API

BASE_URL="${API_URL:-http://localhost:8080}"
BOLD='\033[1m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${BOLD}=== Hybrid Search API Test Suite ===${NC}"
echo -e "${BOLD}Testing: ${GREEN}$BASE_URL${NC}\n"

# Test 1: Health Check
echo -e "${BLUE}Test 1: Health Check${NC}"
curl -s $BASE_URL/health | jq .
echo -e "\n"

# Test 2: Banking Experience Query (should rank Mehmet #1)
echo -e "${BLUE}Test 2: Bankada çalışmış senior developer${NC}"
echo -e "${YELLOW}Expected: Mehmet Öz (Architect at Akbank) should be #1${NC}"
curl -s -X POST $BASE_URL/api/search/hybrid \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Bankada çalışmış senior developer",
    "final_top_n": 10
  }' | jq '.candidates[] | {rank, name, llm_score, llm_reasoning}'
echo -e "\n"

# Test 3: Full Stack Developer Query
echo -e "${BLUE}Test 3: Full stack developer${NC}"
echo -e "${YELLOW}Expected: Merve Birsin (Full Stack Engineer) should be high${NC}"
curl -s -X POST $BASE_URL/api/search/hybrid \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Full stack developer",
    "final_top_n": 10
  }' | jq '.candidates[] | {rank, name, llm_score, llm_reasoning}'
echo -e "\n"

# Test 4: Architect Role Query
echo -e "${BLUE}Test 4: Software architect${NC}"
echo -e "${YELLOW}Expected: Mehmet Öz (Senior Software Architect) should be #1${NC}"
curl -s -X POST $BASE_URL/api/search/hybrid \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Software architect with cloud experience",
    "final_top_n": 10
  }' | jq '.candidates[] | {rank, name, position: .name, llm_score}'
echo -e "\n"

# Test 5: Product Owner Query
echo -e "${BLUE}Test 5: Product owner${NC}"
echo -e "${YELLOW}Expected: Emine Yürektürk Ay (Product Owner) should be #1${NC}"
curl -s -X POST $BASE_URL/api/search/hybrid \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Product owner with business analysis experience",
    "final_top_n": 10
  }' | jq '.candidates[] | {rank, name, llm_score}'
echo -e "\n"

# Test 6: Java Developer Query
echo -e "${BLUE}Test 6: Java developer${NC}"
echo -e "${YELLOW}Expected: Mücahit Şahin (Java Developer) should be high${NC}"
curl -s -X POST $BASE_URL/api/search/hybrid \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Java backend developer",
    "final_top_n": 10
  }' | jq '.candidates[] | {rank, name, llm_score, llm_reasoning}'
echo -e "\n"

# Test 7: Custom Weights - BM25 Heavy (Keyword Match Priority)
echo -e "${BLUE}Test 7: Custom weights - BM25 heavy (keyword priority)${NC}"
curl -s -X POST $BASE_URL/api/search/hybrid \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Architect",
    "bm25_weight": 0.6,
    "vector_weight": 0.2,
    "graph_weight": 0.2,
    "final_top_n": 10
  }' | jq '{config: .config, top_candidates: [.candidates[] | {rank, name, bm25_score, vector_score, graph_score, llm_score}]}'
echo -e "\n"

# Test 8: Custom Weights - Vector Heavy (Semantic Priority)
echo -e "${BLUE}Test 8: Custom weights - Vector heavy (semantic priority)${NC}"
curl -s -X POST $BASE_URL/api/search/hybrid \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Experienced professional with leadership skills",
    "bm25_weight": 0.2,
    "vector_weight": 0.5,
    "graph_weight": 0.3,
    "final_top_n": 10
  }' | jq '{config: .config, top_candidates: [.candidates[] | {rank, name, vector_score, llm_score}]}'
echo -e "\n"

# Test 9: Score Comparison (All Scores Visible)
echo -e "${BLUE}Test 9: Score breakdown for 'bankada çalışmış'${NC}"
curl -s -X POST $BASE_URL/api/search/hybrid \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Bankada çalışmış kişiler",
    "final_top_n": 10
  }' | jq '.candidates[] | {
    rank, 
    name, 
    scores: {
      bm25: .bm25_score,
      vector: .vector_score,
      graph: .graph_score,
      fusion: .fusion_score,
      llm_final: .llm_score
    }
  }'
echo -e "\n"

# Test 10: Performance Measurement
echo -e "${BLUE}Test 10: Performance measurement${NC}"
curl -s -X POST $BASE_URL/api/search/hybrid \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Senior developer",
    "final_top_n": 10
  }' | jq '{
    query,
    total_found,
    processing_time,
    method,
    fastest_candidate: .candidates[0] | {name, llm_score}
  }'
echo -e "\n"

echo -e "${GREEN}${BOLD}=== Test Suite Complete ===${NC}"
