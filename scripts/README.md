# Test Scripts

## Integration Tests

### Local Testing
```bash
./scripts/integration_test.sh
```

Tests against local database (http://localhost:8080).

### Production Testing
```bash
BASE_URL=https://cv-search-production.up.railway.app ./scripts/integration_test.sh
```

Tests against production database.

### Critical Test Cases

**Test 4: Experience-based Ranking**
- Validates that candidates with more years of experience rank higher
- **Critical case**: When testing with production DB, verifies:
  - Query: "Java bilen adayları getir"
  - Expected: MEHMET ÖZ (13 yrs Java) ranks above Can Özçelik (8 yrs Java)
  - This was the original bug that led to implementing skill years feature

**Test 5: Explicit Years Requirement**
- Query: "Java developer with 10 years"
- Validates LLM understands and applies years requirement
- Top candidate should have 10+ years Java experience

### Test Requirements

All tests must pass before deploying to production:
```bash
./scripts/integration_test.sh && git push
```

### Test Coverage

1. API health check
2. Skills include `years_of_experience` field
3. LLM reasoning mentions years
4. Experience-based ranking (more years = higher rank)
5. Explicit years requirement filtering
6. Response structure validation
7. Candidate object validation
8. Skill object validation

### Database Requirements

Tests expect candidates with Java skills and varying years of experience:
- Local DB: Emre (10 yrs), Eser (8 yrs)
- Production DB: Mehmet (13 yrs), Can (8 yrs)
