# HookRelay

基于 Go、MySQL、Redis 和 Asynq 的可靠 Webhook 投递平台学习项目。

目标能力：幂等事件接入、Outbox 可靠入队、异步 HTTP 投递、有限重试、投递历史、失败重放及接收方签名与幂等示例。

> 已打通事件落库 → Redis/Asynq → Worker → HTTP 接收端 → 投递状态更新，支持有限重试。**尚无 Outbox、签名、接收方幂等和自动对账，不是可靠交付完成版，仅供本地受控演示。**

## 学习进度（下次先看这里）

详见 [学习记录与下次起点](docs/learning-progress.md)。

用户已应用 events/deliveries 迁移。助手使用隔离的临时 MySQL 8.0.42 + Redis，通过了事件事务/并发幂等、Worker 状态更新及真实队列成功/临时失败重试/永久失败/耗尽预算测试，并验证了三个独立进程的 API→Worker→接收端链路。没有读取用户 `.env` 或操作已有服务；Compose Redis 启动路径尚未在该验证中执行。

协作方式：助手写代码、用户理解逻辑；优先核心流程，避免过多防御性代码。

当前业务入口是 `cmd/api`；`cmd/healthcheck` 保留为练习程序。API 通过环境变量 `API_KEY` 配置管理接口密钥，不提供默认值。

## 第一天：启动与验证

[第一天操作指南](docs/day-01.md) 保留初始阶段的操作说明。当前启动需要额外配置 `API_KEY` 与 `ALLOWED_ENDPOINT_URL`，请优先按 [学习记录中的恢复环境步骤](docs/learning-progress.md#下次恢复环境) 运行。不要同时启动 `cmd/api` 与 `cmd/healthcheck`。

默认 API 地址：`127.0.0.1:8080`；MySQL 本地端口：`13306`；Redis：`127.0.0.1:16379`。

## 运行异步演示

```bash
docker compose up -d --wait mysql redis
set -a; . ./.env; set +a
export REDIS_ADDR="${REDIS_ADDR:-127.0.0.1:16379}"
export ALLOWED_ENDPOINT_URL="${ALLOWED_ENDPOINT_URL:-http://127.0.0.1:9090/webhook}"
# API_KEY 需自行配置；已有的 000001/000002 迁移勿重复执行。
```

在三个终端分别运行（API/Worker 终端均需加载上述环境）：

```bash
go run ./cmd/receiver
go run ./cmd/worker
go run ./cmd/api
```

创建接收端后，按下方示例提交事件并查询投递。模拟故障时，停止并重启**模拟接收端**：`go run ./cmd/receiver -status 500` 或 `go run ./cmd/receiver -delay 8s`，再提交新事件。默认使用 Asynq 退避，重试不是立即发生。只运行一个 Worker；接收端没有持久化去重和验签。

## 接口与测试

- `GET /healthz`：进程存活。
- `GET /readyz`：数据库可用。两个健康接口均无需鉴权。
- `POST /v1/endpoints`：需 `X-API-Key`，JSON 请求体只接收 `url`，且必须与 `ALLOWED_ENDPOINT_URL` 完全相同；成功返回 201，签名密钥仅在该响应中交付一次。设置 `Content-Type: application/json`。
- `GET /v1/endpoints/:id`：查询接收端，需 `X-API-Key` 请求头；不返回签名密钥，响应 ID 为字符串。
- `PATCH /v1/endpoints/:id`：需 `X-API-Key`，JSON 请求体仅 `{"enabled":true}` 或 `{"enabled":false}`；返回更新后的接收端公开字段，不返回密钥。禁用拒绝新事件；Worker 在领取时发现禁用会将已有任务置为 failed；已发出的请求不能撤回。
- `POST /v1/events`：需 `X-API-Key`、`Idempotency-Key` 和 JSON 请求体；首次返回 202，同键同内容返回 200 和原 ID，同键不同内容返回 409。HTTP 投递由独立 Worker 异步执行。
- `GET /v1/deliveries/:id`：需 `X-API-Key`，返回投递 ID、事件 ID、状态、执行版本、尝试次数、最后 HTTP 状态/错误分类及创建/更新时间；不存在返回 404。
- `go test ./...`：运行不依赖数据库的测试（包括 Sender 的本地 HTTP 测试），集成测试默认跳过。
- `HOOKRELAY_INTEGRATION=1 go test -count=1 -v ./internal/store ./cmd/api ./tests/integration`：需 MySQL、Redis 及两份已应用迁移；覆盖事务、并发幂等、领取/条件回写、真实队列重试等。端点接口测试回滚，跨事务测试只清理自身记录；异步测试使用独立随机队列，结束时删除自己的队列。

用户已确认 Docker 工具及引擎可用，并完成 Compose 启动 MySQL、数据库停止与恢复的手动验证。`docs/day-01.md` 保留的是初始实现时的操作指南与验证记录，最新学习状态以学习进度文档为准。

## 项目计划

详见 [总体计划与每周验收标准](PLAN.md)，包含：

- 项目范围、架构与可靠性边界。
- 数据模型、API 和 Webhook 协议。
- 6～8 周实施步骤及 4 周缩减方案。
- 故障测试、压测与最终交付清单。

## 提交事件

```bash
curl -i http://127.0.0.1:8080/v1/events \
  -H "X-API-Key: $API_KEY" \
  -H 'Idempotency-Key: order-42' \
  -H 'Content-Type: application/json' \
  -d '{"endpoint_id":"1","event_type":"order.created","payload":{"order_id":42}}'
```

将 `endpoint_id` 替换为已创建的接收端 ID（字符串）。成功响应：

```json
{"event_id":"1","delivery_id":"1","reused":false}
```

- 幂等键为 1～128 字节可打印 ASCII，不含空格；事件类型为 1～128 字节，payload 必填，为任意 JSON 值，最多 256 KiB。
- 摘要比较端点 ID、事件类型和 payload 原始字节；payload 内部空白变化也会冲突。不要在重试时重新格式化 payload。
- 新事件端点不存在返回 404，禁用返回 409；已接受事件的同内容重试不受后来禁用影响。
- 202 表示已持久化且本次入队成功（或任务已存在），不表示接收方已处理成功。
- Redis 入队失败返回 `503`、`error=event_saved_queue_unavailable`、`persisted=true` 及两个 ID。**此时数据已经保存**，使用原幂等键和原内容重试可补入队，不要换新键。

## 查询投递

```bash
curl -i http://127.0.0.1:8080/v1/deliveries/1 -H "X-API-Key: $API_KEY"
```

使用提交事件响应中的 `delivery_id`。状态：`pending → processing → succeeded`；临时失败进入 `retrying`，永久错误或耗尽预算进入 `failed`。`attempt_count` 在领取时加一：崩溃发生在 HTTP 前也会占用预算，因此不是精确的远端接收次数。

`internal/delivery/sender.go` 已实现单次 HTTP POST：原样发送 payload，携带稳定 `X-Webhook-ID`；2xx 成功，网络错误/超时/408/429/5xx 标记可重试，3xx 不跟随且视为永久失败，其余 4xx 不重试。Sender 使用调用方指定的 HTTP 超时，传递 context，最多读取 4 KiB 响应体以便连接复用。它不负责排队、自动重试或写数据库，也尚未实现 HMAC、Retry-After，仅供受控本地测试。

## Worker 与当前限制

- 任务类型 `webhook:deliver`，队列 `webhooks`，payload 仅含 delivery_id 与 generation。稳定任务 ID 抑制队列内重复；已终态或旧 generation 由数据库跳过。
- 当前默认：单进程、并发 1，最多 3 次领取，HTTP 超时 5 秒、任务超时 20 秒、执行租约 30 秒。参数集中于 `internal/task/delivery.go`；Asynq 负责退避，数据库负责状态与预算。
- 领取使用短事务；发送不持有事务；回写校验 generation 和 attempt_count。租约过期仍可能重复发送，不能保证外部业务恰好一次。
- **仍有落库后、入队前崩溃窗口**。重复提交只能人工补救，未接 Outbox 不承诺自动恢复。
- Redis 数据丢失、任务因数据库长期故障耗尽重试、最终结果无法写库，可能遗留非终态记录；没有 Reconciler。Worker 强杀恢复时间、多实例并发尚未做故障验证。
- 暂无投递历史、人工重放、HMAC、接收端幂等、Retry-After 和全局端点隔离。签名密钥目前仅保存，不参与发送。

## 下一步

实现 Outbox：将事件、投递和待入队记录放进同一事务，由 Dispatcher 持续发布，替换 API 中的直接入队。
