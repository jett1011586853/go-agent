# OpenCode-style Agent (Go) 开发指南
_生成日期：2026-02-16_

> 目标：用 Go 实现一个可控、可扩展、可审计的 “OpenCode 风格” 编程 Agent：支持 build/plan 双主代理、工具系统（read/edit/bash…）、权限（allow/ask/deny）、会话存储与压缩（compaction）、规则文件（AGENTS.md）、以及可选的 HTTP Server/OpenAPI。

---

## 1. 设计原则
- **????**?LLM ?? ? ???? ? ???? ? ????????????/???
- **强约束可控**：所有外部副作用（写文件、bash、网络）必须走 **权限引擎**。
- **可扩展**：工具注册表 + 插件 hooks + 可插拔 LLM Provider。
- **可审计**：每个 turn、每次 tool call、每个批准/拒绝决策都写入存储。

---

## 2. MVP 范围（先跑通最小闭环）
必须完成：
1) 单会话对话与持久化（Session + Turns）  
2) 双主代理：`build` 与 `plan`（仅配置不同）  
3) 只读工具：`list/glob/grep/read`（先不做写）  
4) 权限系统：`allow/ask/deny` + wildcard  
5) Step loop?`LLM -> tool -> LLM ...`?????????

---

## 3. 推荐工程目录（Go）
```
go-agent/
  cmd/agent/                # CLI 入口
  internal/app/             # turn loop orchestrator
  internal/agent/           # agent 定义 + manager
  internal/config/          # config load + validate
  internal/session/         # session manager + compaction trigger
  internal/message/         # message/tool-call 结构体
  internal/context/         # rules + workspace context + compaction
  internal/permission/      # allow/ask/deny
  internal/tool/            # registry + builtin tools
  internal/llm/             # provider 抽象 & 实现
  internal/storage/         # sqlite/bolt + migrations
  internal/server/          # 可选：HTTP/OpenAPI
```

---

## 4. 核心数据结构（建议）
### 4.1 Message / ToolCall
- `Message`：role(user/assistant/tool/system), content, tool_calls(optional)
- `ToolCall`：id, name, args(JSON)
- `ToolResult`：id, name, output(text/json), error(optional)

### 4.2 Session
- `SessionID`
- `Turns[]`：每次用户输入对应一轮（含 assistant + tools）
- `WorkingSummary`：滚动摘要（用于压缩）
- `PinnedFacts`：重要事实/约束（长期保留）
- `CreatedAt/UpdatedAt`

### 4.3 Agent
- `Name`, `Mode`(primary/sub/system)
- `ModelRef`
- `Tools[]`
- `Permissions`（tool->allow/ask/deny, 支持 `"*"`）

---

## 5. 配置文件（YAML 示例）
```yaml
agents:
  build:
    mode: primary
    model: "provider:model"
    tools: ["read","list","glob","grep","edit","patch","bash","webfetch","websearch"]
    permissions: {"*":"allow"}

  plan:
    mode: primary
    model: "provider:model"
    tools: ["read","list","glob","grep","webfetch","websearch"]
    permissions:
      edit: "ask"
      bash: "ask"
      "*": "allow"
```

---

## 6. Turn Loop（执行循环）规范
每个 turn 的固定流程：
1) 读取 `config + rules(AGENTS.md)`  
2) 选择 agent（默认 build/plan；也可显式 @agent）  
3) 组装上下文（session turns + working summary + rules + workspace signals）  
4) 调用 LLM（带工具 schema）  
5) 若返回 tool call：进入权限引擎  
6) allow：执行工具；ask：向用户发起批准；deny：回注拒绝信息  
7) 将 tool result 写回消息队列，再次调用 LLM  
8) ???? `final` ???????/????  
9) 判断是否触发 compaction（turn 数或 token 估算阈值）  

---

## 7. 权限引擎（allow/ask/deny）
### 7.1 匹配优先级
1) 精确匹配：`edit`  
2) 通配符：`mcp:*` 或 `"*"`  
3) 默认策略：`ask`（推荐）

### 7.2 ask 的状态机
- CLI/TUI：显示 `tool name + args diff + 风险提示`，等待 y/n
- HTTP：返回 `approval_required`，客户端再调用 `/approvals/<built-in function id>`

---

## 8. 工具系统（Tool Registry）
### 8.1 Tool 接口（建议）
- `Name() string`
- `Schema() []byte`（JSON Schema 或 OpenAI function schema）
- `Run(ctx context.Context, args json.RawMessage) (ToolResult, error)`

### 8.2 MVP 工具建议
- `list`: 列目录
- `glob`: 文件通配
- `grep`: 关键字搜索
- `read`: 读取文件（大小限制、二进制检测）

安全建议：
- 所有文件路径必须落在 workspace root 内（防止路径穿越）
- 输出截断（避免把大文件塞爆上下文）

---

## 9. Compaction（压缩）与系统 Agent
触发条件（任选其一）：
- turns > N（如 30）
- token 估算 > 阈值

实现要点：
- 用一个 system agent：`compaction`
- 输出两段：`WorkingSummary`（可持续滚动）+ `PinnedFacts`（长期保留）
- 旧 turns 归档，但保留索引与可回放能力

---

## 10. 可选：HTTP Server/OpenAPI
最小接口建议：
- `POST /sessions`
- `POST /sessions/{id}/turns`
- `POST /sessions/{id}/approvals/{approvalId}`（approve/reject）
- `GET /sessions/{id}`（回放）

---

## 11. 测试清单（建议）
- Tool 路径安全（..、绝对路径、符号链接）
- 权限匹配优先级（exact > wildcard）
- ask/deny 分支（批准与拒绝都可回注继续推理）
- turn loop 最大步数停止
- 存储一致性（崩溃恢复后 turn 不丢）
- compaction 后仍能继续对话且上下文正确

---

## 12. 里程碑（推荐顺序）
1) Config + AgentManager  
2) ToolRegistry + 只读工具  
3) PermissionEngine  
4) LLMProvider（工具调用）  
5) SessionStore（sqlite/bolt）  
6) Compaction  
7) 写工具（edit/patch）与 bash  
8) TUI 或 HTTP Server  
9) Plugins/MCP

---

_你可以把这份指南当作 DoD（Definition of Done）来推进：每个模块做完就能端到端验证。_
