# go-agent

一个面向工程落地的 Go 代码智能体：确定性工作由本地结构化工具执行，远端 LLM 主要负责规划与生成。

## 项目定位

- 这是可执行的工程代理，不只是聊天界面
- 默认使用结构化文件写入（`mkdir`、`write_file`、`edit`、`patch`、`apply_diff`）
- 每轮都可审计：规划决策、工具调用、权限判断、验证结果

## 核心能力

- 两阶段推理：
  - `planner` 负责下一步动作（`tool_call`、`final`、`ask_clarification`、`no_tool`）
  - `writer` 仅在需要长输出时进行流式生成
- 阶段治理（phase mode）：
  - 按阶段推进复杂任务
  - 每阶段文件改动数量限制（`phase_max_files`）
  - 写入后自动执行 `verify`
  - 可将阶段总结追加到 `docs/decision-log.md`
- 更安全的工具运行时：
  - `bash` 禁止写文件/删除/重定向类操作
  - 工作区内绝对路径会自动归一化
  - 工具失败时也会输出诊断信息，便于排障
- 检索增强（可选）：
  - 本地 Embedding NIM：`localhost:8001`
  - 本地 Rerank NIM：`localhost:8002`
  - 索引支持 hook + 文件监听器增量更新
- Skills 系统（可选）：
  - 支持多来源 skills（`superpowers`、`anthropic`、`gsd`）
  - 自动选择并注入相关技能上下文
- 会话与韧性：
  - BoltDB 持久化 `session`、`turn`、`approval`、`audit`
  - 上下文压缩与摘要
  - 超时后在同一会话自动续跑

## 长复杂任务生成样例（projects/trade-lab）

以下样例来自本仓库中的真实生成项目：`projects/trade-lab`。  
目标任务是从零构建一个“事件驱动量化回测与实时风控平台（Go 单体 + 前端 + SQLite/Postgres 可切换）”，并按多阶段推进。

### 任务产物（节选）

- 服务入口与运行：
  - `projects/trade-lab/cmd/server/main.go`
- 核心领域模块：
  - `projects/trade-lab/internal/eventbus/bus.go`
  - `projects/trade-lab/internal/marketdata/replayer.go`
  - `projects/trade-lab/internal/backtest/engine.go`
  - `projects/trade-lab/internal/order/matcher.go`
  - `projects/trade-lab/internal/risk/risk.go`
  - `projects/trade-lab/internal/fault/injector.go`
- API 与可视化：
  - `projects/trade-lab/internal/api/handlers.go`
  - `projects/trade-lab/web/index.html`
- 工程化交付：
  - `projects/trade-lab/Makefile`
  - `projects/trade-lab/Dockerfile`
  - `projects/trade-lab/docker-compose.yml`
  - `projects/trade-lab/docs/decision-log.md`
  - `projects/trade-lab/docs/acceptance.md`
  - `projects/trade-lab/docs/risk-register.md`

### 自动验证结果（本地实测）

在 `projects/trade-lab` 目录执行：

```powershell
go test ./...
```

结果：

- `trade-lab/tests` 通过
- 退出码 `0`
- 说明样例项目当前可编译、可测试、可继续迭代

这个样例体现了 agent 在长任务中的能力：

- 分阶段推进而不是一次性大改
- 写入后自动验证并基于错误回合修复
- 通过结构化工具控制改动范围与可追溯性

## 内置工具

- 只读：`list`、`glob`、`grep`、`read`
- 写入：`mkdir`、`write_file`、`edit`、`patch`、`apply_diff`
- 执行/验证：`bash`、`verify`
- 其他：`webfetch`、`websearch`、`skill`

## 目录结构

```text
cmd/agent/              # CLI 入口
internal/app/           # 主循环、阶段治理、恢复逻辑
internal/tool/          # 工具注册与实现
internal/retrieval/     # embedding 检索与 rerank
internal/indexer/       # 索引增量更新与 watcher
internal/skills/        # skills 加载与选择
internal/llm/           # 模型提供方封装
internal/config/        # yaml/env 配置加载
internal/session/       # 会话管理
internal/storage/       # BoltDB 存储
internal/server/        # 可选 HTTP 模式
opencode/agent.yaml     # 默认配置
projects/               # 长任务生成样例目录
```

## 快速开始

1. 准备环境变量文件：

```powershell
Copy-Item .env.example .env -Force
```

2. 启动 CLI：

```powershell
powershell -ExecutionPolicy Bypass -File .\start-agent.ps1
```

3. 可选：安装快捷命令：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\install-shortcut.ps1
```

重启 PowerShell 后可直接使用：

```powershell
goagent
# 或
gga
```

如果需要支持 `go go agent`：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\install-shortcut.ps1 -EnableGoShim
```

卸载快捷命令：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\uninstall-shortcut.ps1
```

## 以任意文件夹作为工作区启动

方式 A（推荐）：临时环境变量覆盖

```powershell
$env:WORKSPACE_ROOT = "D:\your-project"
powershell -ExecutionPolicy Bypass -File .\start-agent.ps1
```

方式 B：使用单独配置文件

```yaml
# opencode/agent.custom.yaml
workspace_root: D:/your-project
```

```powershell
powershell -ExecutionPolicy Bypass -File .\start-agent.ps1 -Config .\opencode\agent.custom.yaml
```

## 本地 NIM 自动启动

`start.ps1` 可自动拉起本地容器：

- embedding：`embed-nim`（`localhost:8001`）
- rerank：`rerank-nim`（`localhost:8002`）

自动启动条件：

- `embedding_enabled` / `rerank_enabled` 为 `true`
- 对应 base URL 是 localhost

可覆盖镜像与容器参数：

- `EMBED_NIM_IMAGE`、`EMBED_NIM_CONTAINER`、`EMBED_NIM_CACHE`、`EMBED_NIM_PORT`
- `RERANK_NIM_IMAGE`、`RERANK_NIM_CONTAINER`、`RERANK_NIM_CACHE`、`RERANK_NIM_PORT`

## CLI 命令

- `/help`
- `/exit`
- `/new`
- `/sessions`
- `/use <sessionId>`
- `/agent <name>`

单轮覆盖 agent：

- `@build ...`
- `@plan ...`

单次任务模式（one-shot）：

```powershell
powershell -ExecutionPolicy Bypass -File .\start.ps1 -Task "重构 internal/tool"
```

## HTTP 模式（可选）

```powershell
$env:AGENT_HTTP_ENABLE = "true"
powershell -ExecutionPolicy Bypass -File .\start.ps1
```

监听地址由 `http_addr` / `AGENT_HTTP_ADDR` 控制（默认 `:8090`）。

## 运行测试

```powershell
go test ./...
```
