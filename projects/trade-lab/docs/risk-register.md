# Risk Register

## High Priority Risks

### R001: Single Point of Failure - Event Bus
- **Category**: Architecture
- **Impact**: High
- **Likelihood**: Medium
- **Description**: In-memory event bus loses all events on restart
- **Mitigation**: Implement persistent event store (WAL)
- **Status**: Open

### R002: No Data Persistence
- **Category**: Data
- **Impact**: High
- **Likelihood**: High
- **Description**: SQLite/Postgres integration not fully implemented
- **Mitigation**: Phase 7 - Database integration
- **Status**: Open

### R003: No Authentication/Authorization
- **Category**: Security
- **Impact**: High
- **Likelihood**: High
- **Description**: API endpoints have no auth protection
- **Mitigation**: Implement JWT/API key auth
- **Status**: Open

## Medium Priority Risks

### R004: Missing Rate Limiting
- **Category**: Security
- **Impact**: Medium
- **Likelihood**: Medium
- **Description**: No rate limiting on API endpoints
- **Mitigation**: Add rate limiting middleware
- **Status**: Open

### R005: No TLS/HTTPS
- **Category**: Security
- **Impact**: Medium
- **Likelihood**: High
- **Description**: HTTP only, no TLS support
- **Mitigation**: Add TLS configuration
- **Status**: Open

### R006: Memory Leak in Long-running Processes
- **Category**: Reliability
- **Impact**: Medium
- **Likelihood**: Medium
- **Description**: Potential memory leaks in event bus history
- **Mitigation**: Implement bounded buffers with eviction
- **Status**: Mitigated (maxHistory limit)

### R007: No Graceful Degradation
- **Category**: Reliability
- **Impact**: Medium
- **Likelihood**: Low
- **Description**: System fails hard on errors
- **Mitigation**: Implement circuit breaker pattern
- **Status**: Partial (fault injector ready)

## Low Priority Risks

### R008: No Monitoring/Metrics Export
- **Category**: Operations
- **Impact**: Low
- **Likelihood**: High
- **Description**: No Prometheus metrics endpoint
- **Mitigation**: Add metrics middleware
- **Status**: Open

### R009: No Log Rotation
- **Category**: Operations
- **Impact**: Low
- **Likelihood**: Medium
- **Description**: Log files grow unbounded
- **Mitigation**: Add log rotation (lumberjack)
- **Status**: Open

### R010: No Backup Strategy
- **Category**: Operations
- **Impact**: Low
- **Likelihood**: Low
- **Description**: No automated backups
- **Mitigation**: Implement backup cron job
- **Status**: Open

## Risk Summary

| Priority | Count | Status |
|----------|-------|--------|
| High | 3 | Open |
| Medium | 4 | 1 Mitigated, 1 Partial |
| Low | 3 | Open |
