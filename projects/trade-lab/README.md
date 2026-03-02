# Trade-Lab

事件驱动量化回测与实时风控平台

## 项目结构

```
trade-lab/
├── cmd/                    # 命令行入口
│   └── server/             # HTTP服务
├── internal/               # 内部包
│   ├── config/             # 配置管理
│   ├── errors/             # 错误码定义
│   ├── health/             # 健康检查
│   └── logger/             # 日志系统
├── web/                    # 前端资源
├── tests/                  # 测试文件
├── docs/                   # 文档
├── config/                 # 配置文件
└── scripts/                # 脚本
```

## 快速开始

```bash
# 安装依赖
make deps

# 运行开发服务
make run

# 运行测试
make test
```

## 配置

支持环境变量和YAML配置文件:

- `TL_PROFILE`: 运行环境 (dev/test/prod)
- `TL_LOG_LEVEL`: 日志级别 (debug/info/warn/error)
- `TL_HTTP_ADDR`: HTTP地址 (默认 :8080)
- `TL_DB_DRIVER`: 数据库类型 (sqlite/postgres)
- `TL_DB_DSN`: 数据库连接串

## API

| 端点 | 描述 |
|------|------|
| GET /health | 健康检查 |
| GET /ready | 就绪检查 |
| GET /api/v1/ping | API测试 |

## 开发阶段

- [x] 阶段1: 基础骨架与配置中心
- [ ] 阶段2: 行情与事件总线
- [ ] 阶段3: 策略与回测引擎
- [ ] 阶段4: 订单路由与风控
- [ ] 阶段5: 可视化与运维面板
- [ ] 阶段6: 可靠性与发布
