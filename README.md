# OpenCode-style Agent (Go)

This repository has been rewritten to match:

- `opencode/opencode.md`
- `opencode/opencode.png`

The previous gateway/orchestrator/RAG/OCR stack was removed.

## Architecture (implemented)

- CLI-first coding agent
- Two primary agents: `build`, `plan`
- Turn loop: `LLM -> tool -> LLM ...` until final answer or request timeout
- Permission engine: `allow / ask / deny` with wildcard and exact-match priority
- Tool registry: `list`, `glob`, `grep`, `read`, `edit`, `patch`, `bash`, `webfetch`, `websearch`
- Session persistence (BoltDB): session + turns + audit + approvals
- Compaction trigger: turn-count or token-estimate based rolling summary + pinned facts
- Workspace signals in context: root/top entries/git branch hint
- Optional local embedding retrieval (NIM on `localhost:8001`) for TopK code context injection
- Optional HTTP server:
  - `POST /sessions`
  - `POST /sessions/{id}/turns`
  - `POST /sessions/{id}/approvals/{approvalId}`
  - `GET /sessions/{id}`

## Project layout

```text
cmd/agent/
internal/app/
internal/agent/
internal/config/
internal/session/
internal/message/
internal/context/
internal/permission/
internal/tool/
internal/llm/
internal/storage/
internal/server/
```

## Quick start

1) Prepare env

```powershell
Copy-Item .env.example .env -Force
```

Set `NVIDIA_API_KEY` in `.env`.

Optional reliability knobs in `.env`:

- `LLM_MAX_RETRIES=2`
- `COMPACTION_TOKEN_THRESHOLD=12000`
- `LLM_STREAM=true`
- `ENABLE_THINKING=true`
- `CLEAR_THINKING=false`
- `TURN_TIMEOUT=90m`
- `ONESHOT_TIMEOUT=60m`
- `EMBEDDING_ENABLED=true`
- `EMBEDDING_BASE_URL=http://localhost:8001`
- `EMBEDDING_MODEL=nvidia/llama-3.2-nv-embedqa-1b-v2`

2) Start CLI

```powershell
powershell -ExecutionPolicy Bypass -File .\start-agent.ps1
```

3) Start HTTP mode (optional)

```powershell
powershell -ExecutionPolicy Bypass -File .\start.ps1 -Http
```

## CLI commands

- `/help`
- `/exit`
- `/new`
- `/sessions`
- `/use <sessionId>`
- `/agent <name>`

Per-turn agent override:

- `@build ...`
- `@plan ...`

## Test

```powershell
go test ./...
```
