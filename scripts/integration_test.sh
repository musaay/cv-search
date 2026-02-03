#!/bin/bash

# Integration tests for CV Search API
# Must pass before pushing to production

set -e

echo "🧪 Starting Integration Tests..."

BASE_URL="${BASE_URL:-http://localhost:8080}"
FAILED=0

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Test helper functions
test_passed() {
    echo -e "${GREEN}✓ PASSED:${NC} $1"
}

test_failed() {
    echo -e "${RED}✗ FAILED:${NC} $1"
    FAILED=$((FAILED + 1))
}

test_warning() {
    echo -e "${YELLOW}⚠ WARNING:${NC} $1"
}

# Test 1: API Health Check
echo ""
echo "Test 1: API Health Check"
RESPONSE=$(curl -s -X POST "$BASE_URL/api/search/hybrid" \
  -H 'Content-Type: application/json' \
  -d '{"query": "test"}' \
  -o /dev/null -w "%{http_code}")
if [ "$RESPONSE" -eq 200 ]; then
    test_passed "API is responding (HTTP $RESPONSE)"
else
    test_failed "API not responding properly (HTTP $RESPONSE)"
fi

# Test 2: Skills with Years of Experience
echo ""
echo "Test 2: Skills include years_of_experience in response"
RESPONSE=$(curl -s "$BASE_URL/api/search/hybrid" \
  -H 'Content-Type: application/json' \
  -d '{"query": "Java developer"}')

JAVA_YEARS=$(echo "$RESPONSE" | jq -r '.candidates[0].skills[]? | select(.name=="Java") | .years_of_experience')
if [ -n "$JAVA_YEARS" ] && [ "$JAVA_YEARS" != "null" ]; then
    test_passed "Skills contain years_of_experience: $JAVA_YEARS years"
else
    test_failed "Skills missing years_of_experience field"
fi

# Test 3: LLM Reasoning mentions years
echo ""
echo "Test 3: LLM reasoning mentions years of experience"
REASONING=$(echo "$RESPONSE" | jq -r '.candidates[0].llm_reasoning')
if echo "$REASONING" | grep -qiE "[0-9]+ year"; then
    test_passed "LLM reasoning mentions years: $(echo "$REASONING" | grep -oE '[0-9]+ year[s]?' | head -1)"
else
    test_warning "LLM reasoning doesn't explicitly mention years (may still be correct)"
fi

# Test 4: Experience-based ranking
echo ""
echo "Test 4: Candidates with more experience rank higher"
RESPONSE=$(curl -s "$BASE_URL/api/search/hybrid" \
  -H 'Content-Type: application/json' \
  -d '{"query": "Java bilen adayları getir"}')

CANDIDATE1=$(echo "$RESPONSE" | jq -r '.candidates[0].name')
CANDIDATE1_JAVA_YRS=$(echo "$RESPONSE" | jq -r '.candidates[0].skills[]? | select(.name=="Java") | .years_of_experience // 0')
CANDIDATE1_SCORE=$(echo "$RESPONSE" | jq -r '.candidates[0].llm_score')

CANDIDATE2=$(echo "$RESPONSE" | jq -r '.candidates[1].name')
CANDIDATE2_JAVA_YRS=$(echo "$RESPONSE" | jq -r '.candidates[1].skills[]? | select(.name=="Java") | .years_of_experience // 0')
CANDIDATE2_SCORE=$(echo "$RESPONSE" | jq -r '.candidates[1].llm_score')

echo "  - #1: $CANDIDATE1: $CANDIDATE1_JAVA_YRS yrs Java, Score: $CANDIDATE1_SCORE"
echo "  - #2: $CANDIDATE2: $CANDIDATE2_JAVA_YRS yrs Java, Score: $CANDIDATE2_SCORE"

# Verify specific case: Mehmet (13 yrs) should rank higher than Can (8 yrs)
if [[ "$CANDIDATE1" == *"MEHMET"* ]] && [[ "$CANDIDATE2" == *"Can"* ]]; then
    if [ "$CANDIDATE1_JAVA_YRS" -eq 13 ] && [ "$CANDIDATE2_JAVA_YRS" -eq 8 ]; then
        test_passed "✓ CRITICAL CASE: Mehmet (13 yrs) ranks above Can (8 yrs)"
    else
        test_failed "Mehmet/Can case exists but years incorrect"
    fi
elif [[ "$CANDIDATE2" == *"MEHMET"* ]] && [[ "$CANDIDATE1" == *"Can"* ]]; then
    test_failed "✗ CRITICAL CASE FAILED: Can (8 yrs) ranks above Mehmet (13 yrs)!"
fi

# Generic check: more experienced should rank higher
if [ "$CANDIDATE1_JAVA_YRS" -gt "$CANDIDATE2_JAVA_YRS" ]; then
    if (( $(echo "$CANDIDATE1_SCORE >= $CANDIDATE2_SCORE" | bc -l) )); then
        test_passed "More experienced candidate ($CANDIDATE1_JAVA_YRS yrs) ranks higher"
    else
        test_failed "Less experienced candidate ranked higher despite fewer years"
    fi
elif [ "$CANDIDATE2_JAVA_YRS" -gt "$CANDIDATE1_JAVA_YRS" ]; then
    if (( $(echo "$CANDIDATE2_SCORE >= $CANDIDATE1_SCORE" | bc -l) )); then
        test_passed "More experienced candidate ($CANDIDATE2_JAVA_YRS yrs) ranks higher"
    else
        test_failed "Less experienced candidate ranked higher despite fewer years"
    fi
else
    test_warning "Candidates have equal Java experience, cannot verify ranking"
fi

# Test 5: Explicit years requirement filtering
echo ""
echo "Test 5: Explicit years requirement (10+ years)"
RESPONSE=$(curl -s "$BASE_URL/api/search/hybrid" \
  -H 'Content-Type: application/json' \
  -d '{"query": "Java developer with 10 years"}')

TOP_CANDIDATE=$(echo "$RESPONSE" | jq -r '.candidates[0].name')
TOP_JAVA_YRS=$(echo "$RESPONSE" | jq -r '.candidates[0].skills[]? | select(.name=="Java") | .years_of_experience // 0')
TOP_SCORE=$(echo "$RESPONSE" | jq -r '.candidates[0].llm_score')
TOP_REASONING=$(echo "$RESPONSE" | jq -r '.candidates[0].llm_reasoning')

echo "  - Top: $TOP_CANDIDATE ($TOP_JAVA_YRS yrs Java, Score: $TOP_SCORE)"

if [ "$TOP_JAVA_YRS" -ge 10 ]; then
    test_passed "Top candidate meets 10+ years requirement ($TOP_JAVA_YRS years)"
else
    test_warning "Top candidate has <10 years ($TOP_JAVA_YRS yrs) - LLM may prioritize other factors"
fi

if echo "$TOP_REASONING" | grep -qiE "[0-9]+ year"; then
    test_passed "Reasoning mentions years: $(echo "$TOP_REASONING" | grep -oE '[0-9]+ year[s]?' | head -1)"
else
    test_failed "Reasoning doesn't mention years for explicit requirement"
fi

# Test 6: Response includes required fields
echo ""
echo "Test 6: Response structure validation"
REQUIRED_FIELDS=("query" "candidates" "total_found" "method")
for field in "${REQUIRED_FIELDS[@]}"; do
    HAS_FIELD=$(echo "$RESPONSE" | jq "has(\"$field\")")
    if [ "$HAS_FIELD" = "true" ]; then
        test_passed "Response has '$field' field"
    else
        test_failed "Response missing '$field' field"
    fi
done

# Test 7: Candidate object structure
echo ""
echo "Test 7: Candidate object validation"
CANDIDATE_FIELDS=("name" "skills" "llm_score" "llm_reasoning")
for field in "${CANDIDATE_FIELDS[@]}"; do
    HAS_FIELD=$(echo "$RESPONSE" | jq ".candidates[0] | has(\"$field\")")
    if [ "$HAS_FIELD" = "true" ]; then
        test_passed "Candidate has '$field' field"
    else
        test_failed "Candidate missing '$field' field"
    fi
done

# Test 8: Skill object structure
echo ""
echo "Test 8: Skill object validation"
SKILL=$(echo "$RESPONSE" | jq '.candidates[0].skills[0]')
SKILL_FIELDS=("name" "proficiency" "years_of_experience")
for field in "${SKILL_FIELDS[@]}"; do
    HAS_FIELD=$(echo "$SKILL" | jq "has(\"$field\")")
    if [ "$HAS_FIELD" = "true" ]; then
        test_passed "Skill has '$field' field"
    else
        test_failed "Skill missing '$field' field"
    fi
done

# Summary
echo ""
echo "=================================="
if [ $FAILED -eq 0 ]; then
    echo -e "${GREEN}✓ ALL TESTS PASSED${NC}"
    exit 0
else
    echo -e "${RED}✗ $FAILED TEST(S) FAILED${NC}"
    exit 1
fi
