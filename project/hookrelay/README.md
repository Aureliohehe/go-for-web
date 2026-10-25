# HookRelay

基于 Go、MySQL、Redis 和 Asynq 的可靠 Webhook 投递平台学习项目。

目标能力：幂等事件接入、Outbox 可靠入队、异步 HTTP 投递、有限重试、投递历史、失败重放及接收方签名与幂等示例。

> 已打通事件/投递/Outbox 同事务 → Dispatcher → Redis/Asynq → Worker → HTTP 投递，支持入队恢复和有限投递重试。已加入未完成投递容量限制、投递尝试历史、分页查询和失败重放；**已接入 HMAC 签名、接收端验签与事务性持久化幂等，已支持单 Worker 内端点并发限制，尚无自动对账或跨进程全局配额，仅供本地受控演示。**

## 学习进度（下次先看这里）

详见 [学习记录与下次起点](docs/learning-progress.md)。

用户已完成 `000003`（Outbox）和 `000004`（接入锁），助手已只读确认，**不要重复导入**。`000005_create_delivery_attempts.up.sql` 已由助手按用户授权导入 `hookrelay`，并验证表结构、索引、外键及导入时历史记录为 0；五份迁移均已完成，不要重复执行。本轮失败重放不需要新迁移。隔离 MySQL 8.0.42/Redis 7.4 下已验证并发重放、Outbox 写入失败回滚、重放与新事件共享容量、旧 generation 隔离及真实 HTTP 重放后成功投递；历史保留且投递 ID 不变。

Docker 本轮已恢复可用；隔离 MySQL 8.4 / Redis 7.4 上已通过 Retry-After、退避、签名队列测试及“接收端已提交但响应超时”的幂等重投验证：2 次 HTTP 请求、1 条业务记录，最终投递成功。

Receiver 现在需要 `RECEIVER_SIGNING_SECRET` 和独立的 `RECEIVER_MYSQL_*` 配置。新增迁移 **`migrations/receiver/000001_create_receipts.up.sql` 仅应用到接收端独立数据库**，不是平台第六份迁移；助手仅在隔离测试库导入，用户开发环境尚待配置，不要重复导入平台五份迁移。

新增单 Worker 内端点隔离：`WORKER_CONCURRENCY=4`、`ENDPOINT_CONCURRENCY=1`，满额任务在领取前延迟重新入队，不消耗 HTTP 尝试预算。本轮无需新迁移，只需重启 Worker；上一轮 Receiver 独立库的首次配置要求不变。

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

平台数据库五份迁移均已完成，勿重复执行。**本轮先按 [Receiver 数据库准备](#receiver-数据库准备) 创建独立库并导入接收端迁移，再配置 Receiver。** 全新平台数据库才需要按顺序执行顶层五份迁移。`000005` 不补造升级前的历史，所以历史条数可能小于过去的 attempt_count。

先在三个终端分别运行（均需加载上述环境）：

```bash
go run ./cmd/worker
go run ./cmd/dispatcher
go run ./cmd/api
```

创建端点（尚未启动 Receiver 也可以创建）：

```bash
curl -s http://127.0.0.1:8080/v1/endpoints \
  -H "X-API-Key: $API_KEY" -H 'Content-Type: application/json' \
  -d '{"url":"http://127.0.0.1:9090/webhook"}'
```

保存响应中的 `id` 和一次性交付的 `signing_secret`，不要贴到对话或提交到 Git。在第四个终端配置 Receiver，交互输入可避免密钥进入 shell 命令历史：

```bash
read -rsp 'Endpoint signing_secret: ' RECEIVER_SIGNING_SECRET; echo
export RECEIVER_SIGNING_SECRET
# 本终端还需加载下面配置的 RECEIVER_MYSQL_*，特别是独立数据库密码。
go run ./cmd/receiver
```

Receiver 单实例只使用一个端点密钥；多个端点指向相同 URL 但密钥不同，会验签失败。已有端点若未保存一次性密钥，请创建新端点并使用新 ID；GET 不提供取回密钥功能。之后再提交事件，避免 Receiver 尚未启动时耗尽投递预算。

创建接收端后，按下方示例提交事件并查询投递。模拟故障时，停止并重启**模拟接收端**：`go run ./cmd/receiver -status 500` 或 `go run ./cmd/receiver -delay 8s`，再提交新事件。默认使用下述自定义退避，重试不是立即发生。也可用 `go run ./cmd/receiver -status 429 -retry-after 10` 模拟限流。只运行一个 Worker 和一个 Dispatcher；接收端已验签并持久化去重。`-delay 8s` 在首次业务事务提交后延迟响应，重复请求立即返回 204；`-status 500/429` 在写库前模拟拒绝，不产生业务记录。使用 Compose 的 Redis 7.4，宿主旧 Redis 5 不兼容 Asynq 的归档操作。

## Receiver 数据库准备

接收端默认使用同一 MySQL 实例上的**独立数据库/用户** `hookrelay_receiver`，不读取平台的 `MYSQL_*` 配置，不共享业务表。也可以通过 `RECEIVER_MYSQL_HOST/PORT` 指向另一实例。

首次由数据库管理员连接（例如 `docker compose exec mysql mysql -uroot -p`），创建独立数据库和用户；密码自行替换，不要使用示例占位符：

```sql
CREATE DATABASE hookrelay_receiver;
CREATE USER 'hookrelay_receiver'@'%' IDENTIFIED BY '<自行设置的接收端数据库密码>';
GRANT SELECT, INSERT ON hookrelay_receiver.* TO 'hookrelay_receiver'@'%';
```

然后在项目根目录用管理员权限导入**一次**（以下命令使用容器已有的 root 环境配置，不输出密码）：

```bash
docker compose exec -T mysql sh -c \
  'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot hookrelay_receiver' \
  < migrations/receiver/000001_create_receipts.up.sql
```

DDL 非事务性，若失败先检查两张表是否部分创建，不要盲目重跑或删表。Receiver 不自动建库/建表，缺表时拒绝启动。

在 Receiver 终端设置（可自行保存到不提交的 `.env`，不会自动保存）：

```bash
export RECEIVER_MYSQL_HOST=127.0.0.1 RECEIVER_MYSQL_PORT=13306
export RECEIVER_MYSQL_DATABASE=hookrelay_receiver RECEIVER_MYSQL_USER=hookrelay_receiver
read -rsp 'Receiver database password: ' RECEIVER_MYSQL_PASSWORD; echo
export RECEIVER_MYSQL_PASSWORD
```

不要把平台密码当作接收端密码；平台 API/Worker/Dispatcher 的数据库配置无需修改。默认接收端用户只需 SELECT/INSERT，不需要修改或清空历史的权限。

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
- `go test ./...`：运行不依赖数据库的测试（包括 Sender/Receiver 的本地 HTTP 测试），集成测试默认跳过。
- **推荐：`bash scripts/test-integration.sh`**。需要 Docker、Go、openssl；自动创建独立 MySQL 8.4/Redis 7.4 容器、随机本地端口和两个空测试库，应用平台与 Receiver 迁移，运行存储/背压/接收端/队列 race 测试，退出时只删除本次测试容器及其临时卷。不会加载 `.env`、操作已有容器或业务库；日志保留在输出的 `/tmp/hookrelay-integration-*`。
- `HOOKRELAY_RECEIVER_INTEGRATION=1 go test -race -count=1 -v ./internal/receiver`：使用 `RECEIVER_MYSQL_*` 指向独立接收端测试库，需接收端迁移；覆盖并发去重、冲突、第二次写入失败回滚及新连接池读取已提交记录。
- `HOOKRELAY_INTEGRATION=1 go test -count=1 -v ./internal/store ./cmd/api`：需 MySQL 和五份已应用迁移，覆盖事务、并发幂等、历史原子写入、游标查询、并发重放与旧轮次回写；测试回滚或只清理自身记录。
- `HOOKRELAY_ADMISSION_INTEGRATION=1 go test -count=1 -race -run TestAdmissionIntegration ./internal/store`：仅对已迁移的独立空测试库运行，无其他业务进程；验证最后一个名额竞争、满载幂等、终态释放、事务回滚，以及新事件与重放争抢同一容量。不要清空业务库来满足测试条件。
- `HOOKRELAY_OUTBOX_INTEGRATION=1 go test -count=1 -race -v ./tests/integration`：**仅对已迁移的独立测试数据库运行，Outbox 必须为空，不能有 API/Dispatcher/Worker 同时运行**。需 Redis 7.4；测试使用独立随机队列。覆盖成功/重试/归档、Redis 连接失败后恢复、领取后失联、入队后确认失败、旧执行令牌、真实 HTTP 历史分页及失败重放后成功投递。不要指向日常开发数据库，也不要为了通过检查清空业务表。同时设置 `HOOKRELAY_RECEIVER_INTEGRATION=1` 并配置独立空接收端库，可额外验证提交后响应超时的真实队列重投。

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

`internal/delivery/sender.go` 已实现单次 HTTP POST：原样发送 payload，携带稳定 `X-Webhook-ID`；2xx 成功，网络错误/超时/408/429/5xx 标记可重试，3xx 不跟随且视为永久失败，其余 4xx 不重试。Sender 使用调用方指定的 HTTP 超时，传递 context，最多读取 4 KiB 响应体以便连接复用。它不负责排队、自动重试或写数据库，对可重试响应提取单个 Retry-After 供 Worker 调度；重复响应头忽略。每次发送附带新的时间戳和 HMAC 签名，仅供受控本地测试。

## HMAC 签名与验签

协议实现：`internal/webhook/signature.go`，Worker 从端点配置传递密钥给 Sender，密钥不进入 Redis 任务。

- `X-Webhook-ID`：稳定的十进制 delivery_id，自动重试及人工重放不变。
- `X-Webhook-Timestamp`：当前尝试的 Unix 秒时间戳。
- `X-Webhook-Signature`：`v1=` 加 HMAC-SHA256 的十六进制摘要。
- 签名输入：`timestamp + "." + delivery_id + "." + 原始 payload 字节`。密钥为创建端点时 64 字符 hex 密钥**解码后的 32 字节**，不是 hex 字符串的 ASCII 字节。
- 每次发送重新生成时间戳和签名；同一秒内重试可能得到相同签名。不能先重新序列化 JSON 再验签，空白变化也会导致验签失败。
- Receiver 使用 `hmac.Equal` 比较摘要；时间戳允许前后各 5 分钟偏差，边界按秒计算。篡改、过期、超前、缺失或重复签名相关请求头返回 401，且不进入模拟处理。
- Receiver 必须设置 `RECEIVER_SIGNING_SECRET`，缺省/格式错误拒绝启动；没有无签名兼容模式。请求体最多 256 KiB，超限 413，读请求超时 5 秒。验签成功后才执行 `-delay`、`-status`、`-retry-after` 模拟行为。
- Sender 遇到非法本地密钥不发 HTTP；Worker 记录 `signing_error` 并最终失败。密钥格式合法但与 Receiver 不匹配，返回 401，按永久 HTTP 错误处理。
- **签名不等于幂等，也不加密正文。** 时间窗口内有效请求仍可重复到达；接收方业务去重由独立数据库事务完成。非本地传输仍应使用 HTTPS。端点密钥目前明文保存在开发数据库，不提供轮换/加密存储能力。

## 接收方持久化幂等

实现位于 `internal/receiver/store.go` 和 `handler.go`：

```text
验签 → BEGIN → INSERT receiver_receipts(delivery_id 唯一, payload_hash)
             → INSERT receiver_effects(模拟业务写入) → COMMIT → HTTP 响应
```

- 不先查再写，靠唯一约束裁决并发；任一写入失败，两条记录一起回滚。去重 INSERT 冲突后退出失败事务，再读取已提交摘要。
- 相同 ID、相同原始正文返回 204，不重复写业务；相同 ID、不同正文返回 409，不覆盖原数据。数据库不可用/结果不确定返回 503，后续重试仍由唯一约束保护。
- 每次重试仍需有效签名。自动重试、人工重放都保留 delivery_id，因此不会再次产生已提交的模拟业务效果；真正需要新效果时创建新事件。
- `-status` 为 2xx 时，先提交事务，再按 `-delay` 延迟**首次**响应。客户端超时/断连不撤销已提交记录；重复请求跳过延迟直接 204。非 2xx 模式在业务写入前模拟错误。
- 可用 `go run ./cmd/receiver -delay 8s` 演示：默认 Worker HTTP 超时 5 秒，第一次 timeout，第二次幂等成功。查询投递 attempts 应看到两次尝试；管理员查询 `SELECT delivery_id, COUNT(*) FROM receiver_effects GROUP BY delivery_id;`，每个 ID 只有一条业务记录。
- 当前只模拟数据库内业务写入，**不保证发邮件、扣外部账户等外部副作用恰好一次**。验证包含真实数据库/队列和新连接池重读，尚未做接收端进程强杀或数据库灾难恢复测试。
- 去重记录不自动过期；不要只清空 receipt 或重置平台数据库后复用同一个接收端库。去重键仅为 delivery_id，假设一个 HookRelay ID 命名空间，不支持多个独立平台共用该接收端数据集。模拟业务表保存原始 payload，仅用于受控测试。

## HTTP 重试策略

Worker 使用 `internal/delivery/retry.go` 的 `RetryPolicy.Delay` 作为 Asynq 的 `RetryDelayFunc`，不另建数据库重试调度器。

- `RETRY_BASE_DELAY` 默认 `5s`，`RETRY_MAX_DELAY` 默认 `1m`；使用 Go duration 格式，要求 `0 < base <= max`。修改后重启 Worker。
- HTTP 结果的重试序号 n 使用数据库本轮 `attempt_count - 1`；尚未领取的基础设施错误使用当前 Asynq 任务的重试计数。端点满额导致更换队列任务 ID 不会重置 HTTP 退避。退避上界为 `min(base × 2^n, max)`，在其一半到全部之间随机取值。默认前两次为 2.5～5 秒、5～10 秒；每轮最多 3 次领取的业务预算不变。
- 网络错误、超时、408、429、5xx 可重试。合法 Retry-After 支持非负整数秒和 HTTP 日期，最终延迟取抖动结果与接收端建议的较大值，但不超过 max。过长值会被截断，因此不能保证遵守超过上限的接收端等待要求。
- 空值、负数、小数、无效/过期日期和重复响应头不延长退避；2xx、3xx 和永久 4xx 不因该响应头而重试。有效但过大的整数按 max 处理，避免溢出。
- 原始响应头只在内存中传给调度回调，不写日志、数据库或任务 payload。结果写库失败时也保留本次重试建议；后续执行仍受租约与业务预算约束。
- **这是调度延迟，不是精确发送时间。** Asynq v0.25.1 使用秒级时间戳（可能向下取整不足一秒），扫描间隔、队列积压与执行租约也会影响实际时间。毫秒配置适合测试，不能作为严格的等待下限或执行时刻保证。
- 配置仅控制 Worker 的任务重试，不改变 Dispatcher 的 Outbox 发布退避，也不同于 API 满载响应中的 `Retry-After: 5`。

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

## 单 Worker 端点并发限制

```bash
export WORKER_CONCURRENCY=4       # 当前进程总执行槽
export ENDPOINT_CONCURRENCY=1     # 每个 endpoint_id 最多持有的执行许可
go run ./cmd/worker
```

两项只由 Worker 读取，均为正整数，端点上限不得大于总并发；相等时会警告没有给其他端点留余量。默认从原来的串行改为 4 个执行槽。它与 API 的 `MAX_PENDING_DELIVERIES` 积压容量不是一回事。

执行顺序：只读查询端点 ID → 尝试获取进程内许可 → 领取并登记历史 → HTTP → 保存结果 → 释放许可。终态/旧 generation 直接跳过；领取时仍重新校验数据库状态。许可覆盖领取到结果回写，退出、错误或 panic 均释放；空闲端点计数会删除。

**端点满额时不阻塞等许可：** 通过 Asynq 先发布 2～4 秒随机延迟的替代任务，再确认当前任务完成。不调用 Claim、不增加 attempt_count、不新增尝试历史、不改变业务状态。剩余普通重试预算作为新任务的 MaxRetry 传递，不能通过限流“补满”失败预算；业务每轮最多 3 次领取仍由数据库裁决。调度有秒级取整/扫描误差，不保证精确等待时间。

为何不用普通错误 + `IsFailure=false`？Asynq v0.25.1 在重试上限处先归档，再检查 IsFailure；这会吃掉满额任务的最后一次机会。替代任务方案也能处理 MaxRetry(0)，但有如下边界：

- 新队列任务使用新 Asynq ID，**delivery_id/generation 不变**；初始 Outbox 发布仍使用稳定队列 ID。Redis 中不能只查初始任务 ID 就判定投递丢失，后续对账需考虑替代任务。
- 发布成功但旧任务确认前崩溃可能重复入队，仍依赖数据库领取令牌与接收端幂等；发布失败不能确认旧任务，会走普通基础设施重试。Redis 长期故障或最后一次发布失败仍可能归档并遗留非终态记录，目前没有 Reconciler。
- 配额按 **endpoint_id**，不是按 URL/域名；同 URL 的不同端点各有配额。只在单进程内生效，多 Worker 会叠加；请求超时后远端可能继续处理，本地许可不能限制远端仍在运行的业务。
- 不是严格公平调度，也不保证等待上限：大量积压仍有查询/入队开销，热端点可能反复延期；当前没有最大延期次数或总等待期限。共享 MySQL/Redis 故障也不受此隔离保护。

验证入口：`bash scripts/test-integration.sh`，其中 `TestEndpointIsolation` 使用 4 个槽、每端点 1 个许可，阻塞一个慢请求并积压 6 个同端点任务，再投递另一个端点。断言快端点在慢请求未释放时完成、重复延期不耗预算、慢端点峰值为 1、释放后全部成功。测试使用同一受控 URL 下两个端点 ID，不是跨域名隔离或吞吐压测；实际观测时间见学习记录。

## Worker 与当前限制

- 任务类型 `webhook:deliver`，队列 `webhooks`，payload 仅含 delivery_id 与 generation。初始发布用稳定队列任务 ID 抑制重复；端点延期使用新队列 ID，已终态或旧 generation 由数据库跳过。
- 当前默认：单进程、总并发 4、每端点并发 1，最多 3 次领取，HTTP 超时 5 秒、任务超时 20 秒、执行租约 30 秒。次数及超时集中于 `internal/task/delivery.go`；退避通过上述环境变量配置，由 Asynq 调度，数据库负责状态与预算。
- 领取使用短事务；发送不持有事务；回写校验 generation 和 attempt_count。租约过期仍可能重复发送，不能保证外部业务恰好一次。
- 已保存但未入队的记录由 Outbox 恢复。**已 published 后 Redis 丢失任务不在该恢复范围内**，仍需 Reconciler。
- 容量限制针对未完成投递数量，不是总数据量或 Redis 内存限制；终态事件和 Outbox 历史仍会累积，目前没有清理机制。
- Redis 数据丢失、任务因数据库长期故障耗尽重试、最终结果无法写库，可能遗留非终态记录；没有 Reconciler。Worker 强杀恢复时间、多实例并发尚未做故障验证。
- 已有 HMAC、验签、接收端同库事务幂等与单进程端点配额；暂无跨 Worker 全局并发隔离。接收端示例不保证数据库之外的业务副作用恰好一次。

## 下一步

下一步验证 Worker 强杀后的恢复过程和实际等待时间，再推进 Redis 丢任务后的自动对账与指标；不将当前单进程配额描述为跨 Worker 全局隔离。
