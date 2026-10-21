# 学习进度与下次起点

## 后续辅导方式（重要）

用户要求的是带着学习，而不是由助手代写完整项目：

1. 每次只讲一个小步骤：先解释目的、代码和命令的含义。
2. 由用户手动写代码、执行命令，再反馈结果。
3. 检查结果、回答疑问后，再继续下一步。
4. 未经明确要求，不替用户修改业务或练习代码。
5. 记录进度时可以修改文档；不要把记录任务扩展成代码实现。
6. 之前助手生成的完整骨架只作为参考，不代表用户已经理解或独立完成。

## 第一天已完成

以下实验由用户逐步执行，并在对话中反馈成功。

### 1. Go 项目与工具链

- 检查 `go version`：`go1.27.1 linux/amd64`。
- 检查 `go env GOMOD`：`/root/hello-gp/project/hookrelay/go.mod`。
- 理解 module 路径是内部包导入路径的前缀；本地开发不要求先创建 GitHub 仓库。

### 2. 配置与环境变量

- 理解 `.env.example` 是模板，`.env` 是本机配置。
- 创建 `.env`，确认 Git 忽略它；未在学习记录中保存密码。
- 使用以下命令将配置导出到当前终端：

  ```bash
  set -a
  . ./.env
  set +a
  ```

- 理解 Go 不自动读取 `.env`，而是通过 `os.Getenv` 读取进程环境。
- 编写 `cmd/envcheck/main.go`，读取并打印 MYSQL_HOST。
- 验证命令前临时赋值只影响本次命令，不修改文件或当前终端已有变量。
- 理解修改 `.env` 后，如果不重新加载，新启动的程序仍会继承终端中的旧值。

### 3. Docker Compose 与 MySQL

- 确认 `docker compose version` 和 `docker info` 均成功，之前 Docker 不可用的阻塞已解除。
- 理解镜像、容器、环境变量、命名数据卷及健康检查。
- 检查 Compose 配置，并成功启动 MySQL，确认健康状态。
- 理解本机端口 `13306` 映射到容器端口 `3306`。
- 在容器内用应用账号登录 MySQL，执行 SELECT 1、查询当前数据库与用户、SHOW TABLES。
- 确认尚无业务表。

### 4. Go 连接数据库

- 编写 `cmd/dbcheck/main.go`，使用 `database/sql` 和 MySQL 驱动。
- 从环境变量构造连接配置，通过驱动生成 DSN，不打印密码或 DSN。
- 理解 `sql.Open` 初始化连接池对象，成功不代表数据库可达。
- 使用 `PingContext` 实际验证连接，理解连接超时、上下文超时及 defer 清理。
- 正常配置下连接成功。
- 临时使用错误端口模拟连接失败，检查错误与非零退出码；恢复正常命令后连接成功。
- 用户明确理解：此次连接拒绝发生在 PingContext，而不是 sql.Open。

### 5. HTTP 健康检查与故障恢复

- 编写 `cmd/healthcheck/main.go`，使用 Gin 启动 HTTP 服务。
- 注册 GET /healthz，返回 200 和 `{"status":"ok"}`。
- 请求未注册路径，验证 404。
- 将数据库连接池放在路由外初始化，供请求复用。
- 增加 GET /readyz，通过带超时的 PingContext 检查数据库。
- 理解失败分支需要 return，且不向客户端暴露数据库底层错误。
- 完成数据库停止与恢复实验，过程中未重启 Go 服务：

  | 场景 | /healthz | /readyz |
  | --- | --- | --- |
  | 数据库正常 | 200 | 200 |
  | 数据库停止 | 200 | 503 |
  | 数据库恢复 | 200 | 恢复为 200 |

- 理解连接池可以重新建立连接，但这不等于失败的业务写入能够自动安全重试。

### 6. 已解释但尚未亲手完成的拆分

用户询问了之前助手生成的 `internal/config` 和 `internal/handler` 的用途，已解释：

- `config`：读取环境变量、提供默认值、校验配置，不连接数据库。
- `store`：访问数据库、建立连接池。
- `handler`：接收 HTTP 请求、调用逻辑、返回响应，不负责读取数据库密码或初始化连接池。
- `main`：组装配置、数据库和 HTTP 服务，管理启动流程。

这些是工程职责划分，不是 Go 强制目录规范。用户还没有亲手将练习程序拆分成这些包。

## 当前代码的两条线

### 用户亲手完成的练习

- `cmd/envcheck/main.go`
- `cmd/dbcheck/main.go`
- `cmd/healthcheck/main.go`

继续教学以这些代码为起点，尤其是 `cmd/healthcheck/main.go`。

### 助手之前生成的参考骨架

- `cmd/api/main.go`
- `internal/config/`
- `internal/store/`
- `internal/handler/`
- 对应测试和 `docs/day-01.md`

不能因为参考骨架已经存在，就跳过用户的学习与动手步骤。当前练习程序尚未实现完整的优雅退出，也尚未加入配置校验、业务鉴权、业务表或事件接口。

## 下次从这里开始

**主题：把用户写在 `cmd/healthcheck/main.go` 中的配置读取逻辑，逐步提取为独立配置包。暂不进入建表。**

### 第一个教学步骤

1. 请用户打开自己的 `cmd/healthcheck/main.go`。
2. 找出所有 `os.Getenv` 调用，说明哪些属于读取配置，哪些属于数据库驱动设置。
3. 解释为什么要分离“读取配置”和“使用配置创建连接池”。
4. 让用户先设计一个简单配置结构体，理解字段如何映射到环境变量；此时不要直接交付完整 config 包。

### 之后逐步推进

- 先查看已有 `internal/config` 及其调用方，说明它来自先前生成的参考骨架。
- 与用户确认如何利用或调整已有文件，避免不经说明覆盖参考代码、破坏其他入口。
- 由用户逐步实现配置读取、错误返回、必要校验，并在练习入口调用。
- 每次只增加少量内容，通过正常配置、缺失配置实验验证行为。
- 配置拆分理解后，再考虑拆分 handler 和 store；不要一次重构所有目录。
- 拆分完成后再进入 endpoints、events、deliveries 的模型设计与迁移。

## 下次恢复环境

在项目目录执行：

```bash
cd /root/hello-gp/project/hookrelay
docker compose up -d --wait mysql

set -a
. ./.env
set +a

go run ./cmd/healthcheck
```

在另一个终端检查：

```bash
curl -i http://127.0.0.1:8080/healthz
curl -i http://127.0.0.1:8080/readyz
```

注意：不要同时启动 `cmd/api` 和 `cmd/healthcheck`，它们默认都监听 8080，会发生端口冲突。

## 收尾提示

已建议用户在结束时按 Ctrl+C 停止练习程序，执行 `docker compose stop mysql` 保留数据；没有确认当前进程是否已经停止，下次应按实际状态检查。
