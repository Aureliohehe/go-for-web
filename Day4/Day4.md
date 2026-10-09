# Day 4：Go 并发编程——goroutine 与同步

## 1. 并发与并行

- **并发（Concurrency）**：在同一段时间内处理多个任务，强调任务的组织与交替推进，不要求同一时刻执行。
- **并行（Parallelism）**：在同一时刻执行多个任务，通常需要多个 CPU 核心。

单核可以通过调度实现并发，多核可以让并发任务并行执行。因此，“并发只能发生在单核上”并不准确。

例如，网络服务可以在等待客户端 A 的数据时处理客户端 B；即使只有一个 CPU 核心，也能并发服务多个连接。CPU 密集型任务能否通过并行提速，还取决于可用核心数、任务划分以及同步开销。

## 2. 线程、协程与 goroutine

| 概念 | 调度者与特点 |
| --- | --- |
| 操作系统线程 | 由操作系统调度，同一进程内的线程共享地址空间，各自有栈和执行状态 |
| 协程 | 通常由用户态运行时或库管理，许多实现采用协作式调度，但具体机制取决于实现 |
| goroutine | Go 运行时管理的轻量级执行单元，多路复用到操作系统线程上，支持抢占式调度 |

需要修正的几个认识：

- **goroutine 不是线程池**。它是执行任务的单位；Go runtime 负责管理 goroutine、线程和调度。
- goroutine 的调度由 runtime 完成，不需要业务代码自行实现，也不只是靠主动让出执行权。
- 每个 goroutine 有独立的栈，初始栈通常约为 **2 KiB**，可按需增长和缩小；具体数值属于实现细节，不是语言保证。
- 栈大小不等于 goroutine 的全部内存开销；运行时元数据、引用的对象等也会占用内存。
- goroutine 创建成本通常低于操作系统线程，但不是零成本。无限创建仍可能耗尽内存、连接或其他资源。
- 独立的栈不代表数据隔离：多个 goroutine 仍可能通过指针、切片、map 等访问同一份数据。

## 3. 启动 goroutine

在**函数调用**前添加 `go` 关键字：

```go
// 以下是语法片段，不是完整程序。
go hello(1)

go func(message string) {
    fmt.Println(message)
}("hello")
```

- `go f(x)` 会启动新的 goroutine 执行 `f`，当前 goroutine 不等待它结束。
- 函数值与参数在当前 goroutine 中求值，函数体在新 goroutine 中执行。
- 参数按值传递，但复制指针、切片或 map 不会自动复制底层数据。
- `go` 调用不能直接接收函数返回值；需要用 channel 等机制传回结果。
- 各 goroutine 的执行顺序不确定，不能依赖启动顺序推断完成顺序。
- `main` 函数返回时，整个程序退出，不会自动等待其他 goroutine，也不会替它们执行尚未运行的 `defer`。

> `time.Sleep` 只能让当前 goroutine 暂停一段时间，不能保证其他任务已经完成；应使用明确的同步机制。

## 4. 运行现有示例：sync.WaitGroup

本目录的 [`go_async.go`](./go_async.go) 演示了启动并等待 10 个 goroutine：

```go
package main

import (
    "fmt"
    "sync"
)

var wg sync.WaitGroup

func hello(i int) {
    defer wg.Done() // 任务结束时将计数减 1。
    fmt.Println("Hello Goroutine!", i)
}

func main() {
    for i := 0; i < 10; i++ {
        wg.Add(1) // 必须在启动 goroutine 之前登记任务。
        go hello(i)
    }
    wg.Wait() // 阻塞，直到计数归零。
}
```

在 `Day4` 目录执行：

```bash
go run ./go_async.go
go run -race ./go_async.go
```

预期输出 10 行 `Hello Goroutine!`，编号 `0` 到 `9` 各出现一次，顺序不固定。`-race` 用于检查执行过程中发生的数据竞争；需要工具链和平台支持，通常还需要启用 cgo 并安装 C 编译器。

### 4.1 WaitGroup 的使用规则

| 方法 | 作用 |
| --- | --- |
| `Add(n)` | 增加任务计数；通常在启动任务之前调用 |
| `Done()` | 等价于 `Add(-1)`，表示一个任务结束 |
| `Wait()` | 等待计数归零 |

- 不要把 `Add(1)` 放进新启动的 goroutine，否则 `Wait()` 可能在登记前就返回。
- 用 `defer wg.Done()` 覆盖提前返回等路径；多调用 `Done()` 导致负计数会 panic。
- `WaitGroup` 首次使用后不能复制；需要传递时使用指针。
- 重用同一个 `WaitGroup` 管理下一批任务前，应确认上一批的所有 `Wait()` 已返回。
- `WaitGroup` 只负责等待，不限制并发数、不取消任务，也不收集错误。
- 它不代替互斥锁：多个任务同时修改共享变量，依然可能产生数据竞争。

工程代码通常将 `WaitGroup` 放在管理任务的函数内，减少全局状态。较新版本还提供 `WaitGroup.Go`（Go 1.25 起）；本笔记沿用 `Add` / `Done` / `Wait`，便于理解现有代码。

## 5. GMP 调度模型

GMP（也常写作 GPM）是 Go runtime 的调度模型，不是语言层面的 API 保证。

| 角色 | 含义 |
| --- | --- |
| G（goroutine） | 待执行的任务，保存栈、执行上下文、状态等信息 |
| M（machine） | runtime 对操作系统线程的表示，实际执行代码 |
| P（processor） | 执行普通 Go 代码所需的调度资源，包含本地可运行 G 队列等 |

```text
可运行的 G → P 的本地队列 / 全局队列
                       ↓ 调度
                 持有 P 的 M → 执行 G
```

### 5.1 调度的基本过程

1. 新建的 G 进入可运行状态，由调度器安排执行。
2. M 持有 P 时可以执行普通 Go 代码；每个 P 同一时刻由一个 M 使用，但二者不是永久绑定。
3. 调度器综合利用本地队列、全局队列、网络轮询和工作窃取等机制获取可运行任务。
4. P 缺少任务时，可以从其他 P 偷取部分可运行 G，提高资源利用率。
5. runtime 可以抢占长时间执行的 G，避免某个任务一直占用执行资源。

这只是概念模型；实际调度还有公平性检查等策略，并不是严格按“本地队列 → 全局队列 → 窃取”的固定顺序执行。

### 5.2 阻塞时会发生什么？

- **channel、锁、定时器等等待**：通常挂起当前 G，M 可以继续执行其他可运行 G。
- **可被网络轮询器管理的网络 I/O**：通常让 G 等待就绪事件，不必让一个线程始终阻塞在那里。
- **阻塞系统调用**：可能阻塞 M；runtime 可将 P 交给其他 M，让其他 G 继续执行。必要时才创建新线程，也可能复用已有线程。

原来的 M 在系统调用结束后，会尝试重新获取 P 或走后续调度流程，并不是“阻塞结束就一定被回收”。也不是所有 I/O 都能通过网络轮询器处理。

### 5.3 GOMAXPROCS

`GOMAXPROCS` 决定 P 的数量，也就是同一时刻能够并行执行普通 Go 代码的线程数上限。

```go
// 语法片段，需要导入 runtime。
current := runtime.GOMAXPROCS(0) // 查询当前值，不修改。
fmt.Println(current)
```

也可以通过环境变量进行实验：

```bash
GOMAXPROCS=1 go run ./go_async.go
GOMAXPROCS=2 go run ./go_async.go
```

即使设为 `1`，仍然存在并发，输出顺序仍不保证。它**不限制 goroutine 总数，也不限制操作系统线程总数**。默认值受 Go 版本、CPU 可用性、容器配额等影响，通常不需要手动修改。

## 6. channel：在 goroutine 之间通信

channel 是带类型的通信通道，既可以传递数据，也可以建立同步关系。

```go
// 语法片段：无缓冲通道需要其他 goroutine 配合收发。
ch := make(chan int)    // 无缓冲 channel。
buffer := make(chan int, 2) // 容量为 2 的缓冲 channel。

buffer <- 10           // 发送。
value := <-buffer      // 接收。
fmt.Println(value)
close(ch)              // 表示不会再发送，不是释放内存的必需操作。
```

- 无缓冲 channel：发送与接收双方就绪时才能完成数据交接。
- 有缓冲 channel：缓冲未满时可以发送，缓冲非空时可以接收；否则需要等待。
- `len(ch)` 是当前缓冲元素数量，`cap(ch)` 是容量。不要用 `len(ch)` 判断下一次操作是否一定安全，状态可能被其他 goroutine 改变。

### 6.1 完整示例：生产与消费

以下完整示例应分别保存到独立目录，例如本例保存为 `examples/channel/main.go`，运行 `go run ./examples/channel`。后续示例同理，避免把多个 `main` 放在同一个包中。

```go
package main

import "fmt"

func produce(out chan<- int) {
    defer close(out) // 发送方负责关闭。
    for i := 1; i <= 3; i++ {
        out <- i
    }
}

func main() {
    numbers := make(chan int)
    go produce(numbers)

    for n := range numbers {
        fmt.Println(n)
    }
}
```

预期依次输出 `1`、`2`、`3`。`range` 持续接收，直到 channel 关闭且缓冲数据被取完；如果发送方不关闭，它会继续等待。

`chan<- int` 表示只发送，`<-chan int` 表示只接收，可用于限制函数对通道的操作权限。

### 6.2 关闭与 nil channel

| 操作 | 行为 |
| --- | --- |
| 向已关闭 channel 发送 | panic |
| 重复关闭 channel | panic |
| 接收已关闭 channel | 先取完缓冲，之后立即返回元素类型零值 |
| `v, ok := <-ch` | `ok == false` 表示已关闭且没有剩余数据 |
| 向 nil channel 发送 / 从中接收 | 永久阻塞 |
| 关闭 nil channel | panic |

通常由发送方关闭 channel；多个发送方时，应由协调者确认所有发送都结束后再关闭。接收方不要擅自关闭仍可能被写入的通道。

并非所有 channel 都需要关闭：只有接收方需要“没有后续数据”的通知时才需要。关闭也不等于取消正在进行的业务任务。

## 7. select 与 context：超时和取消

`select` 等待多个通道操作：

- 有一个分支就绪时执行该分支。
- 多个分支同时就绪时，伪随机选择一个，不保证代码排列上的优先级。
- 都未就绪时阻塞；存在 `default` 时则立即执行 `default`。
- nil channel 对应的分支不会就绪，可以用来禁用分支。

下面的完整示例模拟耗时任务，在 100 毫秒后发出取消信号，并等待任务真正退出：

```go
package main

import (
    "context"
    "fmt"
    "sync"
    "time"
)

func main() {
    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel() // 即使提前完成，也应释放关联资源。

    var wg sync.WaitGroup
    wg.Add(1)
    go func() {
        defer wg.Done()
        timer := time.NewTimer(time.Second)
        defer timer.Stop()

        select {
        case <-timer.C:
            fmt.Println("任务完成")
        case <-ctx.Done():
            fmt.Println("任务退出：", ctx.Err())
        }
    }()
    wg.Wait()
}
```

通常会输出 `任务退出： context deadline exceeded`。超时并不保证代码在准确的 100 毫秒时执行；如果调度延迟使两个分支都就绪，也可能选择完成分支。

**context 只传递取消信号，不会强制终止 goroutine。** 任务必须主动检查 `ctx.Done()`，或调用支持 context 的 API；`WaitGroup` 则负责等待任务退出。

可能长期阻塞的发送操作也应考虑取消路径：

```go
// 语法片段：位于可直接 return 的函数内。
select {
case results <- result:
case <-ctx.Done():
    return
}
```

避免在循环里用空 `default` 不断轮询，这可能造成 CPU 空转。已关闭 channel 的接收会一直就绪，循环处理时应退出或将该通道变量设为 nil，防止忙循环。

## 8. 共享数据与互斥锁

数据竞争指多个 goroutine 未经同步访问同一内存位置，且至少有一个访问是写入。例如 `counter++` 包含读取、加一、写回，不是一个不可分割的操作。

下面用 `sync.Mutex` 保护共享计数器：

```go
package main

import (
    "fmt"
    "sync"
)

func main() {
    var (
        wg      sync.WaitGroup
        mu      sync.Mutex
        counter int
    )

    for i := 0; i < 10; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for j := 0; j < 1000; j++ {
                mu.Lock()
                counter++
                mu.Unlock()
            }
        }()
    }
    wg.Wait()
    fmt.Println(counter) // 固定输出 10000。
}
```

- `Mutex` 的零值可直接使用，首次使用后不能复制。
- 所有并发访问共享数据的路径都要遵循同一套同步规则，包括读取。
- 上例最终读取发生在 `Wait()` 返回后，此时所有写入均已完成，因此不必再加锁。
- 普通 map 不能在无同步保护下并发读写或并发写入；可能触发数据竞争甚至运行时致命错误。
- `sync.RWMutex` 适合某些读多写少场景，但不保证比 `Mutex` 快，应结合测量选择。
- 简单计数也可以使用 `sync/atomic`，但不要混用原子访问与无保护的普通访问。

channel 适合传递数据、结果或转移任务所有权；锁适合保护共享状态。通过 channel 发送指针后，如果双方继续修改同一个对象，仍可能发生数据竞争。

## 9. 常见陷阱与排查

| 问题 | 原因与处理 |
| --- | --- |
| 主程序提前结束 | `main` 不会等待其他 goroutine；使用 `WaitGroup` 等同步机制 |
| 输出顺序不稳定 | 调度顺序不确定；需要顺序时显式设计协调机制 |
| 用 Sleep 等待完成 | 依赖耗时猜测，不可靠；改用完成通知 |
| goroutine 泄漏 | 通道、锁或 I/O 等待永远无法结束；设计退出路径、超时与取消 |
| channel 死锁 | 例如同一个 goroutine 向无缓冲 channel 发送后才接收；检查收发是否能配对 |
| 等待与收集结果互相卡住 | 主 goroutine 先 `Wait()`，工作 goroutine 却阻塞在发送结果、无法 `Done()`；应并发接收结果 |
| 闭包捕获循环变量 | 本例 `go hello(i)` 已显式传值；Go 1.22 起、按新语言语义编译时，循环中用 `:=` 声明的变量按迭代独立，预先声明后用 `=` 赋值的变量仍会共享 |
| 子 goroutine panic | 未恢复的 panic 会终止程序；另一个 goroutine 中的 `recover` 无法捕获，须在发生 panic 的 goroutine 内通过 deferred 函数恢复 |
| 无限启动任务 | goroutine 轻量但资源有限；使用固定数量 worker 或信号量限制并发 |

限制并发时，如果希望连 goroutine 数量也受控，应在启动 goroutine **之前**取得令牌，而不是先启动海量 goroutine 再让它们排队。

排查建议：

1. 先明确每个 goroutine 的启动者、退出条件以及由谁等待它。
2. 明确每个 channel 的发送方、接收方和关闭方。
3. 使用 `go run -race ./go_async.go`；有测试时可在本目录执行 `go test -race .`。
4. race detector 只能发现实际运行路径中的竞争，未报告不代表一定安全；它也不能代替死锁或泄漏分析。
5. 区分数据竞争与业务逻辑错误：没有数据竞争，也可能因错误的执行顺序得到错误结果。

## 10. 练习与总结

建议练习：

1. 多次运行 `go_async.go`，观察输出顺序；分别设置 `GOMAXPROCS=1`、`2`，解释为什么仍需等待任务结束。
2. 修改 channel 示例，发送整数并收集平方结果，明确由谁关闭结果通道。
3. 在计数器示例中临时移除锁，使用 `-race` 观察报告，再恢复同步保护。
4. 实现固定 3 个 worker 处理 10 个任务：发送结束后关闭任务通道，等待 worker 结束，再关闭结果通道；主 goroutine 同时消费结果，避免互相等待。
5. 为 worker 增加 context 取消，并确保发送方、worker 和结果接收方都能退出。

记住四个问题：**谁执行（goroutine）、谁调度（GMP）、如何通信和保护数据（channel / 锁）、如何结束和等待（context / WaitGroup）**。

## 参考资料

优先以官方文档确认 API 行为；调度实现细节可能随 Go 版本变化。

- [Go 语言规范：Go statements](https://go.dev/ref/spec#Go_statements)
- [Go 语言规范：Channel types](https://go.dev/ref/spec#Channel_types)
- [sync 包](https://pkg.go.dev/sync)
- [context 包](https://pkg.go.dev/context)
- [runtime 包与 GOMAXPROCS](https://pkg.go.dev/runtime#GOMAXPROCS)
- [Go 内存模型](https://go.dev/ref/mem)
- [数据竞争检测器](https://go.dev/doc/articles/race_detector)
- [Go Concurrency Patterns: Pipelines and cancellation](https://go.dev/blog/pipelines)
- [Go 语言中文网教程（TopGoer）](https://www.topgoer.com/)
- [小林 coding：Go 面试题](https://www.xiaolincoding.com/interview/golang.html)
- [Golang 编程指南](https://golangguide.top/golang/)
- [代码随想录 Go 笔记](https://notes.kamacoder.com/go/)
