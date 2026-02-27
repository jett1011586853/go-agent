# OpenCode-style Agent Architecture

```mermaid
flowchart LR
  U[User] --> I[TUI / CLI / HTTP]
  I --> APP[Core App Service]
  APP --> AM[Agent Manager]
  APP --> SM[Session Manager]
  APP --> PE[Permission Engine]
  APP --> LLM[LLM Provider]
  APP --> TOOLS[Tool Registry]
  TOOLS --> T1[list/glob/grep/read]
  SM --> DB[(BoltDB)]
```

## Logic flow

1. load config + AGENTS.md rules
2. select agent (`build` / `plan` / `@agent`)
3. assemble context (turns + summary + pinned facts + rules)
4. call LLM with tool schema contract
5. permission check on each tool call (`allow/ask/deny`)
6. write tool result back into messages
7. repeat until final or request timeout/cancel
8. persist turn/audit and trigger compaction when needed
