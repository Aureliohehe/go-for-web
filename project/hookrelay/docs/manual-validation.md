# 隔离联调与恢复演练记录

## 范围与环境

本轮经用户同意进行运行验证，**未新增或修改项目测试文件，未运行 `go test`**。使用真实编译后的 API、Dispatcher、Worker、Reconciler、Receiver；不读取 `.env`，不连接开发数据库，不操作已有服务，不自动提交。

- Docker 的 WSL 接口不可用，改用独立原生进程、随机回环端口及临时数据目录。
- MySQL **8.0.42**；Redis **7.4.2**（在临时目录编译，下载包 SHA-256 与官方 redis-hashes 一致）。没有使用本机旧 Redis 5。
- 在新建的平台库导入六份迁移，在新建的 Receiver 库导入其独立迁移。开发库的 `000006` 和 Receiver 配置待办不变。
- 默认业务预算 3 次，Worker 并发 4、每端点 1；HTTP 超时 5 秒、任务超时 20 秒、执行租约 30 秒均未修改。
- 为缩短演练：`RETRY_BASE_DELAY=1s`、`RETRY_MAX_DELAY=30s`、`RECONCILE_GRACE=1m`、`RECONCILE_INTERVAL=1s`、恢复上限 3。**不是默认 5 分钟宽限、30 秒扫描间隔的恢复性能。**
- Receiver 按阶段使用 500、429/Retry-After=20、正常响应，以及提交业务后延迟 10 秒响应。

临时日志与驱动程序：`/tmp/hookrelay-manual-MT5472`。主要证据为 `results.json`、`metrics.prom`、各进程日志及 `cleanup.json`。驱动程序和 Go 探针仅存于该临时目录，不加入项目测试套件。早期启动尝试遇到本机 mysql 客户端不支持的参数及 MYSQL_HOST 与 socket 协议冲突，修正临时驱动后完整运行通过，未因此修改业务代码。

## 实际结果

| 场景 | 操作与结果 |
| --- | --- |
| 空环境与鉴权 | 空列表为 `items: []`、空游标，指标事件数为 0；列表和指标无密钥均返回 401 |
| 事件幂等 | 相同键/正文返回 200 和原 ID；改变正文返回 409 |
| 失败与历史 | 两条事件面对 HTTP 500，各执行 3 次后 failed，历史完整；另一端点的错误签名返回 401，仅尝试 1 次 |
| 筛选分页 | 按 failed + endpoint_id，每页 1 条，正确取得两页；端点筛选互不混入，不存在端点返回空列表 |
| 参数校验 | 未知状态、重复状态、空/零端点、过大 limit、负 after、未知参数、错误编码共 8 个请求均返回 400 |
| 人工重放 | 接收端恢复正常后，failed 重放返回 202，generation 从 1 变 2，成功；保留原 3 条历史并新增 1 条，接收端业务记录为 1；再次重放成功记录返回 409 |
| 指标 | 真实 MySQL 聚合及 HTTP 文本输出成功；核对接入 accepted/reused/conflict、投递状态、尝试结果/耗时样本、积压与未知结果数量 |
| 截止时间延期 | 首次 429 写入 20 秒等待；注入提前到达的重复任务后，新任务计划时间在 DB 截止时间之后 **4.834 秒**（抖动 + 向上取整），DB 仍仅 1 次尝试和 1 条历史 |
| 延期预算 | 替代任务 MaxRetry=2、Retried=0，与注入任务剩余预算一致；恢复正常 Worker 后第 2 次业务尝试成功，其他重复任务未新增历史 |
| 延期 ID 取消回写 | Go 探针在真实替代任务入队成功后立即取消 handler 上下文；Worker 仍成功写入 queue_task_id，新 ID 在 Redis 中为 scheduled |
| Dispatcher 取消回写 | Go 探针在真实 Redis 入队成功后立即取消 DispatchOne 上下文；调用成功，Outbox 仍写为 published |
| 已发布任务丢失 | 删除本轮 delivery=5 的已发布 pending 队列任务；Reconciler 经宽限后恢复，新 generation=2、recovery_count=1，最终 attempt_count=1 并成功 |
| Worker 强杀 | Receiver 已提交 delivery=6 的业务记录但延迟响应时，对本轮 Worker 发送 SIGKILL；保留 processing 和未知结果历史，重启后由 Asynq 恢复，最终成功且业务仍只有 1 条 |

### 两条恢复路径的区别

**队列任务被删除：**

```text
published Outbox + 队列任务缺失
→ Reconciler 等待活动宽限期
→ generation=2 + 新 Outbox
→ Dispatcher 入队 → Worker 成功
```

从删除任务后到观察到 succeeded 约 **61.68 秒**。这是轮询观测值，包含调度开销，不是恢复上限。

**Worker 被强杀，但 Redis 保留 active 任务：**

```text
Receiver 已提交 → Worker SIGKILL → 结果未保存
→ 重启 Worker，Asynq 等待其队列执行租约过期并恢复
→ 相同 delivery_id 重投 → Receiver 命中持久化去重 → 204
```

从强杀并等待进程退出后到观察到 succeeded 约 **65.30 秒**，包含移除 Receiver 人工延迟、重启进程及轮询开销。generation 保持 **1**、recovery_count 保持 **0**、attempt_count 为 **2**，说明由 Asynq 恢复而非 Reconciler 切换 generation。第一条历史保持未知结果，第二条 succeeded；接收端只有 1 条业务记录。数据库 30 秒租约到期不等于 Asynq 会立即再次调度。

最终指标：6 条事件，未完成投递/Outbox 均为 0，未保存结果的尝试为 1。这个 1 是被强杀执行的未知历史，不代表还有一条未完成投递，也不能将其伪造为失败结果。

## 清理与未覆盖边界

- 全部本轮启动的数据库、Redis 和业务进程均已退出；结束后核对监听端口，没有遗留本轮服务。保留临时目录作为诊断记录。
- 取消回写是在真实依赖上用临时探针精确取消上下文，不等同于强杀后还能执行清理；SIGKILL 下依然只能依赖租约、队列恢复和业务幂等。
- 本轮是少量事件、单 Worker/Dispatcher 的定点演练，没有大数据量压测、完整 Redis 清空、持续数据库故障、多实例恢复或 Receiver 强杀。
- 验证了指标 HTTP 输出及数值，没有部署 Prometheus 验证实际抓取配置。
- 单次观测不能证明所有故障组合可靠，也不能推导默认配置的恢复时限；下一步优先完整 Redis 数据丢失及多进程故障组合。
