# 学习进度与下次起点

> 已完成接收端创建→查询及 PATCH 启用/禁用的真实 HTTP + MySQL 验证（用户反馈通过）。
> 当前已打通 Redis/Asynq 异步投递、有限重试及状态回写；助手已在隔离临时 MySQL/Redis 上通过真实数据库与队列测试。下一步是 Outbox，当前仍有落库后入队前的故障窗口。

## 辅导方式与协作约定

- 最新偏好：**助手写代码，用户理解逻辑；加快进度，专注核心逻辑，不写过多防御性代码**。讲解按请求链路展开，不要默认把实现任务布置给用户。
- 用户要求助手贴出关键代码、按执行顺序讲解；在继续开发时沿用此讲解方式，不默认让用户写代码。
- 可以直接维护进度文档。未明确要求时，不自动提交、推送或部署，不自动停止用户的服务。
- 不读取、记录或输出真实密码、API Key、DSN。`.env` 保持本地私有。
- 最初的 `cmd/api`、`internal/config`、`internal/handler`、`internal/store` 来自助手参考骨架。本轮用户已在解释后实际接入和扩展，不能据此声称全部代码是用户独立设计的。

## 第一阶段回顾（此前已完成）

以下实验此前由用户执行并反馈成功：

- 理解 Go module、包导入路径、项目工具链；当时检查的 Go 版本为 `go1.27.1 linux/amd64`。
- 创建 `.env`，通过 `set -a; . ./.env; set +a` 导出环境变量。
- 理解 Go 不自动读取 `.env`，临时命令赋值与终端环境的区别，修改文件后需要重新加载。
- Docker Compose 与 MySQL 启动、健康检查、账号登录、SELECT 1；本机 `13306` 映射容器 `3306`。
- 编写 `cmd/envcheck`、`cmd/dbcheck`、最初的 `cmd/healthcheck`。
- 理解 `sql.Open` 创建连接池不代表连接成功，`PingContext` 才实际检查连接。
- 验证数据库正常/停止/恢复时，`/healthz` 始终 200，`/readyz` 为 200/503/200，无需重启 Go 服务。
- 理解连接池重新建立连接不等于失败的业务写入可以自动安全重试。

## 本轮完成内容

### 1. 配置与包拆分

从 `cmd/healthcheck/main.go` 中的环境变量读取开始：

- 定义 `DBConfig`，填充配置，提取 `loadDBConfig()`。
- 增加错误返回、必填密码校验、非敏感配置默认值和端口范围检查。
- 编写缺少密码、正常配置、默认值、表驱动端口测试。
- 迁移到已有 `config.Load()`；测试移至 `internal/config/db_config_test.go`，使用外部测试包 `config_test`。
- 修复迁移后的测试：预期结构体必须包含 `HTTPAddr`；用 `t.Setenv` 隔离终端环境。
- 接入 `handler.NewRouter(pool.PingContext)` 和 `store.OpenMySQL`，main 只负责组装。
- 理解函数依赖注入、方法值与方法调用、闭包、`t.Run`、`t.Helper`、`t.Cleanup`。

### 2. HTTP、数据库初始化与退出

- handler 测试覆盖健康检查、未知路径 404，以及同一个 router 上数据库失败/恢复/再次失败。
- store 初始化包含连接池参数、带超时的启动 Ping、GORM 复用同一连接池。
- 入口采用 `run()` 返回错误，避免 `log.Fatal` 跳过函数内的 defer。
- 用户反馈真实 MySQL 集成测试、启动时数据库不可用则退出、恢复后两个健康接口均成功。
- 加入 `signal.NotifyContext`、`http.Server`、HTTP 超时配置和 Shutdown；用户反馈 Ctrl+C、SIGTERM 实验完成。
- 退出时使用独立 context 等待最多 5 秒，Shutdown 失败则 Close，随后关闭连接池。
- 用户授权助手提取 `cmd/healthcheck` 的 `shutdownHTTP()`，并编写真实本地 HTTP 连接的在途请求测试。
- 该测试证明请求被阻塞时关闭等待，释放请求后客户端收到完整响应、关闭成功；竞争检测重复 10 次通过。
- **范围限制：** 这个关闭测试针对练习入口的共享关闭函数，不是对 `cmd/api` 的进程级测试；强制关闭超时分支尚未覆盖。

### 3. endpoints 表与数据库读写

迁移文件：`migrations/000001_create_endpoints.up.sql`。

字段与约定：

- `id`：BIGINT UNSIGNED 自增主键。
- `url`：VARCHAR(2048)，不设唯一约束。
- `signing_secret`：64 字符 ASCII，数据库 CHECK 限制长度；未来用 crypto/rand 生成 32 字节再十六进制编码。
- `enabled`：默认 1，CHECK 限制为 0/1。
- `created_at`：DATETIME(6)，按 UTC 约定写入。
- InnoDB；第一版不修改 URL/签名密钥，不提供删除接口，之后支持启用/禁用。

用户反馈已执行迁移，并完成结构检查、事务插入后回滚、非法 enabled 被拒绝实验。助手检查了 SQL 文件，但未在本轮工具调用中独立重跑迁移或查询数据库。

代码：

- `internal/store/endpoint.go`：`Endpoint`、`CreateEndpoint`、`FindEndpoint`。
- `SigningSecret` 使用 `json:"-"`，但这不能防止日志打印整个结构体。
- `Enabled` 不配置 GORM `default:true`，确保显式 false 可正确保存。
- `ErrEndpointNotFound` 将 GORM 的记录不存在错误转换为 store 层错误；其他错误使用 `%w` 包装。
- `internal/store/endpoint_test.go`：真实 MySQL 下创建/读取 enabled 与 disabled 记录，检查 ID 回填和创建时间，再删除本次插入的记录并检查不存在错误。
- 测试在事务内执行并回滚，不清空业务表；仍可能消耗自增 ID。
- 用户反馈真实数据库测试成功。迁移目前手动执行，尚无迁移版本追踪工具，也不使用 AutoMigrate。

### 4. API Key 与接收端查询

业务入口已切换为 **`cmd/api`**，`cmd/healthcheck` 保留练习用途。

- `config.Config.APIKey` 读取 `API_KEY`，不设置默认密钥。
- 通用 `config.Load()` 不要求 API Key；`cmd/api` 在连接数据库前要求它非空。
- `.env.example` 增加空 `API_KEY=` 提示；实际本地密钥是否已持久保存到 `.env` 未确认，不要假定存在。
- `RequireAPIKey` 使用 `X-API-Key`，配置为空、请求头缺失、错误密钥均拒绝；用 `AbortWithStatusJSON` 阻止业务执行。
- API Key 用于调用管理 API，与每个接收端的签名密钥不同。
- `NewAPIRouter` 在健康路由之外创建受鉴权保护的 `/v1` 路由组。
- `GET /v1/endpoints/:id`：非法 ID 返回 400，不存在 404，数据库错误 500，成功 200。
- 正常响应只包含 `id`、`url`、`enabled`、`created_at`，其中 ID 为字符串，避免客户端大整数精度损失。
- `EndpointFinder` 函数类型用于注入查询依赖；请求 context 传给 store。
- `cmd/api` 已实际组装 GORM 查询闭包，不只是独立测试 handler。

### 5. 助手最后补充的测试

用户明确授权助手完成 `internal/handler/endpoint_test.go`，替换原来的空占位测试。

共 16 个子场景：

- 缺少/错误 API Key，且未鉴权时先于 ID 校验返回 401。
- 非数字、0、负数、uint64 溢出 ID 返回 400，均不执行查询。
- 正常记录、禁用记录可查询；最大 uint64 ID 以精确字符串返回。
- 直接/包装后的记录不存在错误返回 404；普通数据库错误返回 500。
- 检查查询调用次数、传入 ID、请求 context 传递、JSON Content-Type、精确响应字段。
- 响应不泄露签名密钥、API Key 或数据库错误详情。
- 业务 router 上 `/healthz`、`/readyz` 无需鉴权；覆盖数据库不可用情况，健康路由不查询 endpoints。

最近助手实际运行并通过：

```bash
go test ./...
go vet ./...
go test -race ./internal/handler
git diff --check
```

这些命令未启用真实数据库集成测试，不应描述为助手重新验证了数据库。最新查询接口的真实 HTTP + 数据库端到端调用尚未单独确认；可能没有已提交的接收端数据，因为前面的数据库测试会回滚。

## 当前主要文件

| 文件 | 用途 |
| --- | --- |
| `cmd/api/main.go` | 当前业务入口，API Key 校验、连接池、业务路由、退出 |
| `cmd/healthcheck/main.go`、`main_test.go` | 已完成的练习入口与关闭测试 |
| `internal/config/config.go` 及测试 | 通用环境配置与校验 |
| `internal/handler/router.go` | 健康路由与受保护的 `/v1` 路由组 |
| `internal/handler/auth.go`、`auth_test.go` | API Key 中间件 |
| `internal/handler/endpoint.go`、`endpoint_test.go` | 查询接收端及 16 场景测试 |
| `internal/store/mysql.go`、`mysql_test.go` | 连接池/GORM 初始化与测试 |
| `internal/store/endpoint.go`、`endpoint_test.go` | 接收端模型、创建/查询及集成测试 |
| `migrations/000001_create_endpoints.up.sql` | 第一份手动迁移 |

`docs/day-01.md` 和 `PLAN.md` 中部分“当前起点”属于初始阶段历史描述；最新状态以本文为准。不要按照旧文档重新排查已解决的 Docker 阻塞或重复拆分配置。

## 创建接口实现与下次起点

用户明确改为“助手写代码、用户理解逻辑”。本次助手实现 `POST /v1/endpoints`：

- `ALLOWED_ENDPOINT_URL` 为单个完整 URL；配置加载时校验 http(s) URL 的格式，`cmd/api` 在连接数据库前要求非空。需自行在本地配置环境变量，不能假定 `.env` 已更新。
- 路由在受 `X-API-Key` 保护的 `/v1` 组。只接受 `application/json`、最大 4096 字节；拒绝未知字段、多段 JSON、超长请求与非精确匹配 URL。允许 URL 是操作员配置的值，不是开放公网 URL 的许可；仍非可公开部署的 SSRF 防护方案。
- `crypto/rand` 生成 32 字节签名密钥，hex 编码为 64 字符；服务端控制 enabled=true；存库成功后返回 201、字符串 ID、URL、enabled、created_at 和一次性交付的 `signing_secret`。GET 不返回密钥；数据库错误不返回给客户端。
- 注入 `EndpointCreator` 调用 store 层；新增 handler 测试，更新现有查询测试、配置测试和启动配置测试。
- `cmd/api/endpoint_integration_test.go` 使用真实本地 HTTP 与 MySQL，在事务中验证创建→查询并回滚。首次因响应字段不一致失败；在 `store.CreateEndpoint` 预设 UTC 微秒时间以匹配 MySQL `DATETIME(6)` 后，**用户反馈重跑成功**。不要把这说成助手自行运行了真实数据库测试。

## 新增 PATCH 启用/禁用（当前待验证）

- `PATCH /v1/endpoints/:id`：受 API Key 鉴权，请求只接收 `{"enabled":true/false}`，复用 POST 的 JSON 格式/大小限制；缺省和 null 值被拒绝，成功返回公开字段且不泄露签名密钥。非法 ID 400、不存在 404、其他数据库错误 500。
- `store.SetEndpointEnabled` 显式 `Update("enabled", enabled)`（false 也会写入），随后查询记录；重复设置相同值也算成功，不依赖 MySQL 对未改变记录的 RowsAffected 行为。
- 已增加 handler 测试、store 集成测试，并扩展 HTTP + MySQL 集成测试来覆盖禁用、重复禁用、启用及密钥不变；测试在事务内回滚，但会消耗自增 ID。**用户反馈 PATCH 的真实数据库集成测试已通过**；不是助手自行运行。
- 这只是配置状态变更：事件接收、投递前禁用检查尚未实现，不能声称禁用能阻止实际投递。

## 事件接入：当前起点

- 新增 `migrations/000002_create_events_deliveries.up.sql`，基于已存在的 endpoints 表创建 events 与 deliveries；`idempotency_key` 为字节精确的 VARBINARY(128) 全局唯一，`request_hash` 为 32 字节摘要，原始 payload 以 MEDIUMBLOB 保存，event_id 在 deliveries 中唯一。没有执行迁移、没有 AutoMigrate 或重置表；MySQL DDL 不是多表原子事务，手动应用前要确认数据库现状，失败时先检查是否部分创建而非盲目重跑。
- 新增 `internal/store/event_hash.go`：SHA-256 摘要输入为端点 ID、带长度前缀的事件类型、带长度前缀的原始 payload 字节；幂等键不参与摘要，只负责查重定位。同一键同一内容应返回原 ID，同键不同内容将来返回 409；不同 JSON 空白会视为不同内容。单元测试覆盖输入变化和字段边界。
- 用户确认已手动应用 `000002` 迁移，两张表均创建成功；助手未独立检查数据库，不要再次应用迁移。
- 新增 `internal/store/event.go` 的 `CreateOrGetEventDelivery`：验证输入，先按幂等键查已提交事件，相同摘要返回原事件/投递 ID，不同摘要返回冲突；新事件在事务中锁定端点、检查 enabled，创建 event 与 pending delivery；唯一冲突时退出失败事务后重新读取已提交的赢家。端点禁用拒绝新事件，但已接受的事件重试可复用 ID。并发调用时应传入连接池上的 GORM db，不要共用外层长事务，否则冲突后的读取可能看不到并发提交。
- 新增 `internal/store/event_test.go`：非法输入（无需 DB）、真实 MySQL 下正常创建、同键复用与冲突、禁用端点、注入第二次写入失败验证事件回滚、并发八请求仅一条投递。并发测试需要各自独立连接，不能包在共享外层事务中；生成唯一测试键，正常结束时只清理自己的事件/投递及端点，可能消耗自增 ID。进程崩溃可能留下测试记录，切勿清空业务表。
- `go test ./...`、`go vet ./...`、`go test -race ./internal/store`、`git diff --check` 通过；**真实 MySQL 事件集成测试尚未由助手或用户验证**。下一步请用户运行 `HOOKRELAY_INTEGRATION=1 go test -count=1 -v ./internal/store`，通过后再接 HTTP 事件接口。
- 当前尚无事件接口、Redis/Asynq、Outbox、投递、重试或签名发送功能，不能宣称可靠投递已实现。

## 本轮续接：事件 HTTP 接口

- 新增 `internal/handler/event_create.go`，在鉴权路由组注册 `POST /v1/events`，由 `cmd/api/main.go` 注入现有 `CreateOrGetEventDelivery`，不添加 service 层或新事务逻辑。
- 输入为字符串 `endpoint_id`、`event_type`、`payload`；`Idempotency-Key` 从请求头读取。使用 `json.RawMessage` 保留 payload 原始字节，复用现有 JSON 解码器并参数化大小限制。
- 首次接收返回 202，同内容复用返回 200；响应含字符串 `event_id`、`delivery_id` 和 `reused`。非法输入 400、端点不存在 404、禁用或幂等冲突 409、数据库错误 500。
- 新增核心 handler 测试：参数/context 传递、原始 payload、首次与重复响应、鉴权、格式与大小限制、业务错误映射。`go test ./...` 和 `go vet ./...` 已通过，未启用真实 MySQL 集成测试。
- 没有修改原有 `internal/store/event.go` 的用户未提交改动，也没有执行迁移、读取 `.env` 或启动/停止服务。
- 下一步先验证现有事件数据库测试及 README 中 HTTP 请求，再补投递查询和 Sender，随后进入 Asynq。当前仍不发送 Webhook。

## 本轮续接：投递查询与 Sender

- `internal/store/delivery.go` + `internal/handler/delivery.go`：新增受鉴权保护的 `GET /v1/deliveries/:id`，返回字符串 ID、状态、generation、attempt_count 及时间。复用现有 deliveries 表，无新迁移。
- `internal/delivery/sender.go`：独立单次 HTTP POST，原样发送 payload 和 `X-Webhook-ID`；2xx 成功，网络/超时/408/429/5xx 可重试，3xx 不跟随，其余失败不重试。设置客户端超时、传递 context、最多读取 4 KiB 响应体，不持久化响应正文。
- Sender 只分类、不实际重试，也不读写数据库。尚无 HMAC 或 Retry-After；只供受控本地测试，不作为公网投递能力。
- 新增查询 handler 测试与 Sender 本地 HTTP 测试（状态分类、原始请求体、禁止重定向、超时、取消、网络失败）；现有 MySQL 事件集成测试增加 FindDelivery 的初始状态和不存在检查，尚未运行真实数据库测试。
- `go test ./...`、`go vet ./...`、`go test -race ./internal/handler ./internal/delivery` 通过。未读取 `.env`、未执行迁移、未启动/停止用户服务。
- **下一步：Redis/Asynq + 独立 Worker，接通已保存事件到 Sender 的异步链路。** 先明确直接入队存在故障窗口，随后接 Outbox。当前查询到的投递仍保持 pending，不能宣称已有自动投递。

## 最新进度：Asynq 异步闭环

- 增加 Asynq v0.25.1、`REDIS_ADDR`（默认 `127.0.0.1:16379`）与 Compose Redis 7.4/AOF 配置；本轮无新 SQL 迁移。
- `internal/service/event.go`：先复用存储层事务，再入队；幂等重试也尝试补入队。失败返回 503 + `persisted=true` + 原 ID，禁止误导为“没有保存”。稳定 TaskID 冲突视为已入队。
- `internal/task/delivery.go`：任务 `webhook:deliver`、队列 `webhooks`，只传 delivery_id/generation；当前固定 generation=1，尚无重放。默认最多 3 次领取，HTTP 5s、任务 20s、租约 30s。
- `internal/store/delivery_work.go`：短事务锁定投递、跳过终态/旧版本、检查端点、领取并增加次数；回写以 generation+attempt_count 限制旧结果。计数是领取次数，崩溃在 HTTP 前也消耗预算。
- `internal/delivery/worker.go` + `cmd/worker`：独立 Worker 并发 1，复查允许 URL，执行 Sender；临时失败交给 Asynq 延迟重试，永久失败/耗尽预算置 failed；日志仅含 ID、状态、状态码、耗时，不存原始 HTTP 错误详情。
- `cmd/receiver`：本地测试服务，`-status 500`、`-delay 8s` 可模拟错误；没有验签和持久化去重。
- **助手实际验证**：`go test ./...`、`go vet ./...`、相关包 race 测试；独立临时 MySQL 8.0.42 和 Redis 上执行 `HOOKRELAY_INTEGRATION=1 go test -count=1 -race ./internal/store ./cmd/api ./tests/integration` 通过（含事件事务回滚、并发幂等、租约/执行令牌及真实队列成功/500后成功/400失败/503耗尽预算）。
- 另构建并启动 API、Worker、Receiver 三个进程，真实 HTTP 验证首次 202、重复 200、最终 succeeded/attempt_count=1、同键改内容 409。测试结束已停止本轮创建的临时进程；没有读取用户 `.env`、操作已有数据库或用户服务。Compose Redis 路径及 Worker 强杀恢复尚未验证。
- **下一步：Outbox + Dispatcher**，替换 API 的直接入队，解决保存成功后未入队的窗口。Redis 丢数据、数据库写回长期失败导致非终态遗留仍需后续对账；不能宣称已可靠投递。第一版仅运行单 Worker，尚未做多实例故障测试。

## 下次恢复环境

在项目根目录执行：

```bash
cd /root/hello-gp/project/hookrelay
docker compose up -d --wait mysql redis

# 只加载自己可信的本地配置文件。
set -a
. ./.env
set +a

# 若没有持久保存 API_KEY，可只为当前终端生成一个新的开发密钥。
if [ -z "${API_KEY:-}" ]; then
  export API_KEY="$(openssl rand -hex 32)"
fi
export ALLOWED_ENDPOINT_URL="${ALLOWED_ENDPOINT_URL:-http://127.0.0.1:9090/webhook}"
export REDIS_ADDR="${REDIS_ADDR:-127.0.0.1:16379}"

go run ./cmd/api
# 另外两个终端（Worker 也需加载上述环境）：
# go run ./cmd/worker
# go run ./cmd/receiver
```

- 临时生成的密钥不会自动写入 `.env`；另一个终端也不会自动继承它。不在对话中粘贴密钥。
- `cmd/api` 还要求 `ALLOWED_ENDPOINT_URL`（例如本地开发用 `http://127.0.0.1:9090/webhook`）；不要把真实密钥贴到对话中。
- `cmd/api` 和 `cmd/healthcheck` 默认都监听 8080，不要同时启动。
- 先用 `/healthz`、`/readyz` 检查服务；若 HTTP_ADDR 已修改，使用实际地址。
- 不要重复执行已应用的 CREATE TABLE。若数据库卷改变或表缺失，先确认数据库和迁移状态，不通过删表或 AutoMigrate 绕过问题。
- 若执行集成测试，需数据库运行、迁移已应用，且当前终端已导出数据库配置：

```bash
HOOKRELAY_INTEGRATION=1 go test -count=1 -v ./internal/store ./cmd/api ./tests/integration
```

## 收尾与保存边界

- 默认保留代码和进度在工作区，不自动提交、推送或部署，不替用户停止服务。
- 收尾时 Git 仍有业务代码修改和新增文件；下次先检查 `git status --short`，不要覆盖或清理这些改动。用户可能在两次会话间自行提交。
- 用户可以 Ctrl+C 停止 API，再执行 `docker compose stop mysql` 保留数据。
- 不执行 `docker compose down -v`；它会删除数据库数据卷。
- 当前进程/容器是否已经停止未确认，下次按实际状态检查。
