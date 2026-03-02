# OpenCode-style Agent (Go)

This repository has been rewritten to match:

- `opencode/opencode.md`
- `opencode/opencode.png`

The previous gateway/orchestrator/RAG/OCR stack was removed.

## Architecture (implemented)

- CLI-first coding agent
- Two primary agents: `build`, `plan`
- Turn loop: `LLM -> tool -> LLM ...` until final answer or request timeout
- CLI live stream output from provider SSE chunks (`[llm-stream] ...`) during each turn
- CLI event feed for retrieval/tool/meta stages (`[retrieval]`, `[tool]`, `[meta]`)
- Auto continuation on truncated model output (`finish_reason=length` or missing `*** END PATCH`)
- Adaptive `max_tokens` budgeting based on agent type and prompt size
- Two-stage LLM loop: non-stream planner action first, stream writer only for long final output
- Permission engine: `allow / ask / deny` with wildcard and exact-match priority
- Tool registry: `list`, `glob`, `grep`, `read`, `edit`, `patch`, `bash`, `webfetch`, `websearch`
- Session persistence (BoltDB): session + turns + audit + approvals
- Compaction trigger: turn-count or token-estimate based rolling summary + pinned facts
- Workspace signals in context: root/top entries/git branch hint
- Optional local embedding retrieval (NIM on `localhost:8001`) for TopK code context injection
- Optional local rerank stage (NIM on `localhost:8002`) to reorder retrieval candidates (`Top20 -> TopK`)
- Incremental embedding index updates via tool hook + filesystem watcher (debounced queue)
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
- `EMBEDDING_ENABLED=true` (default in `opencode/agent.yaml`)
- `EMBEDDING_BASE_URL=http://localhost:8001`
- `EMBEDDING_MODEL=nvidia/llama-3.2-nv-embedqa-1b-v2`
- `EMBEDDING_IGNORE_DIRS=.git,.run,node_modules,dist,build,vendor,bin`
- `RERANK_ENABLED=true`
- `RERANK_BASE_URL=http://localhost:8002`
- `RERANK_MODEL=nvidia/llama-3.2-nv-rerankqa-1b-v2`
- `RERANK_CANDIDATE_K=20`

2) Start CLI

```powershell
powershell -ExecutionPolicy Bypass -File .\start-agent.ps1
```

Optional: install short commands (one-time):

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\install-shortcut.ps1
```

Then restart PowerShell and use:

```powershell
goagent
# or
gga
```

If you really want `go go agent`, install with:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\install-shortcut.ps1 -EnableGoShim
```

To remove shortcuts:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\uninstall-shortcut.ps1
```

`start.ps1` will auto-start local NIM containers in detached mode when enabled:

- embedding (`embed-nim`, `localhost:8001`)
- rerank (`rerank-nim`, `localhost:8002`)

You can override images/container/cache/port via env vars:

- `EMBED_NIM_IMAGE`, `EMBED_NIM_CONTAINER`, `EMBED_NIM_CACHE`, `EMBED_NIM_PORT`
- `RERANK_NIM_IMAGE`, `RERANK_NIM_CONTAINER`, `RERANK_NIM_CACHE`, `RERANK_NIM_PORT`

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
