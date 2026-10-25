# 学习进度与下次起点

> 已完成接收端创建→查询及 PATCH 启用/禁用的真实 HTTP + MySQL 验证（用户反馈通过）。
> 当前已实现 Outbox、背压、尝试历史、失败重放、HTTP 重试、HMAC 及接收端事务性持久化幂等。Docker 本轮已恢复，重试/签名/超时重投已通过隔离 MySQL 8.4/Redis 7.4 验证。平台五份迁移勿重复导入；**新增接收端迁移 `migrations/receiver/000001_create_receipts.up.sql` 只应用到独立接收端数据库，目前仅在隔离测试库执行，用户环境待配置。** Receiver 需要 `RECEIVER_SIGNING_SECRET` 和 `RECEIVER_MYSQL_*`。单 Worker 端点并发限制与慢端点隔离也已实现并验证；默认总并发 4、每端点 1。下一步 Worker 强杀恢复验证。

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

## 此前进度：Asynq 异步闭环

- 增加 Asynq v0.25.1、`REDIS_ADDR`（默认 `127.0.0.1:16379`）与 Compose Redis 7.4/AOF 配置；本轮无新 SQL 迁移。
- `internal/service/event.go`：先复用存储层事务，再入队；幂等重试也尝试补入队。失败返回 503 + `persisted=true` + 原 ID，禁止误导为“没有保存”。稳定 TaskID 冲突视为已入队。
- `internal/task/delivery.go`：任务 `webhook:deliver`、队列 `webhooks`，只传 delivery_id/generation；当前固定 generation=1，尚无重放。默认最多 3 次领取，HTTP 5s、任务 20s、租约 30s。
- `internal/store/delivery_work.go`：短事务锁定投递、跳过终态/旧版本、检查端点、领取并增加次数；回写以 generation+attempt_count 限制旧结果。计数是领取次数，崩溃在 HTTP 前也消耗预算。
- `internal/delivery/worker.go` + `cmd/worker`：独立 Worker 并发 1，复查允许 URL，执行 Sender；临时失败交给 Asynq 延迟重试，永久失败/耗尽预算置 failed；日志仅含 ID、状态、状态码、耗时，不存原始 HTTP 错误详情。
- `cmd/receiver`：本地测试服务，`-status 500`、`-delay 8s` 可模拟错误；没有验签和持久化去重。
- **助手实际验证**：`go test ./...`、`go vet ./...`、相关包 race 测试；独立临时 MySQL 8.0.42 和 Redis 上执行 `HOOKRELAY_INTEGRATION=1 go test -count=1 -race ./internal/store ./cmd/api ./tests/integration` 通过（含事件事务回滚、并发幂等、租约/执行令牌及真实队列成功/500后成功/400失败/503耗尽预算）。
- 另构建并启动 API、Worker、Receiver 三个进程，真实 HTTP 验证首次 202、重复 200、最终 succeeded/attempt_count=1、同键改内容 409。测试结束已停止本轮创建的临时进程；没有读取用户 `.env`、操作已有数据库或用户服务。Compose Redis 路径及 Worker 强杀恢复尚未验证。
- **下一步：Outbox + Dispatcher**，替换 API 的直接入队，解决保存成功后未入队的窗口。Redis 丢数据、数据库写回长期失败导致非终态遗留仍需后续对账；不能宣称已可靠投递。第一版仅运行单 Worker，尚未做多实例故障测试。

## 此前进度：Outbox + Dispatcher

- 用户已提交上一轮异步闭环；本轮从干净工作区继续，未自动提交。
- 新增 `migrations/000003_create_outbox.up.sql`：Outbox、轮次唯一约束、扫描索引，并为已有非终态投递回填记录。**仅在隔离测试库应用过，用户数据库待手动迁移**；升级前停止旧 API/Worker，勿重复执行旧迁移；DDL/回填非原子，失败先检查。
- `internal/store/event.go`：events、deliveries、outbox 同事务写入；幂等命中不新增 Outbox，新增测试验证第三次写入失败时前两次回滚，以及并发重复只有一条 Outbox。
- 新增 `internal/store/outbox.go`：短事务领取、过期租约恢复、条件发布/重试。available_at 统一表示重试时间或 processing 租约到期；publish_attempts 作为令牌防止旧发布者回写。
- 新增 `internal/outbox/dispatcher.go`、`cmd/dispatcher`：独立单 Dispatcher 发布，租约 30s、Redis 超时 5s，空闲轮询 1s，入队失败退避 5/10/20/40/60s；入队成功但标记失败保留租约，后续允许重复发布。
- `internal/task/delivery.go` 改为接收真实 generation，稳定 TaskID 为 delivery-ID-generation。API 已去掉 Redis 客户端，删去不再需要的 `internal/service/event.go` 和旧测试；去掉“已保存但队列失败”503。现在 202 只表示三表事务已提交。
- **本轮实际验证**：单测、vet、race；临时 MySQL 8.0.42 + 独立 Docker Redis 7.4 下，三表事务、回填旧非终态投递、重试/归档、租约失效后新 Dispatcher 恢复、旧令牌不能覆盖、入队成功但确认失败导致真正重复入队均通过。
- 四进程演示通过：Redis 未运行时 API 首次 202/重复 200，Dispatcher 记录失败并退避；Redis 恢复后自动发布、Worker succeeded，客户端没有再次提交。
- 测试中发现宿主 Redis 5 不支持 Asynq 归档 Lua 所需命令；此前仅检查数据库 failed 不足以证明队列归档成功。已增加队列终态断言，改用 Redis 7.4 重跑通过。不要使用宿主旧 Redis 5。
- Outbox 集成测试新增显式开关 `HOOKRELAY_OUTBOX_INTEGRATION=1`，**必须独立测试数据库且 Outbox 为空，无业务进程运行**（扫描器会全局领取）；不要在日常开发库上运行或清空业务表。普通存储层集成测试仍使用原开关。
- 未读取 `.env`、未操作用户已有服务。临时进程和独立 Redis 容器已停止/移除；临时测试日志位于 `/tmp/hookrelay-outbox7-TRMJW8`。未执行项目 Compose 启动路径。
- 当前不包含积压背压、Outbox 清理、已发布后 Redis 丢数据对账或接收方幂等；重复发布只验证已成功投递跳过，不宣称外部副作用恰好一次。多 Dispatcher 仍未做故障验证。

## 此前进度：积压容量与背压

- 用户已提交上一轮 Outbox；本轮从干净工作区继续，无自动提交。
- 新增 `000004_create_admission_lock.up.sql`：一行数据库接入锁，不维护容量计数器。用户先反馈 hookrelay 仅有 endpoints/events/deliveries，随后按指导导入 `000003` 和 `000004` 并反馈完成；之后助手受托只读确认两张新表和锁行 id=1，不要重复导入。
- `MAX_PENDING_DELIVERIES` 默认 10000，正整数，所有 API 实例保持一致。上限统计 pending/processing/retrying 的投递总数，而非 Outbox 数量；仅发布到 Redis 不释放容量，成功/最终失败才释放。
- `internal/store/admission.go` + `event.go`：新事件事务先锁接入行，锁后重查幂等，检查端点与当前积压，最后三表同事务保存。事务采用 READ COMMITTED；序列化容量检查和新投递写入，避免并发超额。幂等重试不占新名额，失败回滚不泄漏容量。
- Handler 满载返回 `503`、`Retry-After: 5`、`{"error":"backlog_full","persisted":false}`。这是明确未创建记录的拒绝，与此前已移除的“保存后入队失败”503 语义不同。
- **实际测试通过**：单测/vet/全包 race；临时 MySQL 8.0.42 的容量测试连续 3 次通过，覆盖跨连接池/端点最后一格竞争、相同键并发、满载冲突、processing/retrying 计数、终态释放以及第三次写入失败回滚；Outbox/Redis 7.4 集成测试回归通过。
- 另启动两个独立 API 进程，配置上限 1，8 个不同键并发请求得到 1 个 202、7 个 503；SQL 确认仅一组三表记录，同键重试 200、冲突 409；测试库中模拟终态后，原先拒绝的键返回 202。未读取 `.env`、未改用户数据库/已有服务，临时进程和 Redis 容器已清理。日志 `/tmp/hookrelay-admission-r7XKyi`。
- 新增 `HOOKRELAY_ADMISSION_INTEGRATION=1`：仅在已迁移的独立空测试库、无其他业务进程时运行容量集成测试，不得清空业务表满足条件。
- 代价与边界：新事件接入串行化并逐次计数，尚未压测；上限不限制历史数据总量。未来 failed→pending 的重放必须走同一接入锁/容量检查。下一步实现投递尝试历史，再接失败重放。

## 用户委托的完整背压验证（已通过）

- 只读查询用户 Compose MySQL：数据库 hookrelay，outbox/admission_locks 存在，锁行 id=1，查询时 unfinished=0；没有修改用户业务数据或启停用户进程。
- 新建隔离 MySQL/Redis 7.4，运行单测、vet、真实存储/队列 race 测试；容量集成测试连续三次通过。
- 两个独立 API、容量 1、8 个并发不同键：1 个 202、7 个 503 + Retry-After:5 + persisted:false；SQL 确认仅一组三表记录。满载同键重试 200、内容冲突 409、readyz 仍为 200。
- 随后启动真实 Receiver/Worker/Dispatcher，第一条投递 succeeded、attempt_count=1；原先拒绝的键无需修改即返回 202，随后也 succeeded。**本次不是直接修改 SQL 状态模拟释放容量。**
- 临时进程与专用 Redis 容器已清理，日志 `/tmp/hookrelay-backpressure-e2e-7QC19h`；生产代码无新增修改。下一步进入投递尝试历史与失败重放。

## 此前进度：投递尝试历史

- 本轮沿用未提交的背压代码，未覆盖/清理用户改动，也未自动提交。
- 新增 `000005_create_delivery_attempts.up.sql`：delivery_id/generation/attempt_number 唯一，按 delivery_id/id 建分页索引；只记录新执行，不补造已有次数对应的历史。**用户随后授权助手导入：已确认旧应用进程未运行，在 Compose MySQL 的 hookrelay 中执行成功；SHOW CREATE TABLE 验证结构、唯一约束、索引和外键，历史记录为 0，总计 6 张表。** 未修改已有业务记录，未启动业务进程，MySQL/Redis 保持原运行状态。
- `internal/store/delivery_work.go`：Claim 在原领取事务中登记 processing 尝试；Finish 在同一事务回写投递和尝试，保存 finished_at/http_status/error_class/latency_ms。已完成历史不变；晚到结果可补全自己的未完成历史，但不能覆盖新 generation/attempt_count 的投递状态。
- `internal/store/delivery_attempt.go` + `internal/handler/delivery_attempts.go`：`GET /v1/deliveries/:id/attempts?limit=20&after=0`，需 API Key，默认 20 条、最多 100；响应 items 和 next_after（空字符串表示当前无下一页），IDs 用字符串。不存在投递 404，未执行过为空数组。
- `internal/delivery/worker.go` 传递 Sender 耗时并区分 http_error、network_error、timeout、canceled；不保存原始 URL/payload/错误详情。URL 不允许的领取保留失败历史，但无 Sender 耗时。
- 边界：历史登记发生在 HTTP 前，因此不是远端精确接收次数；finished_at=null 只表示结果未保存，不能推断未发送。领取前因禁用/预算耗尽而跳过，不伪造尝试；领取后崩溃的记录保留未完成状态。
- **实际验证通过**：go test/vet/全包 race；临时 MySQL 8.0.42 + Redis 7.4 下登记失败回滚领取、结果历史缺失回滚状态、重复结果不改旧历史、晚到结果隔离、未知结果保留、分页、背压与 Outbox 回归。历史存储测试连续 3 次通过；真实队列测试增加经本地 HTTP API 查询分页的断言。
- 临时服务已清理，测试日志 `/tmp/hookrelay-history-cU2d99`。未读取 `.env`、未执行用户数据库迁移、未操作已有服务。
- 下一步失败重放：容量接入锁→只允许 failed→generation+1→重置本轮预算→新 Outbox 同事务，保持 delivery_id，不删除历史。

## 此前进度：失败重放

- 新增 `internal/store/delivery_replay.go`、`internal/handler/delivery_replay.go`，接入 `POST /v1/deliveries/:id/replay`，API Key 鉴权，无请求体。本轮无迁移，用户库已有 000001..000005 不要重复执行。
- 同事务：接入锁→投递行锁→只允许 failed→检查端点 enabled 与容量→generation+1、重置本轮 attempt_count 和最后诊断/租约→写新 Outbox。不创建新 event/delivery，不删除历史，不直接访问 Redis。
- 成功 202 返回原 delivery_id/event_id、新 generation 和 pending；不存在 404，非 failed/端点禁用 409；容量不足 503 + Retry-After:5 + replayed:false，不改变原记录。
- 复用 generation 防止旧任务再次执行/覆盖新状态；旧未完成 HTTP 结果可补全自己的历史。每轮计数从 0 开始、最多 3 次领取，历史按 generation 分轮保留。Worker 日志新增 generation。
- 重放不是幂等键接口：同一失败轮次并发请求只接受一次，但新一轮再次 failed 后又 POST 会再开启一轮。响应不确定时先查状态/generation，不盲目重复。稳定 delivery_id 不代表远端恰好一次，接收方幂等仍待实现。
- **实际验证**：单测/vet/全包 race 通过；隔离 MySQL 8.0.42 + Redis 7.4 上重放事务回滚、八请求并发只创建一轮、禁用/非终态拒绝、新事件与重放共享最后一个容量名额、旧任务/旧结果隔离均通过。重放与容量测试连续三次通过。
- 真实 HTTP + 队列测试：接收端第一次 400 → 原任务归档 → POST replay 返回 generation=2 → 人工注入旧 generation 队列任务被跳过 → 新 Outbox 发布 → 接收端 204 → 同一 delivery_id succeeded，历史同时保留两轮。重复 pending/succeeded 重放返回 409。
- 未读取 `.env`、未修改用户数据库或启停用户已有服务；临时实例已清理，日志 `/tmp/hookrelay-replay-XhdMy6`。保留此前未提交的迁移确认文档改动，无自动提交/推送。
- 下一步补接收方 Retry-After、有上限的指数退避和抖动，再进入 HMAC 与接收方幂等。

## 此前进度：HTTP 重试策略

- 接续工作区已有的未提交重试改动，没有覆盖其他改动、自动提交或操作用户服务。本轮未读取 `.env`，未操作业务数据库，无新迁移。
- `internal/delivery/retry.go`：`RetryPolicy.Delay` 接入 Asynq，指数上界为 `min(base × 2^n, max)`（n 从 0 起），在上界的一半到全部之间抖动。配置 `RETRY_BASE_DELAY=5s`、`RETRY_MAX_DELAY=1m`，校验 `0 < base <= max`；每轮 3 次领取预算不变。
- 顺序：Sender 分类并提取可重试响应的 Retry-After → Worker 保存结果并返回带内存元数据的错误 → Asynq 调用策略计算延迟。秒数和 HTTP 日期可延长抖动结果，但不超过 max；非法/过期值回退普通退避，超大合法整数截断而不溢出。重复响应头忽略，不按逗号拆分（HTTP 日期本身有逗号）。
- 结果写库失败仍携带接收端建议；原始响应头不进入错误文本、日志、数据库或任务 payload。永久失败和耗尽业务预算正常使用 SkipRetry。
- `cmd/receiver` 新增 `-retry-after`，可用 `go run ./cmd/receiver -status 429 -retry-after 10` 演示。Worker 启动日志显示两个非敏感延迟配置；无需改表，只需更新重启 Worker。
- 补充重复响应头、非整倍数上限、纳秒/奇数边界，以及限流/预算耗尽后结果写库失败的测试。队列集成测试已增加 429、HTTP 日期、超大秒数、非法/重复头场景，断言调度回调结果、最终状态与历史。
- **本轮实际通过**：`go test -count=1 ./...`、`go vet ./...`、`go test -race -count=1 ./...`、`git diff --check`。未启用数据库/队列集成测试；当前 Docker 命令提示 WSL 集成不可用，因此不能声称新增场景已经通过真实 MySQL/Redis 验证，也不要覆盖此前用户反馈 Docker 可用的历史事实。
- 调度边界：Asynq v0.25.1 将计划时间存为 Unix 秒，可能向下取整不足一秒；扫描、排队及租约也影响发送时间。配置是调度策略，不保证精确执行时间或严格 Retry-After 等待下限；超过 max 的建议会被截断。Dispatcher 发布退避与 API 背压建议不受此配置影响。
- 下一步：在独立已迁移空测试库和 Redis 7.4 中运行 `HOOKRELAY_OUTBOX_INTEGRATION=1 go test -count=1 -race -v ./tests/integration`，不使用日常业务库或清空表。随后实现 HMAC，再做接收方持久化幂等。

## 此前进度：HMAC 签名与接收端验签

- 本轮从干净工作区继续；检查 Docker 后仍提示 WSL 集成不可用，因此上一轮重试队列验收继续保留为待完成，没有冒用宿主旧 Redis 或用户业务库验证。
- 新增 `internal/webhook/signature.go`：签名输入为 `timestamp + "." + delivery_id + "." + 原始 payload`，HMAC-SHA256，头格式 `v1=<hex>`。密钥用创建端点时 64 字符 hex **解码后的 32 字节**；时间窗口为前后 5 分钟，常量时间比较摘要。
- 执行链路：Worker 从已领取的 Endpoint 读取密钥 → Sender 每次发送前生成时间戳和签名 → Receiver 限量读取原始正文 → 检查唯一签名相关头、时间窗口和 HMAC → 才进入模拟延迟/状态码逻辑。密钥不进入队列 payload、日志或错误文本；非法本地密钥不发送 HTTP，Worker 记录 `signing_error` 并最终失败。
- `cmd/receiver` 必须通过环境变量 `RECEIVER_SIGNING_SECRET` 配置对应端点密钥，缺省/格式错误拒绝启动，不提供无签名降级。验签失败 401、超 256 KiB 请求 413，读取超时 5 秒；`-status`、`-delay`、`-retry-after` 在验签成功后仍可使用。
- 启动顺序更新见 README：先 API 并创建端点、保管一次性密钥，再配置 Receiver，最后提交事件。同一 Receiver 仅对应一个密钥；已有端点密钥未保存则创建新端点，不通过 GET 取回或让助手读取数据库密钥。
- **实际通过**：`go test -count=1 ./...`、`go vet ./...`、`go test -race -count=1 ./...`、`git diff --check`。包含独立 Python 计算的协议向量、篡改 ID/正文/时间戳、原始 JSON 空白、过期/未来/边界、重复头、错误密钥、不发无签名 HTTP、真实本地 Sender→Receiver 成功/429/500/401 和请求体大小限制。
- `tests/integration/async_test.go` 新增队列接收端验签断言，覆盖原有重试/重放链路，但**本轮未启用 MySQL/Redis 集成测试**。不能将本地 httptest 通过描述为真实队列签名验收。
- 无新迁移，未读取 `.env`、修改用户数据库、启停已有服务，也未自动提交。签名不加密正文，不阻止时间窗口内重放；接收端仍无持久化幂等，平台仍无自动对账。
- 下一步：接收端独立持久化去重记录与模拟业务写入，同一事务依赖 delivery_id 唯一约束；验证已处理但响应超时后重试仅产生一次业务效果。Docker 恢复后在隔离库补跑重试/签名队列测试。

## 此前进度：接收方持久化幂等

- 接续上一轮未提交的 HMAC 改动，无自动提交或覆盖用户修改。本轮 Docker 引擎 29.1.3 已可用，使用**本次新建**的隔离 MySQL 8.4/Redis 7.4 容器及两个测试库，未读取 `.env`、操作用户业务库或已有服务。
- `internal/receiver/store.go`：验签后，单事务 INSERT `receiver_receipts`（delivery_id 主键 + 原始正文 SHA-256）和 `receiver_effects`（模拟业务写入）。并发靠唯一约束裁决，不先查再写；业务写入失败回滚 receipt。receipt 唯一冲突后退出失败事务，再查已提交记录，相同正文返回 duplicate，不同正文返回冲突。
- `internal/receiver/handler.go`：验签→事务→响应；首次成功可延迟响应，重复请求立即 204；同 ID 不同正文 409、存储错误 503。非 2xx 故障模式在业务写入前拒绝，不落库。`-delay 8s` 现在表示首次业务提交后的响应延迟，专门演示“接收端处理了，但发送方以为失败”。
- `cmd/receiver` 新增独立 DB 连接、启动时检查接收端表、优雅退出。`config.LoadReceiverDB()` 只读取 `RECEIVER_MYSQL_*`，不继承平台密码/数据库，默认库和用户为 `hookrelay_receiver`；接收端仍必须配置端点签名密钥。
- 新增 **`migrations/receiver/000001_create_receipts.up.sql`**，不是平台 000006，不要导入 hookrelay 平台库。用户环境尚未执行，README 提供独立建库/用户/权限/导表步骤。已有平台五份迁移无需改动，不使用 AutoMigrate；DDL 部分失败需先检查，不删库重来。
- 新增 `scripts/test-integration.sh`：不加载 `.env`，创建随机端口、随机测试密码的临时 MySQL/Redis 与两个空库，导入对应迁移；运行存储/API、容量、Receiver、真实队列 race 测试。退出时仅删除本次创建的容器与临时卷，日志保留。
- **实际验证通过**：全包测试/vet/race；隔离数据库下 8 请求并发仅一次业务写入、并发不同正文一成功一冲突、第二次写入失败两表回滚、新连接池读取已提交记录。Receiver 存储测试连续 3 次通过。
- **真实队列关键结果**：第一次签名请求已提交接收端事务，响应延迟 10 秒，Sender 1 秒超时；Asynq 重试相同 delivery_id，命中 receipt，立即 204；投递 succeeded/attempt_count=2，历史先 timeout/retrying 后 succeeded；2 次 HTTP、1 receipt、1 effect。测试使用缩短超时，不冒充生产默认参数。
- 补跑上一轮 Retry-After 秒数/日期/超大值/非法及重复头、HMAC 队列验签、失败重放/旧轮次隔离、Outbox 恢复与背压全部通过。两次完整脚本运行均通过，最新日志 `/tmp/hookrelay-integration-LycOoP`；临时容器已清理。
- 边界：去重状态来自 MySQL，没有内存缓存；本轮验证新连接池重读，不宣称已完成接收端进程强杀/数据库灾难恢复。去重记录不清理，单平台 delivery_id 命名空间；同事务只能保护数据库内模拟副作用，不能保证外部支付/邮件恰好一次。
- 下一步单 Worker 进程内的端点并发配额（非阻塞获取）和慢端点影响测试；之后再做强杀恢复、Redis 丢数据对账与指标。

## 最新进度：单 Worker 端点并发限制

- 接续已有未提交的 HMAC/Receiver 代码，未覆盖或自动提交。本轮无新迁移，未读取 `.env`、修改用户业务库或启停已有服务；测试使用脚本新建的隔离 MySQL 8.4/Redis 7.4。
- `internal/config/worker.go`：Worker 专用配置 `WORKER_CONCURRENCY=4`、`ENDPOINT_CONCURRENCY=1`，要求正整数且端点限额不超过总并发；两者相等时启动警告不留其他端点余量。API 背压上限不受影响。
- `internal/delivery/limiter.go`：一个 Worker 共享一个按 endpoint_id 计数的进程内 limiter，TryAcquire 不等待容量，最后一个许可释放时删除空闲条目。`defer release()` 覆盖成功、DB 错误、取消和 panic。
- 请求链路：`DeliveryStore.EndpointID` 只读预查非终态同 generation 的端点 ID → TryAcquire → 原有 Claim 事务重新校验并登记尝试 → HTTP → Finish → 释放许可。预查不写库，端点 ID 不可变；终态、旧 generation、缺失记录直接跳过。
- 满额时通过 `task.Client.DeferDelivery` 发布 2～4 秒随机延迟的替代任务，再确认旧任务；不 Claim、不发 HTTP、不改变业务状态/attempt_count、不制造历史。队列任务 ID 需要更换，因为原任务仍处于 active；delivery_id/generation 不变，新任务只保留剩余普通重试次数。
- 选择原因：实际阅读 Asynq v0.25.1 `processor.handleFailedMessage`，确认它先判断重试上限、再调用 IsFailure。因此单靠 `IsFailure=false` 不能保护最后一次满额机会。真实 Redis 测试覆盖 MaxRetry(0) 重复延期，以及普通失败→延期(max=1)→失败→延期(max=0)→最终失败归档，证明没有重置失败预算。
- HTTP 重试元数据携带 DB attempt_count，`RetryPolicy.Delay` 使用 attempt_count-1 计算指数退避，避免更换队列 ID 重置 HTTP 退避；尚未领取的基础设施错误仍使用当前队列任务计数。接收方 Retry-After 的解析/上限不变。
- **实际验证**：全包 test/race、vet、diff 检查通过；脚本完整回归存储/API/背压/Receiver/队列。既有队列测试现也使用并发 4 + limiter。新增测试验证并发许可、释放/异常、预查无副作用、满额不消耗尝试、错误入队不确认任务，以及重试预算继承。
- **观测结果**：4 槽、每端点 1；慢端点 1 个挂起 HTTP + 6 个积压，另一个端点在约 **184ms 内已成功**（从后续入队到轮询观测成功且首次延期均完成的上界，不是基准测试）；慢请求随后继续等待重复延期，总 HTTP 延迟约 2.856s。释放后共 8 次 HTTP、每条投递只领取一次、慢端点峰值 1，无归档。测试使用同一受控 URL 的两个端点 ID，不是按主机隔离实验。
- 最新完整日志 `/tmp/hookrelay-integration-79LMLJ`；测试容器已清理。复现：`bash scripts/test-integration.sh`。不需要用户重复迁移；上一轮 Receiver 独立库的首次配置要求仍未代为执行。
- 边界：单进程配额会随多个 Worker 叠加；同 URL 不同端点也各自有配额。远端在本地超时后可能继续执行。大量热端点仍可能反复延期，无严格公平或最长等待保证；Redis 延期入队失败走普通重试，耗尽后可能遗留非终态记录。发布成功、旧任务确认前崩溃可能重复；仍需业务幂等。未来 Reconciler 不能只查初始稳定队列 ID 判断任务是否丢失。
- 下一步 Worker 强杀实验：记录 Asynq 恢复等待时间、数据库租约与未完成历史，再推进 Redis 丢数据对账与指标。

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
export MAX_PENDING_DELIVERIES="${MAX_PENDING_DELIVERIES:-10000}"
export WORKER_CONCURRENCY="${WORKER_CONCURRENCY:-4}"
export ENDPOINT_CONCURRENCY="${ENDPOINT_CONCURRENCY:-1}"

# 000001..000005 均已完成，不要重复执行迁移。
go run ./cmd/api
# 另外三个终端（Worker/Dispatcher 也需加载上述环境）：
# go run ./cmd/dispatcher
# go run ./cmd/worker
# Receiver 首次升级需按 README 创建独立库/用户并执行接收端迁移。
# 本终端需导出 RECEIVER_MYSQL_*，特别是独立库密码（不继承 MYSQL_*）。
# 然后设置对应端点一次性交付的 signing_secret：
# read -rsp 'Endpoint signing_secret: ' RECEIVER_SIGNING_SECRET; echo
# export RECEIVER_SIGNING_SECRET
# go run ./cmd/receiver
```

- 临时生成的密钥不会自动写入 `.env`；另一个终端也不会自动继承它。不在对话中粘贴密钥。
- `cmd/api` 还要求 `ALLOWED_ENDPOINT_URL`（例如本地开发用 `http://127.0.0.1:9090/webhook`）；不要把真实密钥贴到对话中。
- `cmd/api` 和 `cmd/healthcheck` 默认都监听 8080，不要同时启动。
- 先用 `/healthz`、`/readyz` 检查服务；若 HTTP_ADDR 已修改，使用实际地址。
- 不要重复执行已应用的 CREATE TABLE。平台五份迁移已应用；接收端独立迁移目前仅在测试库验证，用户环境仍需按 README 首次配置。若数据库卷改变或表缺失，先确认数据库和迁移状态，不通过删表或 AutoMigrate 绕过问题。
- 若执行集成测试，需数据库运行、迁移已应用，且当前终端已导出数据库配置：

```bash
HOOKRELAY_INTEGRATION=1 go test -count=1 -v ./internal/store ./cmd/api
```

## 收尾与保存边界

- 默认保留代码和进度在工作区，不自动提交、推送或部署，不替用户停止服务。
- 收尾时 Git 仍有业务代码修改和新增文件；下次先检查 `git status --short`，不要覆盖或清理这些改动。用户可能在两次会话间自行提交。
- 用户可以 Ctrl+C 停止 API，再执行 `docker compose stop mysql` 保留数据。
- 不执行 `docker compose down -v`；它会删除数据库数据卷。
- 当前进程/容器是否已经停止未确认，下次按实际状态检查。
