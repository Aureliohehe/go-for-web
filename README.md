# HookRelay

**基于 Go 的可靠 Webhook 异步投递系统**

HookRelay 是一个使用 Go、Gin、MySQL、Redis 和 Asynq 构建的 Webhook 投递项目，重点研究异步任务处理中的数据一致性、重复执行、失败重试、并发控制与故障恢复。

项目采用 **Transactional Outbox + 异步任务队列 + Worker** 架构，实现事件持久化与 HTTP 投递解耦；结合数据库状态机、执行租约、Generation 版本控制及接收端事务性幂等，处理任务重复入队、执行超时和异常恢复等场景。

> 项目定位：面向后端可靠性设计与工程实践的学习项目，目前主要针对单实例及受控测试环境，不宣称具备完整的生产级高可用能力。

## 1. 技术栈

| 组件 | 技术 | 职责 |
|---|---|---|
| API | Go、Gin | 事件接入、接口鉴权、状态查询 |
| 数据存储 | MySQL、GORM | 事件持久化、事务、投递状态机 |
| 任务队列 | Redis、Asynq | 异步任务调度和重试 |
| Dispatcher | Go | Outbox 扫描、任务发布 |
| Worker | Go | HTTP 投递、并发控制和结果回写 |
| Reconciler | Go | 异常任务对账与自动恢复 |
| Receiver | Go、MySQL | HMAC 验签、持久化去重 |
| 环境与测试 | Docker Compose、Go Test | 本地运行、集成测试及故障验证 |

## 2. 系统架构

```text
                       Client
                         |
                         v
                  +-------------+
                  |   Gin API   |
                  +-------------+
                         |
              MySQL Transaction
                         |
          +--------------+--------------+
          |              |              |
          v              v              v
        Events       Deliveries       Outbox
                         |              |
                         |              v
                         |         Dispatcher
                         |              |
                         |              v
                         |        Redis / Asynq
                         |              |
                         |              v
                         |            Worker
                         |              |
                         |         HTTP + HMAC
                         |              |
                         |              v
                         |           Receiver
                         |              |
                         |       Verify + Dedup
                         |              |
                         +<--- State Update

          Reconciler
              |
              +----> Check unfinished deliveries
              |
              +----> Recover eligible lost tasks
```

### 核心流程

1. 客户端调用 API 提交事件，携带 `Idempotency-Key`。
2. API 在同一 MySQL 事务中写入 Event、Delivery 和 Outbox。
3. Dispatcher 扫描可发布的 Outbox 记录，并将任务写入 Redis/Asynq。
4. Worker 领取任务，校验投递状态、执行版本及租约，发送带 HMAC 签名的 HTTP 请求。
5. Worker 将投递结果及尝试历史写回 MySQL。
6. 对于可重试失败，使用 Asynq 按退避策略重新调度。
7. Reconciler 定期检查符合条件的异常投递，并通过新一轮 Outbox 触发恢复。

**MySQL 是投递业务状态的主要依据，Redis/Asynq 负责任务调度。**

系统不将 HTTP 响应成功与任务入队成功混为一谈；事件接收、消息发布、远端执行和状态确认是不同阶段。

## 3. 核心设计

### 3.1 Transactional Outbox：解决双写一致性问题

如果 API 先写 MySQL，再将任务发布到 Redis，可能出现：

- MySQL 提交成功，但 Redis 发布失败。
- 任务发布成功，但进程在确认状态前崩溃。
- API 在数据库提交后异常退出，导致任务没有及时入队。

HookRelay 使用 Transactional Outbox 模式：

```text
BEGIN TRANSACTION

  INSERT events
  INSERT deliveries
  INSERT outbox

COMMIT
```

事件、投递记录及 Outbox 在同一事务中提交，避免业务记录成功保存却没有对应待发布消息的问题。

Dispatcher 独立扫描 Outbox，并使用领取租约与发布令牌处理任务重领及旧发布者回写。Redis 暂时不可用时，已提交的 Outbox 保留在 MySQL，等待后续发布。

这里采用的是 **At-Least-Once** 思路：允许消息重复发布，通过下游状态校验和幂等处理降低重复执行的影响，而不是假设消息绝不会重复。

### 3.2 幂等事件接入

API 使用 `Idempotency-Key` 与事件内容摘要处理重复提交。

| 请求场景 | API 行为 |
|---|---|
| 首次提交 | `202 Accepted`，创建事件与投递 |
| 相同 Key、相同内容 | `200 OK`，复用原记录 |
| 相同 Key、不同内容 | `409 Conflict` |

幂等判断依赖数据库约束和事务处理，避免并发请求通过简单的“先查再写”检查后产生重复记录。

请求摘要包含接收端、事件类型及原始 Payload 字节，因此 JSON 内容相同但原始字节不同的请求不一定被视为同一事件。

### 3.3 Worker 执行状态与 Generation 隔离

投递记录使用状态机表示生命周期：

```text
pending
   |
   v
processing
   |
   +------> succeeded
   |
   +------> retrying ------> processing
   |
   +------> failed
```

Worker 领取任务时，在短事务内检查数据库状态及当前执行版本，登记尝试历史后再发送 HTTP 请求，避免在网络调用期间持续持有数据库事务。

系统引入：

- **Lease**：标识当前任务的执行租约，处理执行超时与重新领取。
- **Generation**：区分人工重放或自动恢复后产生的新执行轮次。
- **Attempt Count**：限制每轮业务投递尝试预算。
- **条件回写**：防止旧执行结果覆盖新一轮投递状态。

即使旧任务在恢复后继续返回结果，也不能直接修改新 Generation 的投递状态。

### 3.4 HTTP 重试与失败重放

Worker 根据 HTTP 结果区分成功、可重试错误与永久错误。

| 结果 | 处理策略 |
|---|---|
| 2xx | 投递成功 |
| 网络错误、超时 | 可重试 |
| 408、429、5xx | 可重试 |
| 3xx、其他 4xx | 视为永久失败 |

重试使用带随机抖动的指数退避，并在可重试场景中解析 `Retry-After`，但实际延迟受配置的最大退避上限约束。

默认每轮最多允许 3 次业务领取；次数耗尽或发生永久失败后进入 `failed`。

系统提供人工 Replay 接口，可对失败投递创建新的执行轮次，并保留原有 Delivery ID 和投递尝试历史。

### 3.5 背压与端点并发隔离

当接入速度超过消费能力时，持续创建任务可能导致积压不断扩大。

HookRelay 使用 `MAX_PENDING_DELIVERIES` 限制非终态投递数量。达到上限后拒绝新的事件接入，并返回 `503` 与 `Retry-After`；已经成功接入的相同幂等请求仍可复用原结果。

Worker 还支持单进程内的端点级并发限制，例如：

```bash
export WORKER_CONCURRENCY=4
export ENDPOINT_CONCURRENCY=1
```

这表示 Worker 最多同时使用 4 个执行槽，且单个 Endpoint 最多持有 1 个执行许可。

当某个端点额度已满时，任务通过延迟替代入队等待，而不是阻塞执行槽；该等待不计入实际 HTTP 尝试预算。

目前端点配额只在单个 Worker 进程内生效，**不提供跨 Worker 实例的全局并发限制，也不保证严格公平调度**。

### 3.6 Reconciler：异常任务自动恢复

Transactional Outbox 主要解决消息尚未确认发布前的可靠性问题。

对于 Outbox 已标记 `published`，但 Redis 任务后来丢失、归档或投递停滞的情况，单纯重扫未发布 Outbox 并不足以完成恢复。

HookRelay 引入独立 Reconciler：

1. 扫描超过宽限期的未完成投递。
2. 检查当前 Generation、Outbox 发布状态及 Redis 任务状态。
3. 对可能丢失的任务进行事务内二次校验。
4. 对符合恢复条件的投递增加 Generation，并写入新的 Outbox。
5. 使用恢复次数上限与投递预算避免无限恢复。

对账过程不会将 Redis 查询失败直接判定为任务丢失，也不会主动重置正常存活的任务。

恢复机制仍存在检测延迟和竞态边界，不能提供严格的 Exactly-Once 执行保证。

### 3.7 HMAC 签名与接收端幂等

HTTP 投递使用 HMAC-SHA256 签名，主要请求头包括：

- `X-Webhook-ID`
- `X-Webhook-Timestamp`
- `X-Webhook-Signature`

接收端验证请求签名、时间戳与原始 Payload，使用恒定时间比较方式校验摘要。

为了处理发送端超时后重复投递，示例 Receiver 使用独立 MySQL 数据库，在同一事务中完成：

```text
Verify Signature
       |
       v
BEGIN TRANSACTION
       |
       +--> Insert Receipt (unique delivery_id)
       |
       +--> Insert Simulated Business Effect
       |
COMMIT
       |
       v
HTTP Response
```

相同 Delivery ID、相同 Payload 的重复请求不会重复插入模拟业务记录。

这里需要明确两个边界：

- HMAC 用于验证消息真实性与完整性，不等于业务幂等，也不提供 Payload 加密。
- Receiver 的事务性去重仅保护当前数据库内的模拟业务效果，不保证外部邮件、支付等副作用恰好执行一次。

## 4. 可靠性验证

项目包含单元测试与依赖真实 MySQL、Redis 的隔离集成测试。

主要验证场景包括：

| 场景 | 重点验证 |
|---|---|
| Outbox 写入失败 | 整个事件接入事务回滚 |
| Redis 发布失败 | Outbox 保留并可重试发布 |
| 入队成功但确认失败 | 重复发布不会直接绕过数据库执行保护 |
| 相同幂等键并发提交 | 不产生重复事件记录 |
| 旧 Generation 结果晚到 | 不覆盖新的投递状态 |
| 失败后人工 Replay | 保留历史并重新建立执行轮次 |
| 端点积压 | 慢端点不占满其他端点可使用的执行槽 |
| 已发布任务丢失 | Reconciler 在满足条件时重新建立任务 |
| Receiver 提交后响应超时 | 重试不重复产生数据库内的模拟业务效果 |

### 典型故障场景：接收端已提交，但响应超时

1. Worker 发送 Webhook 请求。
2. Receiver 完成事务性业务写入。
3. Receiver 延迟响应，Worker 发生 HTTP 超时。
4. Worker 根据重试策略再次发送相同 Delivery ID。
5. Receiver 识别已处理消息，直接返回成功响应。

在隔离测试中，该场景观察到 **两次 HTTP 请求、一次模拟业务写入，最终投递成功**。

这一案例展示了 At-Least-Once 投递与接收端业务幂等之间的关系，而不是宣称网络请求本身恰好发送一次。

## 5. 快速开始

### 环境要求

- Go 1.27.1（仓库当前 `go.mod` 声明的版本）
- Docker 与 Docker Compose
- MySQL 8.4、Redis 7.4（由 Compose 配置启动）
- Bash（用于以下命令和集成测试脚本）

### 5.1 获取代码

```bash
git clone https://github.com/Aureliohehe/go-for-web.git
cd go-for-web/project/hookrelay
```

### 5.2 启动依赖

```bash
cp .env.example .env
```

编辑 `.env`，设置 MySQL 密码、`API_KEY` 等必需配置，不要提交实际密钥。

```bash
docker compose up -d --wait mysql redis
```

默认本地端口：

| 服务 | 地址 |
|---|---|
| API | 127.0.0.1:8080 |
| MySQL | 127.0.0.1:13306 |
| Redis | 127.0.0.1:16379 |
| 示例 Receiver | 127.0.0.1:9090 |

**数据库初始化：** 全新平台数据库需要按顺序应用 `migrations/` 下的六份平台迁移。Receiver 还需要独立数据库和 `migrations/receiver/000001_create_receipts.up.sql`。迁移不会由启动命令自动完成。

已有数据库应核对迁移记录，只执行尚未应用的迁移，不要重复导入。

### 5.3 启动核心服务

在各自终端进入 `project/hookrelay`，加载环境变量：

```bash
set -a
. ./.env
set +a

export REDIS_ADDR="${REDIS_ADDR:-127.0.0.1:16379}"
export ALLOWED_ENDPOINT_URL="${ALLOWED_ENDPOINT_URL:-http://127.0.0.1:9090/webhook}"
```

分别启动：

```bash
go run ./cmd/api
```

```bash
go run ./cmd/dispatcher
```

```bash
go run ./cmd/worker
```

可选启动独立恢复进程：

```bash
go run ./cmd/reconciler
```

如需完整的端到端接收测试，还需配置 Receiver 独立 MySQL 数据库和 `RECEIVER_SIGNING_SECRET`，再启动：

```bash
go run ./cmd/receiver
```

Receiver 的签名密钥来自创建 Endpoint 时的一次性响应；请先完成 Endpoint 创建与密钥配置，再提交测试事件。

### 5.4 创建 Webhook Endpoint

```bash
curl -i http://127.0.0.1:8080/v1/endpoints \
  -H "X-API-Key: $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"url":"http://127.0.0.1:9090/webhook"}'
```

保存响应中的 Endpoint ID 和一次性 `signing_secret`。签名密钥不会通过查询接口再次返回。

### 5.5 提交事件

将 `endpoint_id` 替换为实际创建的 Endpoint ID：

```bash
curl -i http://127.0.0.1:8080/v1/events \
  -H "X-API-Key: $API_KEY" \
  -H "Idempotency-Key: order-42" \
  -H "Content-Type: application/json" \
  -d '{
    "endpoint_id": "1",
    "event_type": "order.created",
    "payload": {"order_id": 42}
  }'
```

成功接入返回事件 ID 与投递 ID。`202 Accepted` 表示事件和 Outbox 已提交，不代表远端 HTTP 投递已经成功。

### 5.6 查询状态与重放

```bash
# 查询投递状态
curl http://127.0.0.1:8080/v1/deliveries/1 \
  -H "X-API-Key: $API_KEY"

# 查询投递尝试历史
curl 'http://127.0.0.1:8080/v1/deliveries/1/attempts?limit=20' \
  -H "X-API-Key: $API_KEY"

# 重放 failed 投递
curl -X POST http://127.0.0.1:8080/v1/deliveries/1/replay \
  -H "X-API-Key: $API_KEY"
```

## 6. API 概览

| Method | Path | 功能 |
|---|---|---|
| GET | `/healthz` | 存活检查 |
| GET | `/readyz` | 数据库就绪检查 |
| POST | `/v1/endpoints` | 创建接收端 |
| GET | `/v1/endpoints/:id` | 查询接收端 |
| PATCH | `/v1/endpoints/:id` | 启用或禁用接收端 |
| POST | `/v1/events` | 幂等创建 Webhook 事件 |
| GET | `/v1/deliveries/:id` | 查询投递状态 |
| GET | `/v1/deliveries/:id/attempts` | 分页查询投递历史 |
| POST | `/v1/deliveries/:id/replay` | 重放失败投递 |

业务管理接口需携带 `X-API-Key`；提交事件还需 `Idempotency-Key`。

## 7. 测试

运行不依赖数据库的测试：

```bash
cd project/hookrelay
go test ./...
```

在仓库根目录执行时，请先进入上述项目目录。

推荐使用隔离集成测试脚本：

```bash
bash scripts/test-integration.sh
```

该脚本使用独立的测试容器、随机本地端口和临时数据库，运行包含竞态检查的相关测试，并在退出时清理本次测试资源。

覆盖范围包括 MySQL 事务、并发幂等、Outbox 故障、异步投递、重试、接收端去重、失败重放和自动恢复等。

集成测试不应直接指向日常开发数据库或已有业务数据。

## 8. 当前限制

HookRelay 当前仍是以可靠性机制验证为主要目标的项目，存在以下限制：

- **不是 Exactly-Once 系统**：任务可能重复入队，HTTP 可能重复发送；外部业务需要自行保证幂等。
- **并发隔离范围有限**：端点并发配额只在单个 Worker 进程内生效，不支持全局公平调度。
- **恢复存在边界**：Reconciler 需要单独运行，依赖宽限期、队列状态检查与恢复预算，不保证立即恢复。
- **安全能力仍需完善**：目前端点密钥以明文存储于开发数据库，尚未提供密钥轮换或加密存储机制；非本地通信应使用 HTTPS。
- **缺少完整性能基准**：尚未建立系统性的吞吐量、P95/P99 延迟和持续高负载测试报告。
- **存储维护机制有限**：历史投递与 Outbox 数据尚无自动清理策略。
- **故障验证尚未覆盖全部场景**：完整 Redis 数据丢失、进程强杀后的端到端恢复时延和多实例故障演练仍待补充。

这些限制也构成了后续优化的主要方向。

## 9. 后续计划

- [ ] 增加投递吞吐量、成功率、队列积压及恢复次数等运行指标
- [ ] 建立可复现的并发压测，记录吞吐量与 P95/P99 延迟
- [ ] 验证 Worker 强杀、Redis 数据丢失及多实例故障场景
- [ ] 优化积压容量检查中的数据库锁竞争与查询开销
- [ ] 设计历史数据保留、清理及密钥管理机制
- [ ] 探索跨 Worker 的全局端点并发限制

## 10. 项目结构与相关文档

核心代码位于 [`project/hookrelay`](./project/hookrelay)。

主要执行入口：

```text
project/hookrelay/
├── cmd/
│   ├── api/
│   ├── dispatcher/
│   ├── worker/
│   ├── reconciler/
│   └── receiver/
├── internal/
├── migrations/
├── scripts/
├── tests/
├── docs/
├── compose.yaml
└── go.mod
```

详细的环境恢复、数据库迁移和技术探索记录保留在项目的 `docs/` 文档中。

设计计划与可靠性边界参见 [`PLAN.md`](./project/hookrelay/PLAN.md)。

---


