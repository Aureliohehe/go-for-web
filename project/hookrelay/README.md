# HookRelay

基于 Go、MySQL、Redis 和 Asynq 的可靠 Webhook 投递平台学习项目。

目标能力：幂等事件接入、Outbox 可靠入队、异步 HTTP 投递、有限重试、投递历史、失败重放及接收方签名与幂等示例。

> 已打通事件/投递/Outbox 同事务 → Dispatcher → Redis/Asynq → Worker → HTTP 投递，支持入队恢复和有限投递重试。已加入未完成投递容量限制、投递尝试历史、分页查询和失败重放；**已接入 HMAC 签名、接收端验签与事务性持久化幂等，已支持单 Worker 内端点并发限制，已有保守自动对账，尚无跨进程全局配额，仅供本地受控演示。**

## 学习进度（下次先看这里）

详见 [学习记录与下次起点](docs/learning-progress.md)。最新已完成隔离 MySQL 8.0.42 / Redis 7.4.2 联调：失败重放、列表筛选分页、指标、延期和取消回写通过；单任务删除后约 61.68 秒恢复，Worker 强杀后约 65.30 秒恢复且接收端业务记录仍只有一条。使用缩短的对账宽限期和扫描间隔，非默认配置时限保证；详见 [联调与恢复演练记录](docs/manual-validation.md)。

用户已完成 `000003`（Outbox）和 `000004`（接入锁），助手已只读确认，**不要重复导入**。`000005_create_delivery_attempts.up.sql` 已由助手按用户授权导入 `hookrelay`，并验证表结构、索引、外键及导入时历史记录为 0；前五份迁移均已完成，不要重复执行。**本轮新增 `000006_add_delivery_recovery.up.sql`，仅在隔离测试库验证，开发库尚未执行；升级代码前先按下方“自动对账恢复”章节补这份迁移。**隔离 MySQL 8.0.42/Redis 7.4 下已验证并发重放、Outbox 写入失败回滚、重放与新事件共享容量、旧 generation 隔离及真实 HTTP 重放后成功投递；历史保留且投递 ID 不变。

Docker 本轮已恢复可用；隔离 MySQL 8.4 / Redis 7.4 上已通过 Retry-After、退避、签名队列测试及“接收端已提交但响应超时”的幂等重投验证：2 次 HTTP 请求、1 条业务记录，最终投递成功。

Receiver 现在需要 `RECEIVER_SIGNING_SECRET` 和独立的 `RECEIVER_MYSQL_*` 配置。新增迁移 **`migrations/receiver/000001_create_receipts.up.sql` 仅应用到接收端独立数据库**，与平台 `000006` 无关；助手仅在隔离测试库导入，用户开发环境尚待配置，不要重复导入平台五份迁移。

新增单 Worker 内端点隔离：`WORKER_CONCURRENCY=4`、`ENDPOINT_CONCURRENCY=1`，满额任务在领取前延迟重新入队，不消耗 HTTP 尝试预算。端点配额本身无需迁移，但本轮自动恢复需要平台 `000006`；Receiver 独立库的首次配置要求不变。

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
# 本轮需先执行新增 000006，见“自动对账恢复”章节。
```

平台数据库前五份迁移已完成，勿重复执行；本轮只补 `000006`。**Receiver 尚未配置时，先按 [Receiver 数据库准备](#receiver-数据库准备) 创建独立库并导入接收端迁移。** 全新平台数据库需要按顺序执行顶层六份迁移。`000005` 不补造升级前的历史，所以历史条数可能小于过去的 attempt_count。

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
- `GET /metrics`：需 `X-API-Key`，返回 Prometheus 文本格式的接入、投递与 Outbox 指标，详情见下方“基础指标”。
- `POST /v1/endpoints`：需 `X-API-Key`，JSON 请求体只接收 `url`，且必须与 `ALLOWED_ENDPOINT_URL` 完全相同；成功返回 201，签名密钥仅在该响应中交付一次。设置 `Content-Type: application/json`。
- `GET /v1/endpoints/:id`：查询接收端，需 `X-API-Key` 请求头；不返回签名密钥，响应 ID 为字符串。
- `PATCH /v1/endpoints/:id`：需 `X-API-Key`，JSON 请求体仅 `{"enabled":true}` 或 `{"enabled":false}`；返回更新后的接收端公开字段，不返回密钥。禁用拒绝新事件；Worker 在领取时发现禁用会将已有任务置为 failed；已发出的请求不能撤回。
- `POST /v1/events`：需 `X-API-Key`、`Idempotency-Key` 和 JSON 请求体；首次返回 202，同键同内容返回 200 和原 ID，同键不同内容返回 409。HTTP 投递由独立 Worker 异步执行。
- `GET /v1/deliveries?status=failed&endpoint_id=1&limit=20&after=0`：需 `X-API-Key`，按 ID 升序分页列出投递；状态和端点筛选可省略。
- `GET /v1/deliveries/:id`：需 `X-API-Key`，返回投递 ID、事件 ID、状态、执行版本、尝试次数、最后 HTTP 状态/错误分类及创建/更新时间；不存在返回 404。
- `GET /v1/deliveries/:id/attempts?limit=20&after=0`：需 `X-API-Key`，按尝试 ID 升序分页查询历史。
- `POST /v1/deliveries/:id/replay`：需 `X-API-Key`，仅允许 failed 状态，成功返回 202 和新 generation；不需要请求体。
- `go test ./...`：运行不依赖数据库的测试（包括 Sender/Receiver 的本地 HTTP 测试），集成测试默认跳过。
- **推荐：`bash scripts/test-integration.sh`**。需要 Docker、Go、openssl；自动创建独立 MySQL 8.4/Redis 7.4 容器、随机本地端口和两个空测试库，应用平台与 Receiver 迁移，运行存储/背压/接收端/队列 race 测试，退出时只删除本次测试容器及其临时卷。不会加载 `.env`、操作已有容器或业务库；日志保留在输出的 `/tmp/hookrelay-integration-*`。
- `HOOKRELAY_RECEIVER_INTEGRATION=1 go test -race -count=1 -v ./internal/receiver`：使用 `RECEIVER_MYSQL_*` 指向独立接收端测试库，需接收端迁移；覆盖并发去重、冲突、第二次写入失败回滚及新连接池读取已提交记录。
- `HOOKRELAY_INTEGRATION=1 go test -count=1 -v ./internal/store ./cmd/api`：需 MySQL 和六份已应用迁移，覆盖事务、并发幂等、历史原子写入、游标查询、并发重放与旧轮次回写；测试回滚或只清理自身记录。
- `HOOKRELAY_ADMISSION_INTEGRATION=1 go test -count=1 -race -run TestAdmissionIntegration ./internal/store`：仅对已迁移的独立空测试库运行，无其他业务进程；验证最后一个名额竞争、满载幂等、终态释放、事务回滚，以及新事件与重放争抢同一容量。不要清空业务库来满足测试条件。
- `HOOKRELAY_OUTBOX_INTEGRATION=1 go test -count=1 -race -v ./tests/integration`：**仅对已迁移的独立测试数据库运行，Outbox 必须为空，不能有 API/Dispatcher/Worker 同时运行**。需 Redis 7.4；测试使用独立随机队列。覆盖成功/重试/归档、Redis 连接失败后恢复、领取后失联、入队后确认失败、旧执行令牌、真实 HTTP 历史分页及失败重放后成功投递。不要指向日常开发数据库，也不要为了通过检查清空业务表。同时设置 `HOOKRELAY_RECEIVER_INTEGRATION=1` 并配置独立空接收端库，可额外验证提交后响应超时的真实队列重投。

用户已确认 Docker 工具及引擎可用，并完成 Compose 启动 MySQL、数据库停止与恢复的手动验证。`docs/day-01.md` 保留的是初始实现时的操作指南与验证记录，最新学习状态以学习进度文档为准。

## 基础指标

API 新增受 `X-API-Key` 保护的 `GET /metrics`，无需新配置、依赖或数据库迁移（已有代码要求的平台 `000006` 仍须完成）：

```bash
curl -fsS http://127.0.0.1:8080/metrics -H "X-API-Key: $API_KEY"
```

| 指标 | 含义 |
| --- | --- |
| `hookrelay_event_admissions_total{outcome}` | 当前 API 进程的存储接入次数，区分 accepted、reused、backlog_full、invalid_event、endpoint_not_found、endpoint_disabled、idempotency_conflict、internal_error |
| `hookrelay_event_admission_duration_seconds_sum/count{outcome}` | 上述存储接入操作累计耗时/次数，可计算均值；不提供分位数 |
| `hookrelay_events` | 数据库中已持久化事件数，幂等复用不会增加 |
| `hookrelay_deliveries{status}` | 当前投递状态数量，含所有 API/Worker 的数据 |
| `hookrelay_deliveries_backlog` / `hookrelay_pending_deliveries_limit` | pending + processing + retrying 积压 / 配置的容量上限 |
| `hookrelay_outbox{status}` / `hookrelay_outbox_backlog` | Outbox 状态数量 / 未发布数量（含处理中租约） |
| `hookrelay_deliveries_oldest_pending_age_seconds` / `hookrelay_outbox_oldest_pending_age_seconds` | 各自最老未完成记录自创建以来的年龄；包含处理中和等待重试，不只 pending；空积压为 0 |
| `hookrelay_finished_attempts{status}` | 已保存结果的尝试数，包含历史 generation |
| `hookrelay_attempt_latency_seconds{status}` / `hookrelay_attempt_latency_observations{status}` | 已记录尝试耗时总秒数 / 样本数，相除得到均值；未知耗时不计入 |
| `hookrelay_unfinished_attempts` | 没有保存结果的尝试数，包含正在执行与崩溃遗留，不能直接当作失败数 |

接入计数和耗时来自 API 内存，进程重启归零；仅覆盖进入存储接入函数的调用，不包含鉴权失败、JSON 解码失败，也不包含人工重放。`accepted` 表示存储函数报告成功，不表示客户端一定收到了响应。其余指标是 MySQL 一致性快照，标记为 **gauge**：清理记录、人工重放和自动恢复可能改变数量，不能当作单调递增的成功投递计数器。投递耗时来自已有尝试历史，不是端到端排队耗时；未完成尝试不伪造结果。

建议每 30 秒抓取一次。每个 API 进程同一时间只允许一次采集，重叠请求返回 503；数据库采集总超时 2 秒，失败返回 503，不返回部分数据或伪造零值。指标不输出 ID、URL、载荷、密钥或底层错误。存储聚合会扫描历史数据，当前适用于本地小规模演示，尚未做大数据量压测。多 API 实例的内存计数应分别抓取后求和，**共享数据库 gauge 不能跨实例相加**。

已在隔离联调中验证真实 MySQL 采集、HTTP 文本输出及关键计数，尚未部署 Prometheus 验证抓取配置。Dispatcher 的限时结果回写也已通过真实 Redis/MySQL 上的精确取消探针验证：Redis 发布仍响应取消，发布结果使用独立 5 秒上下文保存；失败仍依赖租约恢复，不承诺消除重复投递。详见 [演练记录](docs/manual-validation.md)。

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

## 筛选与分页列出投递

不必先知道 delivery_id，可以先找出失败记录，再查看历史并按需调用已有重放接口：

```bash
curl -fsS 'http://127.0.0.1:8080/v1/deliveries?status=failed&endpoint_id=1&limit=20' \
  -H "X-API-Key: $API_KEY"
```

- `status`：可省略或为空（不限状态），否则仅允许 pending、processing、retrying、succeeded、failed。
- `endpoint_id`：可省略；提供时必须是正整数。不存在的端点返回空列表，不返回 404。
- `limit`：默认 20，范围 1～100；`after`：默认 0，表示只返回 ID 大于该值的记录。显式空值、负数或溢出无效。
- 按投递 ID 升序排列，响应为 `{"items":[...],"next_after":"123"}`；items 中每项与单条查询的公开字段一致，ID 为字符串，不返回 payload、密钥、队列任务 ID 或租约。无结果时为 `{"items":[],"next_after":""}`。
- 下一页保持相同筛选条件，把 `next_after` 原样放入 `after`。空字符串表示本次查询没有下一页；不是总数或订阅游标。
- 不合法分页返回 400 `invalid_pagination`；非法端点 ID 返回 400 `invalid_endpoint_id`；未知状态、未知/重复参数或不合法 URL 编码返回 400 `invalid_delivery_filter`。查询失败返回通用 500，不暴露数据库错误。

使用 `id > after` 而非 OFFSET，每次最多查询 limit+1 条，不做额外 COUNT；查询上下文限时 3 秒。筛选复用现有表与索引，无新迁移，但稀疏筛选仍可能扫描较多历史数据，尚未做大数据量压测。

**这是实时列表，不是跨页一致性快照。** 查询失败列表后，重放会让该记录离开 failed；已经越过游标的其他记录若随后变为 failed，需要从 after=0 重新扫描才能看到。列表不会锁定记录或自动重放，最终能否重放仍由重放事务校验。新增列表接口已通过隔离 HTTP/MySQL 联调，覆盖鉴权、状态/端点筛选、两页游标、空结果和无效参数；尚未验证大数据量性能。

## 查询投递

```bash
curl -i http://127.0.0.1:8080/v1/deliveries/1 -H "X-API-Key: $API_KEY"
```

使用提交事件响应中的 `delivery_id`。状态：`pending → processing → succeeded`；临时失败进入 `retrying`，永久错误或耗尽预算进入 `failed`。查询还返回 `recovery_count`（自接入或最近一次人工重放以来的自动恢复次数）。`attempt_count` 在领取时加一：崩溃发生在 HTTP 前也会占用预算，因此不是精确的远端接收次数。

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
- 重试延迟只计算一次，与投递结果同事务保存最早重试时间。复用 `deliveries.lease_until`：processing 时表示执行租约，retrying 时表示重试等待截止时间；终态清空。重复任务或 Asynq 秒级取整导致的提前领取会延期，不新增历史或消耗投递预算。无需迁移；旧 retrying 记录没有等待截止时间，不追溯补算。
- **最早重试时间不是精确发送时间。** Asynq 仍负责正常重试调度，数据库只在领取时检查，不扫描或主动入队。新版 Worker 发现任务提前到达时，读取 `Claim` 返回的租约/等待截止时间，按 `max(当前时间, 截止时间) + 2～4 秒抖动` 一次性延期，再向上取整到秒，避免长等待期间每几秒重新入队。实际执行仍受队列扫描、积压影响；下次领取仍重验数据库，截止时间若发生变化可能再次延期。比较使用 Worker 的 UTC 时钟，依赖时钟同步；结果写库失败时无法保证这次等待已持久化。
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

- 同事务执行：接入锁 → 锁定投递并检查 failed → 检查端点启用/容量 → generation+1、attempt_count=0、recovery_count=0、清空队列任务 ID/最后错误/状态码/租约 → 新 Outbox。Outbox 写入失败则整轮回滚。
- delivery_id、event_id、payload 不变，旧 Outbox 与尝试历史不删除；attempt_count 表示自首次接入或最近一次人工重放以来的累计领取次数。自动恢复也会递增 generation，但不重置 attempt_count；最多 3 次领取，只有人工再次重放才开启新预算。
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

**端点满额时不阻塞等许可：** 通过 Asynq 先发布 2～4 秒随机延迟、向上取整到秒的替代任务，将返回的队列 ID 写入 `deliveries.queue_task_id` 并刷新 updated_at，再确认当前任务完成。不调用 Claim、不增加 attempt_count、不新增尝试历史、不改变业务状态。剩余普通重试预算作为新任务的 MaxRetry 传递，不能通过限流“补满”失败预算；业务每轮最多 3 次领取仍由数据库裁决。调度有秒级取整/扫描误差，不保证精确等待时间。

为何不用普通错误 + `IsFailure=false`？Asynq v0.25.1 在重试上限处先归档，再检查 IsFailure；这会吃掉满额任务的最后一次机会。替代任务方案也能处理 MaxRetry(0)，但有如下边界：

- 新队列任务使用新 Asynq ID，**delivery_id/generation 不变**；初始 Outbox 发布仍使用稳定队列 ID。对账优先检查已记录的替代任务 ID，没有记录时才使用初始 ID。
- Redis 入队仍响应任务取消；入队成功后的队列 ID 回写使用独立 5 秒限时上下文，避免退出信号直接丢弃替代任务的身份。发布成功但旧任务确认前崩溃仍可能重复入队，仍依赖数据库领取令牌与接收端幂等；发布或回写失败不能确认旧任务，会走普通基础设施重试。Redis 长期故障、任务 ID 写库失败或最后一次发布失败仍可能遗留非终态记录；启用 Reconciler 后按下述规则受控恢复。
- 配额按 **endpoint_id**，不是按 URL/域名；同 URL 的不同端点各有配额。只在单进程内生效，多 Worker 会叠加；请求超时后远端可能继续处理，本地许可不能限制远端仍在运行的业务。
- 不是严格公平调度，也不保证等待上限：大量积压仍有查询/入队开销，热端点可能反复延期；当前没有最大延期次数或总等待期限。共享 MySQL/Redis 故障也不受此隔离保护。

验证入口：`bash scripts/test-integration.sh`，其中 `TestEndpointIsolation` 使用 4 个槽、每端点 1 个许可，阻塞一个慢请求并积压 6 个同端点任务，再投递另一个端点。断言快端点在慢请求未释放时完成、重复延期不耗预算、慢端点峰值为 1、释放后全部成功。测试使用同一受控 URL 下两个端点 ID，不是跨域名隔离或吞吐压测；实际观测时间见学习记录。

## 自动对账恢复（Reconciler）

**升级要求：** 停止旧 API/Worker/Dispatcher，仅补平台迁移 `migrations/000006_add_delivery_recovery.up.sql`，再启动新版进程。前五份不要重跑。助手只在隔离测试库执行了 `000006`，没有修改开发库。确认尚未执行后，可用管理员运行一次：

```bash
docker compose exec -T mysql sh -c \
  'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot "$MYSQL_DATABASE"' \
  < migrations/000006_add_delivery_recovery.up.sql
```

迁移增加 `queue_task_id`、`recovery_count` 和扫描索引。即使暂不启用 Reconciler，新版 API/Worker 也需要这些字段；Receiver 独立数据库无需改动。若执行失败，先检查表结构，勿盲目重跑或清库。

恢复功能作为独立进程运行，不会随 API 自动启动。在加载平台 `MYSQL_*`、`REDIS_ADDR` 后另开终端：

```bash
export RECONCILE_GRACE=5m RECONCILE_INTERVAL=30s
export RECONCILE_BATCH_SIZE=100 MAX_DELIVERY_RECOVERIES=3
go run ./cmd/reconciler
```

以上为默认值：宽限期至少 1 分钟，扫描间隔为正数，每批 1～1000 条，恢复上限为正整数。默认每 30 秒扫描一批，用 ID 游标遍历，避免低 ID 的健康积压让后续记录一直得不到检查。等待时间还受批次和积压影响，不承诺恰好 5 分钟恢复。仍按单实例运行。

核心流程：

1. 只检查 pending/processing/retrying，且当前 generation 的 Outbox 已 published。最后活动、发布确认和租约/重试等待到期时间都必须越过宽限期；未发布 Outbox 留给 Dispatcher。
2. 在数据库事务外检查记录的队列任务 ID。pending、active、scheduled、retry 等仍存活的任务直接跳过，即使队列暂停；只有明确不存在、archived 或 completed 才进入恢复。Redis 查询错误不当作丢失。
3. 短事务锁定投递，重新校验 generation、尝试次数、恢复次数、队列 ID 和 updated_at；检查期间若有 Worker/延期活动则放弃本次恢复。
4. 同事务执行 generation+1、recovery_count+1、回到 pending、创建新 Outbox；**保留 attempt_count、稳定 delivery_id 和历史，不直接请求 Redis**。旧任务不能领取新一代，旧结果只能补自己的历史，不能覆盖恢复后的状态。
5. 已耗尽 3 次业务领取或自动恢复上限时置 failed，分别记录 `attempts_exhausted` / `recovery_exhausted`；generation 无法递增时记录 `generation_exhausted`。前两种失败可按原 API 人工重放并重置两种预算；generation 已到上限则不能再重放。

自动恢复的 generation 是新的执行版本，不等于新的投递预算；该版本的首条历史 attempt_number 可能不是 1。`GET /v1/deliveries/:id` 返回 recovery_count 便于诊断。

边界：保存的是最近已知队列 ID，不是所有重复任务的完整索引。入队成功但记录 ID 前崩溃、升级前未追踪的延期任务或并发重复任务，仍可能导致恢复时替换一个尚存的旧任务；generation 和总尝试预算限制其影响。无法撤回已发出的 HTTP，仍需要接收方幂等。时间判断依赖 Worker/Reconciler 时钟同步；恢复不是严格的恰好一次保证。

此前隔离 MySQL 8.4/Redis 7.4 下覆盖单任务删除、存活延期跳过、归档恢复、快照变化跳过、查询错误不恢复、Outbox 失败回滚、旧结果隔离和预算耗尽。最新又完成独立进程下任务删除与 Worker 强杀演练，详情见 [运行记录](docs/manual-validation.md)；仍未做整库 FLUSH 或多实例演练。测试入口为 `TestRecovery`（需专用空库和六份迁移）；现有 `scripts/test-integration.sh` 会创建隔离环境并包含它。

## Worker 与当前限制

- 任务类型 `webhook:deliver`，队列 `webhooks`，payload 仅含 delivery_id 与 generation。初始发布用稳定队列任务 ID 抑制重复；端点延期使用新队列 ID，已终态或旧 generation 由数据库跳过。
- 当前默认：单进程、总并发 4、每端点并发 1，最多 3 次领取，HTTP 超时 5 秒、任务超时 20 秒、执行租约 30 秒。次数及超时集中于 `internal/task/delivery.go`；退避通过上述环境变量配置，由 Asynq 调度，数据库负责状态与预算。
- 领取使用短事务；发送不持有事务；回写校验 generation 和 attempt_count。租约过期仍可能重复发送，不能保证外部业务恰好一次。
- 遇到仍有效的执行租约或尚未到期的重试等待时，延期到所观察的截止时间之后，不把等待算作普通重试或新增投递尝试；替换入队失败仍返回错误，不提前确认原任务。`cmd/worker` 已接入 `DeferDeliveryUntil`；自行组装 Worker 若仅提供旧 `Defer` 回调，仍退回短周期延期，数据库仍阻止提前领取。
- 截止时间感知调度及延期 ID 取消回写已在真实隔离 MySQL/Redis 上验证：429 后提前到达的重复任务被安排在数据库截止时间后，取消上下文仍保存队列 ID，未新增尝试或补满预算；本轮使用临时探针，未新增项目测试文件。
- HTTP 继续遵循任务取消；结果回写使用独立的 5 秒限时上下文，避免任务取消直接丢掉已获得的 HTTP 结果。回写失败仍交给队列重试；进程强杀或数据库持续不可用仍可能留下未完成历史。该回写逻辑本身无需迁移；本轮自动恢复升级仍需 `000006`。
- 已保存但未入队的记录由 Outbox 恢复。**已 published 后 Redis 丢失任务不是 Outbox 单独能恢复的**；需额外运行 Reconciler，且仍受宽限期、队列检查和恢复预算约束。
- 容量限制针对未完成投递数量，不是总数据量或 Redis 内存限制；终态事件和 Outbox 历史仍会累积，目前没有清理机制。
- Reconciler 未运行或依赖长期不可用时仍可能遗留非终态记录；它也不会强行接管队列中仍存活的任务。Worker 强杀后已观测到一次约 65.30 秒恢复（本次配置与单次观测，不是时限保证）；完整 Redis 清空及多实例故障验证尚未完成。
- 已有 HMAC、验签、接收端同库事务幂等与单进程端点配额；暂无跨 Worker 全局并发隔离。接收端示例不保证数据库之外的业务副作用恰好一次。

## 下一步

自动对账核心、基础运行指标、截止时间感知延期和投递筛选列表均已实现；最近增量已完成隔离联调，并观测了单任务删除及 Worker 强杀恢复。下一步优先完整 Redis 数据丢失与多实例故障演练，之后再补自动化回归和性能验证；不将当前实现描述为完整可靠或跨 Worker 全局隔离版本。
