# Day 2：Go 网络编程——TCP 与 UDP


## 1. TCP 与 UDP 的区别

TCP（Transmission Control Protocol，传输控制协议）和 UDP（User Datagram Protocol，用户数据报协议）都是传输层协议。TCP/IP 则是一组网络协议的统称，不是 TCP 单个协议的名称。

| 对比项 | TCP | UDP |
| --- | --- | --- |
| 连接 | 面向连接，通信前建立连接 | 无连接，无需握手 |
| 传输形式 | 字节流，不保留应用消息边界 | 数据报，保留报文边界 |
| 可靠性 | 提供有序传输、重传等机制 | 不保证送达、顺序或不重复 |
| 开销 | 有连接管理、确认和重传等开销 | 协议开销较小 |
| 常见应用 | HTTP/1.1、HTTP/2、文件传输 | DNS、音视频实时传输、游戏状态同步 |

> UDP 并不保证“实时”或一定比 TCP 快。应用可以根据需求自行处理丢包、乱序与重传；TCP 的可靠传输也不代表对端业务一定已经处理了数据，必要时仍需业务层确认。

## 2. Go 实现 TCP 通信

### 2.1 通信流程

服务端：

1. `net.Listen("tcp", address)`：监听地址和端口。
2. `listener.Accept()`：等待并接受客户端连接，返回 `net.Conn`。
3. `go handleConn(conn)`：为每个连接启动 goroutine，并发处理数据收发。
4. 连接处理结束时调用 `conn.Close()`；监听器退出时调用 `listener.Close()`。

客户端：

1. `net.Dial("tcp", address)`：建立连接。
2. 使用连接的 `Write` 和 `Read` 发送、接收数据。
3. 通信结束后关闭连接。

`Accept` 和没有可读数据时的 `Read` 通常会阻塞等待；goroutine 让服务端在处理已有连接时，仍能继续接受新连接。

### 2.2 TCP 消息边界：一次 Write 不等于一次 Read

TCP 传输的是字节流。例如客户端连续发送 `hello` 和 `world`，服务端可能一次读到 `helloworld`，也可能分多次读取。这通常被称为“粘包”或“拆包”，并不是数据损坏。

常见的消息划分方式：

- 固定长度：每条消息长度固定。
- 分隔符：例如每条消息以 `\n` 结束。
- 长度前缀：先发送消息长度，再发送消息体；可用 `io.ReadFull` 读取指定长度。

下面的回显示例约定：**一行是一条消息，以 `\n` 结尾**。客户端发送一行，服务端原样返回一行。相比教程直接使用固定缓冲区 `Read` 的入门示例，这里明确了消息边界。

### 2.3 TCP 服务端

建议保存为 `tcp/server/main.go`（相对于 `Day2` 目录；需自行创建，不覆盖现有 UDP 服务端）。

```go
package main

import (
    "bufio"
    "fmt"
    "io"
    "net"
)

func handleConn(conn net.Conn) {
    defer conn.Close()
    fmt.Println("客户端已连接：", conn.RemoteAddr())

    // 每个连接只创建一个 reader，避免丢弃其中预读但尚未消费的数据。
    reader := bufio.NewReader(conn)
    for {
        message, err := reader.ReadString('\n')
        if err != nil {
            if err != io.EOF {
                fmt.Println("读取失败：", err)
            }
            // 本示例只处理以换行符结尾的完整消息。
            // EOF 时若有未完成的一行，直接丢弃。
            return
        }
        fmt.Printf("收到 %s：%s", conn.RemoteAddr(), message)

        if _, err := io.WriteString(conn, message); err != nil {
            fmt.Println("回写失败：", err)
            return
        }
    }
}

func main() {
    listener, err := net.Listen("tcp", "127.0.0.1:20000")
    if err != nil {
        fmt.Println("监听失败：", err)
        return
    }
    defer listener.Close()
    fmt.Println("TCP 服务端监听 127.0.0.1:20000")

    for {
        conn, err := listener.Accept()
        if err != nil {
            fmt.Println("接受连接失败：", err)
            return
        }
        go handleConn(conn)
    }
}
```

### 2.4 TCP 客户端

建议保存为 `tcp/client/main.go`。

```go
package main

import (
    "bufio"
    "fmt"
    "io"
    "net"
    "os"
    "strings"
    "time"
)

func main() {
    conn, err := net.DialTimeout("tcp", "127.0.0.1:20000", 5*time.Second)
    if err != nil {
        fmt.Println("连接失败：", err)
        return
    }
    defer conn.Close()

    input := bufio.NewScanner(os.Stdin)
    reader := bufio.NewReader(conn)
    fmt.Println("输入消息后回车，输入 q 或 Q 退出：")

    for input.Scan() {
        text := input.Text() // Scanner 去掉行尾换行符。
        if strings.EqualFold(text, "q") {
            return
        }

        // 每轮发送前更新读写截止时间，避免一直等待服务端。
        if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
            fmt.Println("设置超时失败：", err)
            return
        }
        if _, err := io.WriteString(conn, text+"\n"); err != nil {
            fmt.Println("发送失败：", err)
            return
        }

        reply, err := reader.ReadString('\n')
        if err != nil {
            fmt.Println("接收失败：", err)
            return
        }
        fmt.Printf("服务端回复：%s", reply)
    }
    if err := input.Err(); err != nil {
        fmt.Println("读取输入失败：", err)
    }
}
```

### 2.5 运行与验证

将上面两个示例保存到建议路径后，在 `Day2` 目录打开两个终端：

```bash
# 终端 1：先启动服务端
go run ./tcp/server

# 终端 2：再启动客户端
go run ./tcp/client
```

客户端输入 `hello server` 并回车，应看到 `服务端回复：hello server`。输入 `q` 或 `Q` 退出客户端；服务端用 `Ctrl+C` 停止。

可以再开一个终端启动第二个客户端，验证服务端能同时处理多个连接。发送空行也会收到空行回复，因为实际发送了 `\n`，不会因为发送零字节而一直等待回包。

## 3. Go 实现 UDP 通信

UDP 不需要像 TCP 那样通过 `Accept` 建立连接。服务端读取数据报时同时获得发送方地址，再向该地址发送回复。

### 3.1 UDP 服务端

对应现有文件：`server/main.go`。

```go
package main

import (
    "fmt"
    "net"
)

func main() {
    socket, err := net.ListenUDP("udp", &net.UDPAddr{
        IP:   net.IPv4(0, 0, 0, 0),
        Port: 30000,
    })
    if err != nil {
        fmt.Println("监听失败：", err)
        return
    }
    defer socket.Close()

    var data [1024]byte
    for {
        n, addr, err := socket.ReadFromUDP(data[:])
        if err != nil {
            fmt.Println("读取失败：", err)
            return
        }
        fmt.Printf("data:%s addr:%v count:%d\n", data[:n], addr, n)

        if _, err := socket.WriteToUDP(data[:n], addr); err != nil {
            fmt.Println("发送失败：", err)
        }
    }
}
```

### 3.2 UDP 客户端

对应现有文件：`client/main.go`。下面是整理后的示例，使用 `127.0.0.1` 作为目标地址、`Printf` 格式化输出，并增加读写超时；现有源码未同步修改。

```go
package main

import (
    "fmt"
    "net"
    "time"
)

func main() {
    socket, err := net.DialUDP("udp", nil, &net.UDPAddr{
        IP:   net.IPv4(127, 0, 0, 1),
        Port: 30000,
    })
    if err != nil {
        fmt.Println("创建 UDP socket 失败：", err)
        return
    }
    defer socket.Close()

    if err := socket.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
        fmt.Println("设置超时失败：", err)
        return
    }
    if _, err := socket.Write([]byte("hello server")); err != nil {
        fmt.Println("发送失败：", err)
        return
    }

    data := make([]byte, 4096)
    n, addr, err := socket.ReadFromUDP(data)
    if err != nil {
        fmt.Println("接收失败：", err)
        return
    }
    fmt.Printf("recv:%s addr:%v count:%d\n", data[:n], addr, n)
}
```

`net.DialUDP` 的第二个参数为本地地址，传入 `nil` 表示由系统选择本地地址和端口。它指定了默认远端，但**不会执行 TCP 式握手，也不保证远端服务存在**。

### 3.3 运行与验证

在 `Day2` 目录中运行现有 UDP 程序：

```bash
# 终端 1
go run ./server

# 终端 2
go run ./client
```

服务端应回显收到的 `hello server`。注意现有客户端使用了 `fmt.Println("recv:%v ...", ...)`，占位符不会被替换；要得到上面示例的格式化输出，需要改为 `fmt.Printf`。本机测试的目标地址也应使用 `127.0.0.1`，而不是 `0.0.0.0`。

## 4. 关键 API 与注意事项

| API | 用途 |
| --- | --- |
| `net.Listen` / `Accept` | TCP 监听 / 接受连接 |
| `net.Dial` / `net.DialTimeout` | 建立连接 / 限制连接建立时间 |
| `net.Conn.Read` / `Write` | 通过连接读写字节 |
| `net.ListenUDP` / `net.DialUDP` | 创建监听 UDP socket / 指定默认远端 |
| `ReadFromUDP` / `WriteToUDP` | 接收数据报及来源地址 / 向指定地址发送数据报 |
| `SetDeadline` | 设置后续读写的绝对截止时间 |
| `Close` | 释放连接或 socket 资源 |

- **地址含义**：`127.0.0.1` 是本机回环地址；服务端绑定 `0.0.0.0` 表示监听所有本地 IPv4 接口。客户端连接远程服务时应填写服务端实际 IP 或域名，并检查防火墙。
- **只处理有效字节**：使用 `data[:n]`，不要把整个缓冲区转换成字符串；一般的 `Read` 可能同时返回 `n > 0` 和错误，应先处理有效数据，再判断错误。
- **UDP 缓冲区大小**：一个数据报若超过接收缓冲区，可能被截断，剩余部分不能像 TCP 字节流那样留到下一次读取。示例的 1024 字节不是 UDP 协议上限。
- **错误与资源管理**：监听、连接、读写都要检查错误；成功创建资源后及时安排 `defer Close()`。TCP 对端正常关闭发送方向后，读完已有数据会读到 `io.EOF`。
- **超时范围**：`DialTimeout` 只限制建立连接；读写需要单独设置 deadline。deadline 是绝对时间，不会因一次读写成功而自动刷新。
- **示例限制**：TCP 服务端的 `ReadString` 没有限制消息长度，客户端 `Scanner` 默认 token 上限约 64 KiB；生产环境需明确消息大小上限、服务端空闲超时、并发连接数限制与退出策略。

## 5. 动手练习

1. 同时启动两个 TCP 客户端，观察服务端打印的远端地址与端口。
2. 连续发送多行消息，理解“按行读取”和“按一次 Read 读取”的区别。
3. 不启动 UDP 服务端，运行带超时的 UDP 客户端，观察超时或系统返回的网络错误。
4. 思考：如果消息本身允许包含换行符，如何改用“长度前缀 + 消息体”协议？
