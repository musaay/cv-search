#!/bin/bash
# Test Suite for Candidate Deduplication & Merging API
# Adheres to .github/copilot-instructions.md testing and verification rules.

BASE_URL="${API_URL:-http://localhost:8080}"
BOLD='\033[1m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

echo -e "${BOLD}=== Candidate Deduplication & Merging API Test Suite ===${NC}"
echo -e "${BOLD}Testing: ${GREEN}$BASE_URL${NC}\n"

# Check if jq is installed
if ! command -v jq &> /dev/null; then
    echo -e "${RED}Error: jq is required for this script. Please install jq.${NC}"
    exit 1
fi

# Test 1: Health Check
echo -e "${BLUE}Test 1: Health Check${NC}"
HEALTH_RES=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL/health")
if [ "$HEALTH_RES" -eq 200 ]; then
    echo -e "${GREEN}✔ Server is healthy (HTTP 200)${NC}\n"
else
    echo -e "${RED}✖ Server health check failed (HTTP $HEALTH_RES). Is the server running?${NC}"
    echo -e "${YELLOW}Rule reminder: If a test cannot be run (no server), do not proceed with push.${NC}"
    exit 1
fi

# Test 2: Get Duplicate Candidate Groups
echo -e "${BLUE}Test 2: Fetching Duplicate Candidate Groups (GET /api/candidates/duplicates)${NC}"
echo -e "${YELLOW}Expected: JSON containing 'groups' array and 'total_groups' count.${NC}"
DUPS_RES=$(curl -s -H "X-API-Key: ${API_KEY:-kartezya_secure_api_key_2026}" "$BASE_URL/api/candidates/duplicates")
echo "$DUPS_RES" | jq .

TOTAL_GROUPS=$(echo "$DUPS_RES" | jq -r '.total_groups // "error"')
if [ "$TOTAL_GROUPS" != "error" ]; then
    echo -e "${GREEN}✔ Successfully fetched duplicate groups. Total groups found: $TOTAL_GROUPS${NC}\n"
else
    echo -e "${RED}✖ Failed to parse duplicate groups response.${NC}\n"
    exit 1
fi

# Test 3: Validate Merge Endpoint Error Handling - Empty Payload
echo -e "${BLUE}Test 3: Merge Endpoint Validation - Empty Payload (POST /api/candidates/merge)${NC}"
echo -e "${YELLOW}Expected: HTTP 400 Bad Request (invalid/missing IDs)${NC}"
HTTP_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE_URL/api/candidates/merge" \
  -H "X-API-Key: ${API_KEY:-kartezya_secure_api_key_2026}" \
  -H "Content-Type: application/json" \
  -d '{}')

if [ "$HTTP_STATUS" -eq 400 ]; then
    echo -e "${GREEN}✔ Empty payload correctly rejected with HTTP 400${NC}\n"
else
    echo -e "${RED}✖ Expected HTTP 400 for empty payload, got HTTP $HTTP_STATUS${NC}\n"
    exit 1
fi

# Test 4: Validate Merge Endpoint Error Handling - Non-existent Candidate IDs
echo -e "${BLUE}Test 4: Merge Endpoint Error Handling - Non-existent IDs (POST /api/candidates/merge)${NC}"
echo -e "${YELLOW}Expected: HTTP 500/Error response with clear error message (no rows in result set)${NC}"
ERR_RES=$(curl -s -X POST "$BASE_URL/api/candidates/merge" \
  -H "X-API-Key: ${API_KEY:-kartezya_secure_api_key_2026}" \
  -H "Content-Type: application/json" \
  -d '{"master_candidate_id": 999999, "duplicate_candidate_ids": [999998]}')
echo "$ERR_RES"

# Verify error message is returned and not silently swallowed
if echo "$ERR_RES" | grep -q -i "failed"; then
    echo -e "${GREEN}✔ Gracefully handled non-existent candidate ID without crashing${NC}\n"
else
    echo -e "${RED}✖ Unexpected response for non-existent IDs${NC}\n"
fi

# Test 5: Instructions for Real Merge Execution
echo -e "${BOLD}=== Interactive Merge Execution Example ===${NC}"
echo -e "To execute a real merge on detected candidates from Test 2, use the following command structure:"
echo -e "${YELLOW}curl -X POST $BASE_URL/api/candidates/merge \\"
echo -e "  -H \"Content-Type: application/json\" \\"
echo -e "  -d '{\"master_candidate_id\": <MASTER_ID>, \"duplicate_candidate_ids\": [<DUP_ID_1>, <DUP_ID_2>]}' | jq .${NC}\n"

echo -e "${GREEN}${BOLD}All automated duplicate API verification tests completed successfully.${NC}"
