# HookRelay

基于 Go、MySQL、Redis 和 Asynq 的可靠 Webhook 投递平台学习项目。

目标能力：幂等事件接入、Outbox 可靠入队、异步 HTTP 投递、有限重试、投递历史、失败重放及接收方签名与幂等示例。

> 当前已完成接收端创建、查询、启用/禁用接口（含鉴权、受控 URL 与一次性签名密钥交付）、MySQL 读写、健康检查和优雅退出；事件、队列和可靠投递能力仍待开发，不应作为生产服务使用。

## 学习进度（下次先看这里）

详见 [学习记录与下次起点](docs/learning-progress.md)。

用户已验证真实 HTTP + MySQL 创建→查询闭环。已添加接收端启用/禁用接口和相关测试；新的 PATCH 数据库闭环尚待验证，范围见学习记录。

用户希望由助手写代码、自己理解逻辑；接下来讲解 PATCH 实现并在本地运行集成测试。

当前业务入口是 `cmd/api`；`cmd/healthcheck` 保留为练习程序。API 通过环境变量 `API_KEY` 配置管理接口密钥，不提供默认值。

## 第一天：启动与验证

[第一天操作指南](docs/day-01.md) 保留初始阶段的操作说明。当前启动需要额外配置 `API_KEY` 与 `ALLOWED_ENDPOINT_URL`，请优先按 [学习记录中的恢复环境步骤](docs/learning-progress.md#下次恢复环境) 运行。不要同时启动 `cmd/api` 与 `cmd/healthcheck`。

默认 API 地址：`127.0.0.1:8080`；项目 MySQL 本地端口：`13306`。

- `GET /healthz`：进程存活。
- `GET /readyz`：数据库可用。两个健康接口均无需鉴权。
- `POST /v1/endpoints`：需 `X-API-Key`，JSON 请求体只接收 `url`，且必须与 `ALLOWED_ENDPOINT_URL` 完全相同；成功返回 201，签名密钥仅在该响应中交付一次。设置 `Content-Type: application/json`。
- `GET /v1/endpoints/:id`：查询接收端，需 `X-API-Key` 请求头；不返回签名密钥，响应 ID 为字符串。
- `PATCH /v1/endpoints/:id`：需 `X-API-Key`，JSON 请求体仅 `{"enabled":true}` 或 `{"enabled":false}`；返回更新后的接收端公开字段，不返回密钥。当前尚无投递功能，禁用仅修改配置状态。
- `go test ./...`：运行不依赖数据库的测试，集成测试默认跳过。
- `HOOKRELAY_INTEGRATION=1 go test -count=1 -v ./internal/store ./cmd/api`：导出数据库配置、确认 endpoints 迁移已应用后，运行真实连接及接收端读写测试，以及真实本地 HTTP 创建→查询→启用/禁用闭环。业务测试在事务内回滚。

用户已确认 Docker 工具及引擎可用，并完成 Compose 启动 MySQL、数据库停止与恢复的手动验证。`docs/day-01.md` 保留的是初始实现时的操作指南与验证记录，最新学习状态以学习进度文档为准。

## 项目计划

详见 [总体计划与每周验收标准](PLAN.md)，包含：

- 项目范围、架构与可靠性边界。
- 数据模型、API 和 Webhook 协议。
- 6～8 周实施步骤及 4 周缩减方案。
- 故障测试、压测与最终交付清单。

## 下一步

1. 讲解 PATCH 更新代码，并在本地验证真实 HTTP + MySQL 启用/禁用闭环。
2. 设计 events、deliveries 数据模型与迁移，实现事件持久化与幂等事务。
3. 再进入异步投递、Outbox 和故障恢复。
