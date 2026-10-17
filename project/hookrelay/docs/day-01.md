# 第一天：启动 API 并连接 MySQL

## 今日目标

先不建业务表，也不接 Redis。只完成配置 → 数据库连接 → HTTP 健康检查这条最小链路。

```text
导出环境变量 → config.Load → store.OpenMySQL → 启动 HTTP API
                               │                    ├─ /healthz：进程存活
                               │                    └─ /readyz：数据库可用
                               └─ 连接失败则退出，不伪装启动成功
```

## 1. 先看代码，再启动

建议按下面的顺序阅读，不需要一次理解所有依赖：

1. `.env.example`：运行时需要哪些配置。
2. `internal/config/config.go`：配置默认值、必填密码、端口校验。
3. `internal/store/mysql.go`：DSN、连接池、PingContext。
4. `internal/handler/router.go`：存活与就绪的区别。
5. `cmd/api/main.go`：把这些模块连接起来，处理启动和退出。
6. 对应的 `*_test.go`：每个模块如何验证。

### 三个关键概念

- **DSN**：连接数据库的描述，包含账号、密码、地址和数据库名。使用驱动提供的格式化函数，不手拼、不打印。
- **连接池**：`sql.DB` 不是单个连接，而是可复用的连接池。GORM 复用它，进程退出时关闭。
- **超时**：连接数据库、就绪检查和 HTTP 服务都有时间上限，避免无限等待。

GORM 本身不负责这里的建库。Docker 镜像首次初始化时创建数据库和应用账号；未来的业务建表使用 migration，不在启动时 AutoMigrate。

## 2. 准备运行环境

在项目目录执行：

```bash
cd /root/hello-gp/project/hookrelay
go version
docker compose version
```

本次实现环境的 Go 版本为 `go1.27.1`，与 go.mod 一致。

### 如果 WSL 提示 Docker 不可用

在 Windows 的 Docker Desktop 中：

1. 启动 Docker Desktop，使用 Linux containers。
2. 在 Settings → Resources → WSL Integration 中启用当前发行版。
3. 应用设置，必要时重开 WSL 终端。
4. 再执行 `docker compose version` 和 `docker info`。

这里只需要 Docker 客户端可以连接 Docker Desktop，无需为了本项目修改已有 MySQL 服务。

默认将项目 MySQL 映射到 `127.0.0.1:13306`，容器内部仍为 `3306`。这样可以避开本机已有的 `3306` 服务；若 `13306` 也被占用，修改 `.env` 中的 MYSQL_PORT。

## 3. 配置并启动 MySQL

```bash
# 保留已有配置，不覆盖自己的密码。
test -f .env || cp .env.example .env

# 检查 Compose 配置，不打印展开后的密码。
docker compose config --quiet

# 首次运行会拉取镜像、初始化数据目录；等待健康检查成功。
docker compose up -d --wait mysql

docker compose ps
```

`.env.example` 的密码仅用于本地练习。`.env` 已被 Git 忽略，不要提交真实凭据。

必要时查看日志：

```bash
docker compose logs --tail=100 mysql
```

使用应用账号检查数据库：

```bash
docker compose exec mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql --protocol=TCP -h 127.0.0.1 -u "$MYSQL_USER" -D "$MYSQL_DATABASE" -e "SELECT 1"'
```

预期输出包含 `1`。这里只执行查询，不修改任何业务数据。

## 4. 启动 API

Compose 会自动读取 `.env`，但 Go 程序不会。需要先把变量导出到当前终端：

```bash
# 只 source 自己可信的配置文件，这个操作会按 shell 脚本执行文件内容。
set -a
. ./.env
set +a

go run ./cmd/api
```

默认监听 `127.0.0.1:8080`，只允许本机访问。启动日志应包含 `mysql connected` 和 `starting api`。

如果报 `MYSQL_PASSWORD is required`，通常是忘记导出环境变量；如果报连接被拒绝，检查 MySQL 容器状态和 MYSQL_PORT。

启动时连接数据库失败会返回非零退出码，修复配置后重新启动即可。

## 5. 验证两个健康接口

打开第二个终端：

```bash
curl -i http://127.0.0.1:8080/healthz
curl -i http://127.0.0.1:8080/readyz
```

预期：

```text
/healthz → HTTP 200，{"status":"ok"}
/readyz  → HTTP 200，{"status":"ready"}
```

### 停掉数据库，再观察区别

只操作本项目 Compose 服务，不要停止机器上原有的 MySQL：

```bash
cd /root/hello-gp/project/hookrelay
docker compose stop mysql

curl -i http://127.0.0.1:8080/healthz
curl -i http://127.0.0.1:8080/readyz
```

此时预期：

```text
/healthz → HTTP 200，{"status":"ok"}
/readyz  → HTTP 503，{"status":"not_ready"}
```

原因：API 进程还在，但依赖不可用，不应宣称已经就绪。接口不会把底层数据库连接错误直接返回给客户端。

恢复数据库后再检查：

```bash
docker compose up -d --wait mysql
curl -i http://127.0.0.1:8080/readyz
```

预期恢复到 `200`，无需重启 API。

## 6. 运行测试

普通测试不依赖数据库，集成测试默认跳过：

```bash
go test ./...
go vet ./...
go test -race ./...
```

真实 MySQL 测试需要已导出配置且数据库已启动：

```bash
set -a
. ./.env
set +a
HOOKRELAY_INTEGRATION=1 go test -v ./internal/store
```

集成测试通过 GORM 执行 `SELECT 1`，不创建或删除表。使用 `-v` 可以确认它确实执行，而不是被跳过。

编译可执行文件：

```bash
mkdir -p bin
go build -o bin/hookrelay-api ./cmd/api
./bin/hookrelay-api
```

先停止原来 `go run` 启动的 API，避免端口冲突。`bin/` 已被 Git 忽略。

## 7. 停止服务与数据保留

在 API 终端按 Ctrl+C，程序会尝试在 5 秒内等待请求结束，再关闭连接池。

```bash
docker compose down
```

上述命令保留命名数据卷。不要随意执行带 `-v` 的删除命令，它会删除项目数据库数据。

MySQL 镜像的初始化环境变量只在数据卷为空时生效。数据库初始化后修改 `.env` 的密码，不会自动修改已有数据库账号密码。

## 8. 验收与已验证范围

自己完成下面的操作后，第一天任务才算掌握：

- [ ] 能解释配置来自哪里，以及为什么 Go 不会自动加载 `.env`。
- [ ] 能通过 Compose 启动 MySQL，并用应用账号执行 SELECT 1。
- [ ] 能启动 API，看到两个健康检查返回 200。
- [ ] 能停止数据库，观察 healthz=200、readyz=503。
- [ ] 能恢复数据库，观察 readyz 恢复为 200。
- [ ] 能执行测试，并解释普通测试和集成测试的区别。
- [ ] 能按 Ctrl+C 正常结束 API。

本次实现已通过 Go 构建、单元测试、go vet，以及独立临时 MySQL 8.0 实例上的连接测试、真实 HTTP 健康检查、数据库停止后的检查和 API 优雅退出。临时实例没有改动机器原有数据库。

**尚待验证：** 当前 WSL 的 Docker Desktop 集成未启用，因此 `mysql:8.4` 的 Compose 启动路径需要启用 Docker 后再实测，不能把本地 MySQL 测试等同于容器部署验证。

## 9. 明天做什么

今天没有业务 API，因此暂不加入 API Key、业务请求体限制和业务表。第二天开始设计 endpoints、events、deliveries 的迁移，再逐步实现事件接入事务。

先尝试回答：数据库已经正常连接，为什么仍然需要在创建事件时使用事务？
