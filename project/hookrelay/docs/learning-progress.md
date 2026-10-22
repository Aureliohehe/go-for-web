# 学习进度与下次起点

> 已完成：配置/handler/store 拆分、优雅退出、endpoints 表及数据库读写、API Key 中间件、接收端创建与查询接口及单元测试。
> **下次先讲解 POST 请求链路并验证真实 HTTP + MySQL 创建→查询闭环。**

## 辅导方式与协作约定

- 最新偏好：**助手写代码，用户理解逻辑；不懂的地方由助手讲解**。讲解按请求链路展开，不要默认把实现任务布置给用户。
- 用户明确要求的代写范围为当前创建接收端功能；后续任务继续确认需求与节奏。
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
- 新增 `cmd/api/endpoint_integration_test.go`：使用真实本地 HTTP 与 MySQL，在事务中验证创建→查询、密钥只在创建响应出现，并在测试结束回滚。默认跳过，需 `HOOKRELAY_INTEGRATION=1`、环境数据库配置和已应用的 endpoints 迁移。普通测试已运行，**真实 HTTP + MySQL 测试尚未执行**。
- 已运行 `go test ./...`、`go vet ./...`、`go test -race ./internal/handler`、`git diff --check`；下一步结合代码解释集成测试并让用户自行在受控环境运行，不能宣称真实数据库已验证。

之后再推进 PATCH 启用/禁用、events/deliveries 表和事件事务幂等。当前尚无事件接口、Redis/Asynq、Outbox、投递、重试或签名发送功能，不能宣称可靠投递已实现。

## 下次恢复环境

在项目根目录执行：

```bash
cd /root/hello-gp/project/hookrelay
docker compose up -d --wait mysql

# 只加载自己可信的本地配置文件。
set -a
. ./.env
set +a

# 若没有持久保存 API_KEY，可只为当前终端生成一个新的开发密钥。
if [ -z "${API_KEY:-}" ]; then
  export API_KEY="$(openssl rand -hex 32)"
fi
export ALLOWED_ENDPOINT_URL="${ALLOWED_ENDPOINT_URL:-http://127.0.0.1:9090/webhook}"

go run ./cmd/api
```

- 临时生成的密钥不会自动写入 `.env`；另一个终端也不会自动继承它。不在对话中粘贴密钥。
- `cmd/api` 还要求 `ALLOWED_ENDPOINT_URL`（例如本地开发用 `http://127.0.0.1:9090/webhook`）；不要把真实密钥贴到对话中。
- `cmd/api` 和 `cmd/healthcheck` 默认都监听 8080，不要同时启动。
- 先用 `/healthz`、`/readyz` 检查服务；若 HTTP_ADDR 已修改，使用实际地址。
- 不要重复执行已应用的 CREATE TABLE。若数据库卷改变或表缺失，先确认数据库和迁移状态，不通过删表或 AutoMigrate 绕过问题。
- 若执行集成测试，需数据库运行、迁移已应用，且当前终端已导出数据库配置：

```bash
HOOKRELAY_INTEGRATION=1 go test -count=1 -v ./internal/store ./cmd/api
```

## 收尾与保存边界

- 本次收尾只保存进度文档、同步 README，不自动提交、推送或部署，不替用户停止服务。
- 收尾时 Git 仍有业务代码修改和新增文件；下次先检查 `git status --short`，不要覆盖或清理这些改动。用户可能在两次会话间自行提交。
- 用户可以 Ctrl+C 停止 API，再执行 `docker compose stop mysql` 保留数据。
- 不执行 `docker compose down -v`；它会删除数据库数据卷。
- 当前进程/容器是否已经停止未确认，下次按实际状态检查。
