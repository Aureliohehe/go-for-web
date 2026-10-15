# Day7：etcd 介绍与集群搭建

本节学习 etcd 的特点、应用场景、与 ZooKeeper 的区别，以及三节点集群的搭建和验证。

> 参考：[TopGoer：ETCD 介绍](https://www.topgoer.com/数据库操作/go操作etcd/etcd介绍.html)。原文包含旧版 API 和历史背景，本文以 **etcd v3 API** 为主；示例使用静态集群配置，不直接沿用旧版目录 API。

## 1. etcd 是什么

etcd 是使用 Go 开发的开源分布式键值存储系统，通过 **Raft 共识算法**在多个节点之间保持数据一致，常用于配置共享、服务注册与发现、分布式协调。Kubernetes 使用 etcd 保存集群状态。

主要特点：

- **数据复制**：正常运行的成员最终保存完整的键值数据副本，而不是把数据分片到不同成员。
- **高可用**：在多数投票成员可通信的前提下，允许部分节点故障，并可自动选出新的 Leader。
- **强一致性**：默认键值读取是线性一致读；显式选择串行化读（serializable）时可能读到旧数据，不能笼统地说“所有读取总是最新”。
- **接口清晰**：v3 API 主要基于 gRPC，并提供 HTTP/JSON 网关；Go 中通常使用 `go.etcd.io/etcd/client/v3`。
- **持久化**：通过 WAL（预写日志）、快照等机制保存和恢复状态，但复制不等于备份。
- **安全机制**：支持 TLS、客户端证书认证及基于角色的访问控制，需要部署时主动配置。

etcd 适合保存少量、关键的元数据，不适合存放大文件，也不应直接替代 MySQL、对象存储或高吞吐缓存。吞吐量受磁盘延迟、网络、数据大小和集群规模影响，不宜把某个基准测试值视为固定性能。

## 2. 应用场景

### 2.1 服务注册与发现

服务发现解决的是：调用方如何根据服务名称找到当前可用实例的地址。

一种常见流程：

1. 服务启动后申请租约（Lease），将实例地址写入约定前缀，并把键绑定到租约。
2. 服务持续续租（KeepAlive），表示自己仍然存活。
3. 调用方查询该前缀，并通过 Watch 订阅实例的新增、更新和删除。
4. 服务主动退出时撤销租约；异常退出后租约到期，etcd 自动删除关联的键。

例如：

```text
/services/order/instance-01 -> 10.240.0.21:8080
/services/order/instance-02 -> 10.240.0.22:8080
```

注意：etcd 不会主动探测业务端口，也不负责请求转发或负载均衡。租约过期检测有延迟，续租成功也不代表业务一定健康，调用方仍需处理连接失败。

### 2.2 配置中心

把配置按业务组织为键值，例如：

```text
/config/order/log-level -> info
/config/order/timeout   -> 3s
```

应用启动时先读取配置，再使用 Watch 接收后续变化并更新本地配置。

**避免先读后订阅之间漏掉更新：**

1. 执行 Get，获得配置和响应头中的 revision，记为 `R`。
2. 从 `R + 1` 开始 Watch，而不是只订阅 Watch 建立后的变化。
3. 断线后根据已处理的 revision 恢复订阅；如果所需历史已被压缩（Compaction），重新读取完整配置并建立 Watch。

Watch 是变化通知机制，不保证应用瞬间完成配置切换；还需要校验配置内容，并处理取消、断线和重试。

### 2.3 分布式锁

分布式锁用于协调多个进程对共享资源的访问，例如防止多个实例同时执行同一个定时任务。

v3 中常用两种方式：

- **事务抢占**：使用 Txn 比较锁键的 `Version == 0`，仅在键不存在时执行 Put，并绑定租约。比较与写入是一个原子事务，竞争者中只有一个成功。
- **标准并发库**：Go 中使用 `go.etcd.io/etcd/client/v3/concurrency` 的 Session 和 Mutex，由库管理租约和锁等待队列。

释放自建锁时，应在事务中比较所有者标识或创建 revision，确认仍是自己的锁后再删除，避免误删其他客户端新获得的锁。

> 原资料中的 `prevExist`、目录、通过 `POST` 自动创建有序键属于旧版 v2 API，不能直接用于 v3。v3 使用扁平键空间，路径中的 `/` 只是键名的一部分；锁排队可利用创建 revision 表示顺序。

**锁并不能自动保护外部资源：**客户端暂停或网络中断后，租约可能已过期，但旧客户端恢复后仍可能继续写数据库。严格场景需要 fencing token（单调递增的令牌）等机制，由资源端拒绝过期持有者的写入。

## 3. etcd 与 ZooKeeper 如何选择

二者都可以实现服务发现、配置管理和分布式协调，不能简单认为 ZooKeeper “不能做”或“不值得使用”。

| 对比项 | etcd | ZooKeeper |
| --- | --- | --- |
| 主要实现语言 | Go | Java |
| 一致性协议 | Raft | ZAB（不是直接使用 Paxos） |
| 数据模型 | 扁平键值、revision、事务 | 树形 znode、节点版本、原子多操作 |
| 临时数据 | Lease 绑定键，过期删除 | 临时节点随会话结束删除 |
| 变化通知 | 可从指定 revision 订阅的 Watch，受历史压缩限制 | 传统 Watch 通常一次触发，较新版本也支持持久 Watch |
| 常见生态 | Kubernetes、云原生、Go | Hadoop 等已有 ZooKeeper 生态 |

选择 etcd 的常见原因：

- Go 二进制部署较直接，便于与 Go 服务、云原生系统集成。
- 提供事务、租约和 Watch，适合构建协调服务。
- Raft 的模型相对易于理解，工具链支持集群检查、快照和恢复。

如果现有系统已经依赖 ZooKeeper，并且团队具备成熟的运维经验，继续使用也很合理。应根据生态、语义需求、运维成本和版本支持选择，而非照搬旧文章中的项目活跃度判断。

## 4. etcd 集群与 Raft

### 4.1 Leader、Follower 与多数派

正常情况下，集群有一个 Leader，其余投票成员是 Follower。客户端可以连接任意可用成员，写请求最终由 Leader 协调复制；日志经多数派确认提交后才能作为已提交操作生效。Leader 故障时，其余成员通过选举产生新的 Leader。

对于 `N` 个投票成员：

```text
多数派数量 = floor(N / 2) + 1
最多容忍故障数 = floor((N - 1) / 2)
```

| 投票成员数 | 多数派数量 | 可容忍故障数 |
| --- | --- | --- |
| 1 | 1 | 0 |
| 2 | 2 | 0 |
| 3 | 2 | 1 |
| 4 | 3 | 1 |
| 5 | 3 | 2 |
| 7 | 4 | 3 |

因此常用 **3 或 5 个成员**；7 个成员也可用于特定需求，但并非越多越好。偶数成员通常比前一个奇数配置多一份复制开销，却没有增加容错能力。新增成员也不会像分片数据库那样直接提高写吞吐。

网络分区时，只有具备多数派的一侧能够继续提交写入。失去多数派后，不能正常写入或完成线性一致读；串行化读可能仍能返回本地旧数据，但不能代替一致性保证。

### 4.2 端口与部署原则

- `2379`：客户端访问端口，供业务程序、`etcdctl` 使用。
- `2380`：集群成员间通信端口，用于 Raft 复制等。
- 每个成员使用唯一名称和独立数据目录。
- 生产环境应跨故障域部署，避免全部成员因同一台宿主机或同一故障同时失效。
- 磁盘和网络延迟会影响提交延迟及选举稳定性，应使用可靠存储和稳定网络。

## 5. 搭建三节点静态集群

### 5.1 准备环境

准备三台互通的 Linux 主机，安装同一受支持版本的 etcd 及配套 `etcdctl`，并确保命令位于 `PATH` 中：

```bash
etcd --version
etcdctl version
```

下面的 IP 是示例，必须替换成实际网卡地址。各节点之间需允许访问 `2380`，客户端需能访问各节点的 `2379`。

| 名称 | 示例 IP | 数据目录（各自主机上） |
| --- | --- | --- |
| n1 | 10.240.0.17 | ./data/n1.etcd |
| n2 | 10.240.0.18 | ./data/n2.etcd |
| n3 | 10.240.0.19 | ./data/n3.etcd |

> 以下使用明文 HTTP，仅用于隔离的实验网络。不要把未认证的 etcd 暴露到公网；生产环境需启用客户端及节点间 TLS、认证与权限控制。

### 5.2 配置共同参数

在**每台机器启动 etcd 的同一个 Shell 中**，先执行：

```bash
TOKEN=day7-etcd-cluster-01
CLUSTER_STATE=new
CLUSTER='n1=http://10.240.0.17:2380,n2=http://10.240.0.18:2380,n3=http://10.240.0.19:2380'
```

- 三个节点必须使用相同的初始成员列表和 token，名称必须与列表一致。
- `new` 表示引导一个全新的集群，首次启动使用新的空数据目录。
- `initial-cluster-token` 用于区分初始化集群，**不是访问密码或认证凭据**。

### 5.3 分别启动节点

在 n1 上执行：

```bash
etcd --name n1 --data-dir=./data/n1.etcd \
  --listen-peer-urls http://10.240.0.17:2380 \
  --initial-advertise-peer-urls http://10.240.0.17:2380 \
  --listen-client-urls http://10.240.0.17:2379 \
  --advertise-client-urls http://10.240.0.17:2379 \
  --initial-cluster "$CLUSTER" \
  --initial-cluster-state "$CLUSTER_STATE" \
  --initial-cluster-token "$TOKEN"
```

在 n2 上执行：

```bash
etcd --name n2 --data-dir=./data/n2.etcd \
  --listen-peer-urls http://10.240.0.18:2380 \
  --initial-advertise-peer-urls http://10.240.0.18:2380 \
  --listen-client-urls http://10.240.0.18:2379 \
  --advertise-client-urls http://10.240.0.18:2379 \
  --initial-cluster "$CLUSTER" \
  --initial-cluster-state "$CLUSTER_STATE" \
  --initial-cluster-token "$TOKEN"
```

在 n3 上执行：

```bash
etcd --name n3 --data-dir=./data/n3.etcd \
  --listen-peer-urls http://10.240.0.19:2380 \
  --initial-advertise-peer-urls http://10.240.0.19:2380 \
  --listen-client-urls http://10.240.0.19:2379 \
  --advertise-client-urls http://10.240.0.19:2379 \
  --initial-cluster "$CLUSTER" \
  --initial-cluster-state "$CLUSTER_STATE" \
  --initial-cluster-token "$TOKEN"
```

以上命令以前台方式运行，需要在各自主机的终端分别启动。只有第一个成员启动时，暂时无法形成多数派是正常现象；至少两个成员能够通信后才具备多数派。

### 5.4 参数说明

| 参数 | 作用 |
| --- | --- |
| `--name` | 本节点名称，与初始成员列表中的名称对应 |
| `--data-dir` | 本节点持久化数据目录 |
| `--listen-peer-urls` | 本机监听成员间通信的地址 |
| `--initial-advertise-peer-urls` | 初始化时告知其他成员的本节点通信地址 |
| `--listen-client-urls` | 本机监听客户端请求的地址 |
| `--advertise-client-urls` | 对外公布的客户端访问地址 |
| `--initial-cluster` | 初始成员的名称及 peer 地址列表 |
| `--initial-cluster-state` | 初始化状态：新集群为 `new`，加入已有集群按成员管理流程使用 `existing` |
| `--initial-cluster-token` | 初始化集群的标识 |

`listen` 是本机绑定地址，`advertise` 是供其他机器连接的地址。监听可按需使用 `0.0.0.0`，但不能把它作为对外公布的可路由地址。

初始集群参数主要用于首次引导。已有数据目录会保留成员身份，重启时不要随意删除；扩容应先执行 `etcdctl member add` 等成员管理操作，不能只修改 `--initial-cluster`，也不能只改为 `existing` 就直接加入。

### 5.5 关于服务发现方式引导集群

参考资料还介绍了通过 `discovery.etcd.io` 获取 discovery URL，再通过 `--discovery` 引导集群的方式。其作用是让成员在初始化时找到彼此，**不是供业务直接读写的公共 etcd 数据库**。

这类发现方式的参数及支持情况随版本变化，应查阅目标版本文档；旧文章中的 discovery URL 不能直接复用。本节固定成员地址，使用静态配置即可，不需要同时配置 discovery。

## 6. 使用 etcdctl 验证集群

在能访问三个节点、已安装 `etcdctl` 的客户端终端中设置：

```bash
# 兼容仍支持 v2/v3 切换的旧版 etcdctl；现代版本默认使用 v3。
export ETCDCTL_API=3
export ENDPOINTS='http://10.240.0.17:2379,http://10.240.0.18:2379,http://10.240.0.19:2379'
```

### 6.1 成员、状态与健康检查

```bash
etcdctl --endpoints="$ENDPOINTS" --write-out=table member list
etcdctl --endpoints="$ENDPOINTS" --write-out=table endpoint status
etcdctl --endpoints="$ENDPOINTS" endpoint health
```

预期：成员列表包含 n1、n2、n3；正常稳定状态下只有一个 Leader，所有 endpoint 均健康。`member list` 反映成员配置，单独看到三个成员不代表三个成员都健康。

### 6.2 写入、查询和删除

以下命令只操作学习前缀 `/day7/`：

```bash
etcdctl --endpoints="$ENDPOINTS" put /day7/config/message 'hello etcd'
etcdctl --endpoints="$ENDPOINTS" get /day7/config/message
etcdctl --endpoints="$ENDPOINTS" get /day7/config/ --prefix
etcdctl --endpoints="$ENDPOINTS" del /day7/config/message
```

Put 通常返回 `OK`，Get 默认输出键和值，Del 返回删除的键数量。`--prefix` 是字符串前缀匹配，不是读取真实目录。

### 6.3 监听变化

在终端 A 中配置相同的 `ENDPOINTS`，执行：

```bash
etcdctl --endpoints="$ENDPOINTS" watch /day7/config/ --prefix
```

在终端 B 中配置相同的 `ENDPOINTS`，执行：

```bash
etcdctl --endpoints="$ENDPOINTS" put /day7/config/log-level debug
etcdctl --endpoints="$ENDPOINTS" del /day7/config/log-level
```

终端 A 应先后收到 `PUT` 和 `DELETE` 事件，按 `Ctrl+C` 结束监听。未指定起始 revision 的 Watch 不会自动返回现有配置。

### 6.4 验证容错能力（仅限实验环境）

1. 三节点均健康时，在其中一个节点的前台终端按 `Ctrl+C` 停止进程。
2. 使用仍存活节点的 endpoint 执行 Put/Get；若停止的是 Leader，需要等待重新选举。
3. 保留原数据目录，用原命令重启停止的节点，检查其恢复健康。
4. 若同时停止两个节点，剩余节点失去多数派，写入及线性一致读将无法正常完成。

不要为了测试直接删除数据目录，也不要在生产环境随意停止多数成员。

## 7. 常见问题与学习小结

- **连接超时**：检查进程、IP、端口、防火墙，以及公布的地址是否从访问方可达。`2379` 和 `2380` 不要混用。
- **无法选出 Leader**：检查是否有多数成员启动、peer 网络是否互通、初始成员配置是否一致。
- **重新搭建时身份冲突**：检查是否误用了旧数据目录；应先区分正常重启、成员替换和灾难恢复，不要直接清空目录。
- **Watch 历史不可用**：历史压缩后不能无限回放，需要重新获取快照状态再监听。
- **磁盘空间持续增长**：了解历史压缩与碎片整理（Defrag）的区别，并按官方运维流程操作。
- **数据安全**：定期备份快照并演练恢复；多数副本无法防止误删除同步到整个集群。

记住：**Raft 保证已提交数据的一致性，Lease 管理存活时间，Watch 通知变化，Txn 提供原子条件更新。** 后续 Go 实践可以围绕 `clientv3.New`、`Put/Get/Delete`、`Watch`、`Grant/KeepAlive` 和 `Txn` 展开。

## 8. 参考资料

- [TopGoer：ETCD 介绍](https://www.topgoer.com/数据库操作/go操作etcd/etcd介绍.html)
- [etcd 官方文档（按所用版本选择）](https://etcd.io/docs/)
- [etcd v3.6：静态集群与其他引导方式](https://etcd.io/docs/v3.6/op-guide/clustering/)
- [etcd v3.6：API 保证](https://etcd.io/docs/v3.6/learning/api_guarantees/)
