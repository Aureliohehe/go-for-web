# HookRelay

基于 Go、MySQL、Redis 和 Asynq 的可靠 Webhook 投递平台学习项目。

目标能力：幂等事件接入、Outbox 可靠入队、异步 HTTP 投递、有限重试、投递历史、失败重放及接收方签名与幂等示例。

> 已打通事件/投递/Outbox 同事务 → Dispatcher → Redis/Asynq → Worker → HTTP 投递，支持入队恢复和有限投递重试。已加入未完成投递容量限制、投递尝试历史、分页查询和失败重放；**尚无签名、接收方幂等和自动对账，仅供本地受控演示。**

## 学习进度（下次先看这里）

详见 [学习记录与下次起点](docs/learning-progress.md)。

用户已完成 `000003`（Outbox）和 `000004`（接入锁），助手已只读确认，**不要重复导入**。`000005_create_delivery_attempts.up.sql` 已由助手按用户授权导入 `hookrelay`，并验证表结构、索引、外键及导入时历史记录为 0；五份迁移均已完成，不要重复执行。本轮失败重放不需要新迁移。隔离 MySQL 8.0.42/Redis 7.4 下已验证并发重放、Outbox 写入失败回滚、重放与新事件共享容量、旧 generation 隔离及真实 HTTP 重放后成功投递；历史保留且投递 ID 不变。

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
export MAX_PENDING_DELIVERIES="${MAX_PENDING_DELIVERIES:-10000}"
export ALLOWED_ENDPOINT_URL="${ALLOWED_ENDPOINT_URL:-http://127.0.0.1:9090/webhook}"
# API_KEY 需自行配置；已有的 000001..000005 迁移勿重复执行。
```

当前环境五份迁移均已完成，**本轮无需导表，只需更新并重启 API/Worker**。全新数据库才需要按顺序执行五份迁移；不要重复执行已有迁移。`000005` 不补造升级前的历史，所以历史条数可能小于过去的 attempt_count。

在四个终端分别运行（API/Worker/Dispatcher 终端均需加载上述环境）：

```bash
go run ./cmd/receiver
go run ./cmd/worker
go run ./cmd/dispatcher
go run ./cmd/api
```

创建接收端后，按下方示例提交事件并查询投递。模拟故障时，停止并重启**模拟接收端**：`go run ./cmd/receiver -status 500` 或 `go run ./cmd/receiver -delay 8s`，再提交新事件。默认使用 Asynq 退避，重试不是立即发生。只运行一个 Worker 和一个 Dispatcher；接收端没有持久化去重和验签。使用 Compose 的 Redis 7.4，宿主旧 Redis 5 不兼容 Asynq 的归档操作。

## 接口与测试

- `GET /healthz`：进程存活。
- `GET /readyz`：数据库可用。两个健康接口均无需鉴权。
- `POST /v1/endpoints`：需 `X-API-Key`，JSON 请求体只接收 `url`，且必须与 `ALLOWED_ENDPOINT_URL` 完全相同；成功返回 201，签名密钥仅在该响应中交付一次。设置 `Content-Type: application/json`。
- `GET /v1/endpoints/:id`：查询接收端，需 `X-API-Key` 请求头；不返回签名密钥，响应 ID 为字符串。
- `PATCH /v1/endpoints/:id`：需 `X-API-Key`，JSON 请求体仅 `{"enabled":true}` 或 `{"enabled":false}`；返回更新后的接收端公开字段，不返回密钥。禁用拒绝新事件；Worker 在领取时发现禁用会将已有任务置为 failed；已发出的请求不能撤回。
- `POST /v1/events`：需 `X-API-Key`、`Idempotency-Key` 和 JSON 请求体；首次返回 202，同键同内容返回 200 和原 ID，同键不同内容返回 409。HTTP 投递由独立 Worker 异步执行。
- `GET /v1/deliveries/:id`：需 `X-API-Key`，返回投递 ID、事件 ID、状态、执行版本、尝试次数、最后 HTTP 状态/错误分类及创建/更新时间；不存在返回 404。
- `GET /v1/deliveries/:id/attempts?limit=20&after=0`：需 `X-API-Key`，按尝试 ID 升序分页查询历史。
- `POST /v1/deliveries/:id/replay`：需 `X-API-Key`，仅允许 failed 状态，成功返回 202 和新 generation；不需要请求体。
- `go test ./...`：运行不依赖数据库的测试（包括 Sender 的本地 HTTP 测试），集成测试默认跳过。
- `HOOKRELAY_INTEGRATION=1 go test -count=1 -v ./internal/store ./cmd/api`：需 MySQL 和五份已应用迁移，覆盖事务、并发幂等、历史原子写入、游标查询、并发重放与旧轮次回写；测试回滚或只清理自身记录。
- `HOOKRELAY_ADMISSION_INTEGRATION=1 go test -count=1 -race -run TestAdmissionIntegration ./internal/store`：仅对已迁移的独立空测试库运行，无其他业务进程；验证最后一个名额竞争、满载幂等、终态释放、事务回滚，以及新事件与重放争抢同一容量。不要清空业务库来满足测试条件。
- `HOOKRELAY_OUTBOX_INTEGRATION=1 go test -count=1 -race -v ./tests/integration`：**仅对已迁移的独立测试数据库运行，Outbox 必须为空，不能有 API/Dispatcher/Worker 同时运行**。需 Redis 7.4；测试使用独立随机队列。覆盖成功/重试/归档、Redis 连接失败后恢复、领取后失联、入队后确认失败、旧执行令牌、真实 HTTP 历史分页及失败重放后成功投递。不要指向日常开发数据库，也不要为了通过检查清空业务表。

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
- 202 表示事件、投递和 Outbox 已在同一事务中提交；不表示已入队或已投递。
- API 不再访问 Redis。Redis 不可用且积压未满时仍返回 202，Dispatcher 会后台重试，无需客户端重新提交。旧版 `event_saved_queue_unavailable` 503 响应已移除。

## 积压上限与背压

`MAX_PENDING_DELIVERIES` 默认 `10000`，必须是正整数，所有 API 实例配置一致。统计 `pending + processing + retrying` 投递；任务进入 Redis 不释放名额，只有 `succeeded` 或 `failed` 才释放。

达到上限时，新事件返回 `503`、`Retry-After: 5` 和：

```json
{"error":"backlog_full","persisted":false}
```

这个响应明确表示本次请求没有创建事件、投递或 Outbox，可以保留同一幂等键稍后重试。5 秒只是重试建议，不保证届时有容量。已接受事件的相同内容重试仍返回 200；同键不同内容仍为 409。

实现为数据库单行锁串行化“检查容量 + 三表写入”，事务采用 READ COMMITTED，锁后再次检查幂等。没有独立计数器，因此无需 Worker 维护增减计数；代价是新事件接入串行化、每次查询非终态数量，后续压测再优化。配置调低到现有积压以下不会删除已有任务，只暂停接收新事件。失败重放也经过同一容量检查，与新事件共享上限。

## 查询投递

```bash
curl -i http://127.0.0.1:8080/v1/deliveries/1 -H "X-API-Key: $API_KEY"
```

使用提交事件响应中的 `delivery_id`。状态：`pending → processing → succeeded`；临时失败进入 `retrying`，永久错误或耗尽预算进入 `failed`。`attempt_count` 在领取时加一：崩溃发生在 HTTP 前也会占用预算，因此不是精确的远端接收次数。

`internal/delivery/sender.go` 已实现单次 HTTP POST：原样发送 payload，携带稳定 `X-Webhook-ID`；2xx 成功，网络错误/超时/408/429/5xx 标记可重试，3xx 不跟随且视为永久失败，其余 4xx 不重试。Sender 使用调用方指定的 HTTP 超时，传递 context，最多读取 4 KiB 响应体以便连接复用。它不负责排队、自动重试或写数据库，也尚未实现 HMAC、Retry-After，仅供受控本地测试。

## 查询投递尝试历史

```bash
curl -s 'http://127.0.0.1:8080/v1/deliveries/1/attempts?limit=20' \
  -H "X-API-Key: $API_KEY"
```

返回 `{"items":[...],"next_after":""}`。每条包含字符串 id/delivery_id、generation、attempt_number、status、started_at、finished_at、http_status、error_class、latency_ms。`next_after` 非空时将它作为下一页 `after`；默认每页 20，范围 1～100，跨 generation 按 ID 升序。不存在的投递返回 404，已有但未执行的投递返回空数组。

- Worker 领取投递时，在同一事务中登记 processing 历史，然后才执行 HTTP；登记失败则领取状态/次数也回滚。
- HTTP 结果和投递状态在同一事务中回写；已完成历史不被重复结果覆盖。旧请求晚到可以补全自己的未完成历史，但不能改变新执行的投递状态。
- `finished_at=null` 表示结果尚未记录：可能正在执行，也可能已经崩溃或结果写回失败。**不代表 HTTP 一定没发送或远端没处理。** 没有自动推断为失败。
- http_status 为 null 表示没有记录到 HTTP 响应；error_class 只保存固定分类（如 http_error、timeout、network_error），不保存原始 URL、payload 或错误详情。
- started_at 是领取时刻，latency_ms 是 Sender 耗时。领取后发现 URL 不允许时仍留失败记录，latency_ms 为 null；端点禁用或预算已耗尽时根本不领取，不新增尝试。
- 这是执行尝试历史，不是精确的远端请求计数；尚无历史清理策略。

## 失败重放

先修复接收端问题，再对 failed 投递调用（替换为实际 delivery_id）：

```bash
curl -i -X POST http://127.0.0.1:8080/v1/deliveries/9/replay \
  -H "X-API-Key: $API_KEY"
```

成功返回 202，例如：

```json
{"delivery_id":"9","event_id":"7","generation":2,"status":"pending"}
```

- 同事务执行：接入锁 → 锁定投递并检查 failed → 检查端点启用/容量 → generation+1、attempt_count=0、清空最后错误/状态码/租约 → 新 Outbox。Outbox 写入失败则整轮回滚。
- delivery_id、event_id、payload 不变，旧 Outbox 与尝试历史不删除；attempt_count 现在表示当前 generation 的领取次数。每轮仍最多 3 次领取，人工再次重放可开启新预算。
- 不存在返回 404；pending/processing/retrying/succeeded 返回 409 `delivery_not_replayable`；端点禁用返回 409 `endpoint_disabled`，需要先启用。
- 满载返回 503、`Retry-After: 5`、`{"error":"backlog_full","replayed":false}`；原 failed 状态和 generation 不变，不生成新 Outbox。
- 同一失败状态被并发请求时，只能创建一轮新执行。它不是带幂等键的重放接口：若新一轮后来又 failed，再 POST 会开启下一轮。请求超时/响应丢失时先查状态及 generation，不要盲目重复重放。
- 旧任务被 generation 检查跳过；旧请求晚到只能补全旧历史，不能覆盖新一轮状态。已经发出的 HTTP 无法撤回，接收端仍需按稳定 delivery_id 幂等。若希望产生一次新的业务效果，应提交新事件，而不是重放。
- 202 只表示重放事务提交，不表示已经入队或投递成功。可查询 delivery 状态和跨 generation 的 attempts 历史。

## Outbox 发布流程

```text
API ──MySQL 事务──► events + deliveries + outbox
                                            │
                                      Dispatcher
                                            │
                                       Redis/Asynq
                                            │
                                          Worker → HTTP
```

Outbox 状态：`pending → processing → published`。领取用短事务和 30 秒租约，事务提交后才请求 Redis；入队失败回到 pending，按 5/10/20/40/60 秒退避。Redis 超时 5 秒，空闲每秒扫描一次。

`available_at` 在 pending 时表示下次重试时间，在 processing 时表示租约到期时间。Dispatcher 重启可重新领取过期记录；`publish_attempts` 是领取令牌，阻止旧发布者覆盖新状态。

入队成功但标记失败时允许重复发布，任务仍使用稳定 delivery_id/generation；已成功投递会由 Worker 跳过。**published 只表示入队已确认，不表示投递成功。** 这不是接收方业务的恰好一次保证。

## Worker 与当前限制

- 任务类型 `webhook:deliver`，队列 `webhooks`，payload 仅含 delivery_id 与 generation。稳定任务 ID 抑制队列内重复；已终态或旧 generation 由数据库跳过。
- 当前默认：单进程、并发 1，最多 3 次领取，HTTP 超时 5 秒、任务超时 20 秒、执行租约 30 秒。参数集中于 `internal/task/delivery.go`；Asynq 负责退避，数据库负责状态与预算。
- 领取使用短事务；发送不持有事务；回写校验 generation 和 attempt_count。租约过期仍可能重复发送，不能保证外部业务恰好一次。
- 已保存但未入队的记录由 Outbox 恢复。**已 published 后 Redis 丢失任务不在该恢复范围内**，仍需 Reconciler。
- 容量限制针对未完成投递数量，不是总数据量或 Redis 内存限制；终态事件和 Outbox 历史仍会累积，目前没有清理机制。
- Redis 数据丢失、任务因数据库长期故障耗尽重试、最终结果无法写库，可能遗留非终态记录；没有 Reconciler。Worker 强杀恢复时间、多实例并发尚未做故障验证。
- 暂无 HMAC、接收端幂等、接收方 Retry-After 解析和全局端点隔离。签名密钥目前仅保存，不参与发送。

## 下一步

完善 HTTP 重试策略：接收方 Retry-After 解析、有上限的退避与抖动。之后补 HMAC 和接收方幂等；Redis 丢任务后的自动对账仍是后续任务。
