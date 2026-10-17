# HookRelay

基于 Go、MySQL、Redis 和 Asynq 的可靠 Webhook 投递平台学习项目。

目标能力：幂等事件接入、Outbox 可靠入队、异步 HTTP 投递、有限重试、投递历史、失败重放及接收方签名与幂等示例。

> 当前已完成第一天的 API 基础骨架：配置、MySQL 连接、健康检查和测试。上述业务能力尚未实现，不应作为生产服务使用。

## 第一天：启动与验证

按 [第一天操作指南](docs/day-01.md) 启动 MySQL、运行 API，并验证数据库故障时两个健康接口的区别。

默认 API 地址：`127.0.0.1:8080`；项目 MySQL 本地端口：`13306`。

- `GET /healthz`：进程存活。
- `GET /readyz`：数据库可用。
- `go test ./...`：运行不依赖数据库的测试，集成测试默认跳过。
- `HOOKRELAY_INTEGRATION=1 go test -v ./internal/store`：导出数据库配置后运行真实连接测试。

当前环境的 Docker Desktop WSL 集成尚未启用，Compose 启动路径待验证；独立临时 MySQL 实例上的连接与健康检查已验证。

## 项目计划

详见 [总体计划与每周验收标准](PLAN.md)，包含：

- 项目范围、架构与可靠性边界。
- 数据模型、API 和 Webhook 协议。
- 6～8 周实施步骤及 4 周缩减方案。
- 故障测试、压测与最终交付清单。

## 下一步

1. 启用 Docker Desktop WSL 集成，按第一天指南验证 Compose 启动流程。
2. 创建 endpoints、events、deliveries 数据库迁移。
3. 实现端点管理、事件持久化与幂等接入。
