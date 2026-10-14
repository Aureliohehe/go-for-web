# Day6：Go 操作 MySQL 与事务

本节学习通过 `sqlx` 连接 MySQL、执行增删改查，并使用事务保证多条 SQL 要么全部提交，要么全部回滚。

对应代码：

- [`mysql.go`](./mysql.go)：查询示例，使用 `Select` 将结果映射到 `[]Person`。**当前文件全部被注释，不能直接运行**；下文同时保留 INSERT 的学习内容。
- [`musql_transaction.go`](./musql_transaction.go)：事务示例，在同一个事务中插入两条记录。文件名以仓库实际拼写为准。

## 1. 安装依赖

在项目模块目录下执行：

```bash
go get github.com/go-sql-driver/mysql
go get github.com/jmoiron/sqlx
```

```go
import (
    "fmt"

    _ "github.com/go-sql-driver/mysql"
    "github.com/jmoiron/sqlx"
)
```

- `github.com/go-sql-driver/mysql`：MySQL 驱动。匿名导入 `_` 会执行包的初始化逻辑，向 `database/sql` 注册名为 `mysql` 的驱动。
- `github.com/jmoiron/sqlx`：标准库 `database/sql` 的扩展，提供结构体映射、`Get`、`Select` 等功能，并不只支持 MySQL。

## 2. 准备数据库和表

先启动 MySQL，再登录：

```bash
mysql -u root -p
```

在 MySQL 客户端执行：

```sql
CREATE DATABASE IF NOT EXISTS gotest
    DEFAULT CHARACTER SET utf8mb4;

USE gotest;

CREATE TABLE IF NOT EXISTS person (
    user_id INT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    username VARCHAR(64) NOT NULL,
    sex VARCHAR(16) NOT NULL,
    email VARCHAR(128) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

- `user_id` 是自增主键，插入时可以省略。
- 使用支持事务的 **InnoDB** 引擎；MyISAM 不支持这里需要的事务回滚。
- `sqlx.Open` 和 Go 结构体都不会自动创建数据库或表。
- `IF NOT EXISTS` 不会更新已有表的结构或引擎，可用 `SHOW CREATE TABLE gotest.person;` 检查。
- 本例没有给 `username` 添加唯一约束，因此插入两个相同的 `stu001` **不会因为用户名重复而失败**。

## 3. 结构体与字段映射

代码中的 `Person`：

```go
type Person struct {
    UserId   int    `db:"user_id"`
    Username string `db:"username"`
    Sex      string `db:"sex"`
    Email    string `db:"email"`
}
```

字段需要导出（首字母大写），`db` 标签指定字段对应的数据库列，例如 `UserId` 对应 `user_id`。

- 查询时，`Get`、`Select` 会使用这些标签完成结果映射。
- 使用 `Exec` 直接传参数时，不会用到这些结构体标签。
- 代码中还定义了 `Place`，但当前操作并未使用，因此不需要创建对应表。
- 如果数据库列允许 `NULL`，需根据情况使用 `sql.NullString` 等可空类型，避免将 `NULL` 扫描到普通 `string` 时出错。

## 4. 初始化数据库连接

### 4.1 当前代码与 DSN

两个示例采用相同的初始化方式：

```go
var Db *sqlx.DB

func init() {
    database, err := sqlx.Open("mysql", "root:root@tcp(127.0.0.1:3306)/gotest")
    if err != nil {
        fmt.Println("open mysql failed,", err)
        return
    }
    Db = database
}
```

DSN（数据源名称）的基本格式：

```text
用户名:密码@tcp(主机地址:端口)/数据库名
```

当前使用 `root` / `root` 连接 `127.0.0.1:3306` 上的 `gotest`。运行前应按本地配置修改，实际项目不要把真实密码写入仓库，可从环境变量或配置管理中读取。

如需将 MySQL 日期时间解析为 Go 的 `time.Time`，可以按需配置：

```text
root:root@tcp(127.0.0.1:3306)/gotest?charset=utf8mb4&parseTime=true&loc=Local
```

`loc=Local` 指定驱动解析时间使用的位置，并不自动修改 MySQL 会话的时区。

### 4.2 `Open`、`Ping` 与连接池

- `init()` 在 `main()` 前自动执行。
- `*sqlx.DB` 是可复用的数据库操作对象，底层管理**连接池**，不是单条连接。
- `sqlx.Open` 通常不会立即连接服务器，返回成功不代表账号密码和数据库一定可用。
- `Db.Ping()` 主动检查连通性；`sqlx.Connect` 则包含 Open 和 Ping 的过程。

当前代码在初始化失败时仅 `return`，不会阻止 `main()` 继续运行；若 `Db` 仍为 `nil`，后续方法调用可能 panic。学习阶段可将 `init()` **替换**为：

```go
func init() {
    database, err := sqlx.Connect("mysql", "root:root@tcp(127.0.0.1:3306)/gotest")
    if err != nil {
        panic(fmt.Errorf("connect mysql failed: %w", err))
    }
    Db = database
}
```

实际项目更适合在 `main()` 中显式初始化并处理错误。连接池还可按负载设置 `SetMaxOpenConns`、`SetMaxIdleConns`、`SetConnMaxLifetime` 等参数，不要每次查询都重新创建 `DB`。

### 4.3 关闭资源

确认初始化成功后，在 `main()` 开头添加：

```go
defer Db.Close()
```

它会在 `main()` 返回时关闭数据库操作对象。不要在 `init()` 中这样做，否则初始化结束就会关闭连接池。事务的 `Commit` / `Rollback` 与 `Db.Close` 是不同层次的资源管理，不能互相替代。

## 5. Go 操作 MySQL：增删改查

以下片段假设 `Db` 已成功初始化，可分别放入 `main()` 练习；它们是学习用法，不表示当前源文件已包含全部 CRUD。

### 5.1 INSERT：`Exec` 与自增 ID

```go
r, err := Db.Exec(
    "INSERT INTO person(username, sex, email) VALUES (?, ?, ?)",
    "stu001", "man", "stu01@qq.com",
)
if err != nil {
    fmt.Println("insert failed:", err)
    return
}

id, err := r.LastInsertId()
if err != nil {
    fmt.Println("get insert id failed:", err)
    return
}
fmt.Println("insert succ:", id)
```

`Exec` 用于执行不返回结果集的 SQL，如 `INSERT`、`UPDATE`、`DELETE`，返回 `sql.Result` 和 `error`。

`?` 是 MySQL 的参数占位符，参数按顺序对应 `username`、`sex`、`email`。应通过参数传值，而不是拼接用户输入，以避免 SQL 注入。占位符只能代表数据值，不能替代表名或列名。

`LastInsertId()` 返回本次插入生成的自增 ID，类型为 `int64`。在默认自动提交模式下，直接执行的 INSERT 成功后，即使读取 ID 失败，也不代表插入被回滚，不应盲目重试。

### 5.2 SELECT：查询多条记录

`mysql.go` 中的核心查询是：

```go
var person []Person
err := Db.Select(
    &person,
    "SELECT user_id, username, sex, email FROM person WHERE user_id=?",
    2,
)
if err != nil {
    fmt.Println("select failed:", err)
    return
}
fmt.Println("select succ:", person)
```

- `Select` 适合查询多条记录，目标传入**切片指针**。
- 即使按主键查询最多只返回一条，也可以用切片接收。
- 没有匹配记录时，结果为空切片，不会返回 `sql.ErrNoRows`。
- 本例查询 ID 为 `2` 的记录；若数据库中没有该 ID，空结果是正常情况。

### 5.3 SELECT：查询单条记录

查询单条记录可使用 `Get`。下面的片段需要额外导入标准库 `database/sql` 和 `errors`：

```go
var person Person
err := Db.Get(
    &person,
    "SELECT user_id, username, sex, email FROM person WHERE user_id=?",
    2,
)
if errors.Is(err, sql.ErrNoRows) {
    fmt.Println("person not found")
    return
}
if err != nil {
    fmt.Println("get failed:", err)
    return
}
fmt.Println("get succ:", person)
```

`Get` 将一行结果映射到结构体或标量，没有记录时返回 `sql.ErrNoRows`。它不会替你验证查询条件是否唯一，应使用主键或唯一条件保证业务上的唯一性。

### 5.4 UPDATE 与 DELETE

修改记录：

```go
r, err := Db.Exec("UPDATE person SET email=? WHERE user_id=?", "new@qq.com", 2)
if err != nil {
    fmt.Println("update failed:", err)
    return
}
rows, err := r.RowsAffected()
if err != nil {
    fmt.Println("get rows affected failed:", err)
    return
}
fmt.Println("rows affected:", rows)
```

删除的调用方式相同，将上面的 `Exec` 替换为：

```go
r, err := Db.Exec("DELETE FROM person WHERE user_id=?", 2)
```

- `RowsAffected()` 返回影响行数，类型为 `int64`。
- 删除影响 `0` 行通常表示没有匹配记录，并非 SQL 执行错误。
- MySQL 驱动默认情况下，UPDATE 返回实际改变的行数；值没有变化时也可能是 `0`。配置 `clientFoundRows=true` 后，返回匹配行数。
- 修改和删除前确认 `WHERE` 条件，避免误操作整张表；练习时只操作测试数据。

### 5.5 常用 API 对照

| API | 用途 | 注意事项 |
| --- | --- | --- |
| `Exec` | 增删改 | 检查错误，再读取 ID 或影响行数 |
| `Get` | 查询单条 | 目标通常是结构体指针；处理 `sql.ErrNoRows` |
| `Select` | 查询多条 | 目标是切片指针；无记录时结果为空 |
| `Queryx` | 逐行处理结果集 | 自行关闭 `Rows`，并在遍历后检查 `Rows.Err()` |

`Get`、`Select` 内部负责关闭结果集；自行使用 `Query` / `Queryx` 时，成功后应及时 `defer rows.Close()`。

## 6. MySQL 事务基础

### 6.1 为什么需要事务

一次业务操作可能包含多条 SQL，例如转账需要“扣款”和“入账”。如果第一条成功、第二条失败，而两条语句分别自动提交，就会产生业务上的不一致。

事务将多条操作组成一个整体：

```text
开启事务 → 执行 SQL 1 → 执行 SQL 2 → 全部成功则提交
                     ↘ 任一步失败则回滚
```

本节使用两次 INSERT 演示：要么两条记录一起提交，要么已执行的插入也撤销。

### 6.2 ACID 四个特性

| 特性 | 含义 |
| --- | --- |
| 原子性（Atomicity） | 事务中的操作作为整体提交或回滚 |
| 一致性（Consistency） | 事务前后满足约束和业务规则；也依赖正确的表设计与业务逻辑 |
| 隔离性（Isolation） | 并发事务之间按隔离级别控制可见性与相互影响 |
| 持久性（Durability） | 成功提交的结果具有持久性保障，具体也受日志刷盘等配置影响 |

### 6.3 MySQL 中的事务语句

```sql
START TRANSACTION;

INSERT INTO person(username, sex, email)
VALUES ('tx_demo_1', 'man', 'tx1@example.com');

INSERT INTO person(username, sex, email)
VALUES ('tx_demo_2', 'woman', 'tx2@example.com');

COMMIT;
-- 若要撤销，应在 COMMIT 前改为执行 ROLLBACK。
```

MySQL 默认启用自动提交：事务外的语句通常各自提交。开启显式事务后，需要明确执行 `COMMIT` 或 `ROLLBACK`。

注意：

- 一条 SQL 报错不等于整个事务一定已自动回滚；应用程序仍需正确结束事务。
- `ROLLBACK` 不能撤销已经提交的事务。
- 不要把 `CREATE TABLE`、`ALTER TABLE`、`TRUNCATE TABLE` 等 DDL 当作普通可回滚 DML，它们通常会触发隐式提交。
- 回滚不保证归还已分配的自增 ID，因此 ID 有空洞是正常现象。

### 6.4 隔离级别

| 隔离级别 | 脏读 | 不可重复读 | 幻读（按标准概念） |
| --- | --- | --- | --- |
| READ UNCOMMITTED（读未提交） | 可能 | 可能 | 可能 |
| READ COMMITTED（读已提交） | 避免 | 可能 | 可能 |
| REPEATABLE READ（可重复读） | 避免 | 避免 | 标准允许，InnoDB 有额外机制 |
| SERIALIZABLE（串行化） | 避免 | 避免 | 避免 |

- **脏读**：读到其他事务尚未提交的数据。
- **不可重复读**：同一事务重复读取同一行时，看到其他事务已提交的修改。
- **幻读**：重复按范围查询时，符合条件的记录集合发生变化。

InnoDB 默认通常为 `REPEATABLE READ`，实际配置可通过 MySQL 8 的 `SELECT @@transaction_isolation;` 检查。其普通一致性读使用 MVCC 快照，范围锁定读等场景会使用间隙锁 / next-key lock，不能简单理解为“可重复读一定会发生幻读”，也不能把快照读和当前读混为一谈。

Go 中可通过 `BeginTx` / `BeginTxx` 传入 `sql.TxOptions` 指定隔离级别；不指定时使用数据库默认设置。隔离级别越高并不总是越好，应结合一致性需求、锁等待和并发性能选择。

## 7. Go 中使用事务

### 7.1 当前事务代码的执行过程

`musql_transaction.go` 的流程是：

1. `conn, err := Db.Begin()` 开启事务。
2. 第一次 `conn.Exec(...)` 插入记录，读取 `LastInsertId()`。
3. 第二次 `conn.Exec(...)` 再次插入记录，读取 `LastInsertId()`。
4. 中间失败时调用 `conn.Rollback()` 并返回。
5. 最后调用 `conn.Commit()`。

虽然变量名叫 `conn`，但 `Db.Begin()` 返回的是 **`*sql.Tx` 事务对象**，不是普通连接；它在事务期间绑定一条连接。更推荐命名为 `tx`。

| 调用 | 说明 |
| --- | --- |
| `Db.Begin()` | 开启事务，返回标准库 `*sql.Tx` |
| `Db.Beginx()` | 开启事务，返回 `*sqlx.Tx`，可使用 `Get`、`Select` 等扩展 |
| `tx.Exec(...)` | 在当前事务内执行增删改 |
| `tx.Commit()` | 提交事务，必须检查错误 |
| `tx.Rollback()` | 撤销尚未提交的修改并结束事务 |

**事务内的 SQL 必须通过同一个 `tx` 执行。** 开启事务后再调用 `Db.Exec`，不会自动加入该事务，可能使用连接池中的另一条连接，无法由这个 `tx.Rollback()` 撤销。

### 7.2 当前代码的改进点

- 使用 `Ping` 或 `Connect` 检查连接，避免初始化失败后继续执行。
- 开启事务后立即 `defer tx.Rollback()`，防止新增错误分支时漏掉回滚。
- 检查 `Commit()` 的返回错误，不能默认提交一定成功。
- 当前两次 `insert succ` 都在提交前打印，只能说明 SQL 执行成功，**不代表事务已经提交**。
- 显式回滚时可检查并记录回滚错误；保留原始执行错误，避免错误原因被覆盖。
- 在程序退出时关闭 `Db`，不要用关闭连接池代替结束事务。

### 7.3 推荐的事务写法

下面可作为 `musql_transaction.go` 的改进参考：保留原有导入、`Db` 定义，并先按第 4 节改进初始化，再用以下函数替换原 `main()`。

```go
func insertTwoPeople(db *sqlx.DB) (int64, int64, error) {
    tx, err := db.Begin()
    if err != nil {
        return 0, 0, fmt.Errorf("begin transaction: %w", err)
    }
    // 兜底清理：出错或 panic 时尝试回滚。
    // 提交成功后调用会返回 sql.ErrTxDone，不会撤销已提交的数据。
    defer tx.Rollback()

    r, err := tx.Exec(
        "INSERT INTO person(username, sex, email) VALUES (?, ?, ?)",
        "stu001", "man", "stu01@qq.com",
    )
    if err != nil {
        return 0, 0, fmt.Errorf("insert first person: %w", err)
    }
    firstID, err := r.LastInsertId()
    if err != nil {
        return 0, 0, fmt.Errorf("get first id: %w", err)
    }

    r, err = tx.Exec(
        "INSERT INTO person(username, sex, email) VALUES (?, ?, ?)",
        "stu002", "woman", "stu02@qq.com",
    )
    if err != nil {
        return 0, 0, fmt.Errorf("insert second person: %w", err)
    }
    secondID, err := r.LastInsertId()
    if err != nil {
        return 0, 0, fmt.Errorf("get second id: %w", err)
    }

    if err := tx.Commit(); err != nil {
        return 0, 0, fmt.Errorf("commit transaction: %w", err)
    }
    return firstID, secondID, nil
}

func main() {
    defer Db.Close()

    firstID, secondID, err := insertTwoPeople(Db)
    if err != nil {
        fmt.Println("transaction failed:", err)
        return
    }
    fmt.Println("transaction committed:", firstID, secondID)
}
```

这里的延迟回滚用于兜底，示例省略了回滚错误日志。实际项目可在清理函数中记录非 `sql.ErrTxDone` 的回滚错误。

注意：提交过程中如果连接断开，客户端可能无法确认服务端是否已提交。**`Commit` 返回错误不应一概理解为“数据肯定未落库”**，应结合业务唯一标识核实结果，并通过幂等设计避免盲目重试造成重复数据。

### 7.4 事务使用原则

- 事务尽量短，不要在持锁期间等待用户输入或执行耗时的外部网络请求。
- 及时提交或回滚，否则会占用连接，并可能长时间持锁。
- 并发更新可能出现锁等待、死锁。需要重试时，应针对可重试错误、有界地重试整个业务事务，并考虑幂等性。
- 生产代码可使用带 `context.Context` 的 `BeginTxx`、`ExecContext` 等 API 设置超时和取消信号。
- 事务只保护参与该事务的数据库操作，不会自动回滚已发送的邮件、HTTP 请求等外部副作用。

## 8. 运行与验证

以下操作仅在本地测试数据库进行。

### 8.1 运行当前事务示例

在 `Day6` 目录执行：

```bash
go run musql_transaction.go
```

原始代码输出类似：

```text
insert succ: 1
insert succ: 2
```

ID 由数据库自增状态决定，不一定从 `1` 开始。原始输出发生在提交前，且代码没有检查提交错误，所以还需在 MySQL 客户端验证：

```sql
SELECT user_id, username, sex, email
FROM gotest.person
ORDER BY user_id DESC;
```

当前源代码每次成功提交会新增两条 `stu001`；改进示例则新增 `stu001` 和 `stu002`。

### 8.2 运行查询示例

`mysql.go` 目前全部被注释，需要先恢复其中的 Go 代码，再执行：

```bash
go run mysql.go
```

两个文件都含有 `main()`、`init()`、`Db` 和同名结构体。恢复查询文件后，应**按文件分别运行**，不要使用 `go run .` 或同时传入两个文件，否则会出现重复声明。后续可将两个示例拆分到不同子目录。

### 8.3 验证失败后整体回滚

为了明确观察回滚，可在事务示例的本地副本中做一次故障实验：

1. 将两次插入的 `username` 都临时改为独特测试标记，例如 `tx_rollback_demo_01`。
2. 在独立 MySQL 客户端记录测试前数量：

   ```sql
   SELECT COUNT(*) FROM gotest.person
   WHERE username = 'tx_rollback_demo_01';
   ```

3. 仅将**第二条 INSERT 的列名** `email` 临时改为不存在的 `email_not_exists`，其余保持不变。
4. 运行事务程序：第一条插入执行成功，第二条因字段不存在而失败，触发回滚。
5. 再查询同一标记的数量，应与运行前一致，说明第一条插入也被撤销。
6. 恢复正确列名后再运行，成功提交后数量应增加 `2`。

使用原始代码时，第一条可能仍会打印 `insert succ`，但回滚后数据不会保留。这正好说明“语句成功”与“事务提交成功”的区别。自增 ID 可能跳号，不影响回滚结论。

## 9. 常见问题

| 现象 | 检查方向 |
| --- | --- |
| `connection refused` | MySQL 是否启动，地址和端口是否正确 |
| `Access denied` | 用户名、密码以及账号权限是否正确 |
| `Unknown database 'gotest'` | 是否提前创建了数据库 |
| 表不存在或列不存在 | 表结构、数据库名和 SQL 是否一致 |
| 调用 `Db` 方法时出现空指针错误 | 初始化是否失败后仍继续执行 |
| `Select` 返回空结果 | 查询条件是否匹配，ID 为 `2` 的记录是否存在 |
| `Get` 返回 `sql.ErrNoRows` | 没有匹配记录，应作为业务上的“未找到”处理 |
| 两次插入相同用户名却不报错 | 当前表的 `username` 没有唯一约束 |
| 回滚后仍有新数据 | 是否误用了 `Db.Exec`、是否已提交、表是否为 InnoDB |
| 事务看似成功但数据没有保留 | 是否遗漏 `Commit` 或未检查其错误 |
| 提交或回滚后继续使用 `tx` 报错 | 事务已结束，需要开启新事务 |
| 回滚后自增 ID 不连续 | 正常现象，自增值分配不会随事务完全撤销 |
| `go run mysql.go` 报缺少 package 等语法错误 | 当前查询文件全部被注释，需先恢复代码 |
| 恢复查询文件后运行目录报重复声明 | 两个示例都有同名定义，应分别按文件运行 |

## 10. 本节总结

Go 操作 MySQL 的基本流程：

**安装驱动和 sqlx → 建库建表 → 初始化连接池并检查连通性 → 参数化执行 SQL → 处理结果与错误 → 关闭资源。**

事务的基本流程：

**Begin → defer Rollback 兜底 → 全部使用 tx 执行 SQL → 检查每一步错误 → Commit 并检查错误 → 报告提交成功。**

重点掌握：

1. 匿名导入驱动、DSN、`Open` 与 `Ping` 的区别。
2. `db` 标签、`Exec`、`Get`、`Select` 的适用场景。
3. `LastInsertId()`、`RowsAffected()` 与参数占位符的使用。
4. 事务的 ACID 特性，以及 InnoDB、自动提交和隔离级别的基本概念。
5. `Db` 是连接池入口，`tx` 是事务；事务内不能混用两者执行 SQL。
6. 语句执行成功不等于事务提交成功；通过故障实验验证整体回滚。
