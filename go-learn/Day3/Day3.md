# Day 3：Go HTTP 编程


## 1. Web 与 HTTP 的基本原理

HTTP 是应用层协议，用于客户端与服务端之间的请求、响应通信。Go 标准库 `net/http` 已经封装了连接管理和 HTTP 报文解析，不需要像 TCP 示例那样自己设计消息分隔符。

典型流程：

```text
客户端 → 建立或复用连接 → 发送 HTTP 请求
                              ↓
服务端 → 路由匹配 → Handler 处理 → 返回状态码、响应头和响应体
```

- HTTP/1.1、HTTP/2 通常运行在 TCP 之上；HTTPS 在此基础上使用 TLS 加密并验证服务端身份。
- HTTP/3 基于 QUIC（使用 UDP），所以不能笼统地说所有 HTTP 都基于 TCP。
- 一次请求不一定新建一次连接；HTTP 可以复用连接，HTTP/2 还支持在同一连接上多路复用请求。
- HTTP 是无状态协议：多次请求的业务关联需要应用通过 Cookie、Session 或 Token 等机制维护，而不是靠 TCP 连接自动保存登录状态。

### 1.1 请求与响应报文

以下是 HTTP/1.1 的文本示意，空行用于分隔头部与消息体；实际行结束符是 `\r\n`。HTTP/2、HTTP/3 使用不同的帧格式。

```http
GET /go?name=Alice HTTP/1.1
Host: 127.0.0.1:8000
Accept: text/plain

```

```http
HTTP/1.1 200 OK
Content-Type: text/plain; charset=utf-8
Content-Length: 11

www.5lmh.com
```

请求包含方法、请求目标、协议版本、请求头以及可选的请求体；响应包含状态码、响应头以及可选的响应体。`Content-Length` 按字节计数，并不是所有响应都必须携带它，通常交给 `net/http` 处理。

### 1.2 常见方法与状态码

| 方法 | 常见用途 |
| --- | --- |
| GET | 获取资源，不应产生业务上的修改 |
| HEAD | 获取与 GET 对应的响应头，不返回响应体 |
| POST | 提交数据、创建资源或执行操作 |
| PUT | 创建或整体替换指定资源 |
| PATCH | 部分更新资源 |
| DELETE | 删除资源 |
| OPTIONS | 查询支持的通信选项，也常用于 CORS 预检 |

现有服务端注释中的 `UPDATE` 不是常见的标准 HTTP 更新方法，应理解为使用 `PUT` 或 `PATCH`。方法表达的是语义约定，业务代码仍需正确实现。

| 状态码 | 含义 |
| --- | --- |
| 200 / 201 / 204 | 成功 / 已创建 / 成功但无响应体 |
| 301 / 302 / 307 / 308 | 重定向；307、308 要求保留请求方法和请求体 |
| 400 / 401 / 403 | 请求错误 / 缺少有效认证 / 拒绝访问 |
| 404 / 405 | 资源不存在 / 方法不允许 |
| 413 / 415 | 请求体过大 / 不支持的媒体类型 |
| 500 / 503 | 服务端内部错误 / 服务暂不可用 |

代码中优先使用 `http.MethodGet`、`http.StatusBadRequest` 等常量。

## 2. 运行本目录现有示例

所有命令均在 `Day3` 目录下执行。

终端一启动服务端：

```bash
go run ./server/httpserver.go
```

终端二运行客户端，或使用 curl：

```bash
go run ./client/cl.go
curl -i http://127.0.0.1:8000/go
curl -i 'http://127.0.0.1:8000/go?name=Alice'
curl -i http://127.0.0.1:8000/missing
```

预期：`/go` 返回 `200 OK` 和 `www.5lmh.com`；现有处理函数不会使用查询参数；未注册的 `/missing` 返回 `404`。按 `Ctrl+C` 停止服务端。

`127.0.0.1:8000` 只监听本机回环地址；`:8000` 通常监听所有可用本地接口，可能允许外部访问，仍受防火墙和网络配置影响。本地学习优先使用回环地址。

## 3. 理解服务端：路由与 Handler

现有服务端的核心是：

```go
http.HandleFunc("/go", myHandler)
http.ListenAndServe("127.0.0.1:8000", nil)
```

- `http.HandleFunc`：将函数注册到全局 `http.DefaultServeMux`。
- `http.ListenAndServe`：监听 TCP 地址并提供 HTTP 服务；第二个参数为 `nil` 时使用默认路由器。它会阻塞，并且返回时总有非空错误，不能忽略。
- `http.Handler`：具有 `ServeHTTP(http.ResponseWriter, *http.Request)` 方法的接口。
- `http.HandlerFunc`：把相同签名的普通函数适配为 `http.Handler`。
- `http.NewServeMux()`：创建独立路由器，减少全局状态影响，便于测试和组合。

Handler 的两个参数分别代表输出和输入：

```go
func myHandler(w http.ResponseWriter, r *http.Request)
```

### 3.1 读取请求

| 字段或方法 | 用途 |
| --- | --- |
| `r.Method` | 请求方法，例如 `GET` |
| `r.URL.Path` | 路径，例如 `/go`，不包含查询参数 |
| `r.URL.Query().Get("name")` | 获取查询参数；未提供和空值都会返回空字符串 |
| `r.Header.Get("Content-Type")` | 获取请求头，头名称不区分大小写 |
| `r.Host` | 目标主机；入站请求的 Host 应从这里读取 |
| `r.RemoteAddr` | 直接连接对端的地址，通常包含端口 |
| `r.Body` | 请求体数据流，需读取或解码才能获得内容 |
| `r.Context()` | 请求上下文，用于取消、超时和向下游传递信号 |

`fmt.Println(r.Body)` 只是打印流对象，并不等于读取正文。对于服务端入站请求，`net/http` 会关闭请求体，Handler 通常不必自行关闭。读取不可信请求体前应使用 `http.MaxBytesReader` 限制大小。

反向代理后的 `RemoteAddr` 可能是代理地址，不能随意信任客户端自填的 `X-Forwarded-For`。日志也不应直接输出密码、Cookie、Authorization 等敏感信息。

### 3.2 写入响应

顺序是：**设置响应头 → 写状态码 → 写响应体**。

```go
w.Header().Set("Content-Type", "text/plain; charset=utf-8")
w.WriteHeader(http.StatusOK)
_, err := w.Write([]byte("hello"))
if err != nil {
    log.Printf("写响应失败: %v", err)
}
```

首次 `Write` 前未调用 `WriteHeader`，就会隐式发送 `200 OK`。普通最终响应状态只能确定一次；写入正文后再设置状态码或普通响应头已经太晚。写失败时通常也无法再改成 `500`，应记录错误。

`http.Error(w, "bad request", http.StatusBadRequest)` 可快捷写入文本错误响应，但不会结束函数，后面通常需要 `return`。Handler 返回后不能继续使用 `w`，也不要让后台 goroutine 在返回后写响应。

### 3.3 路由规则与版本差异

- `"/go"` 匹配该路径，不自动匹配 `/go/abc`；不带方法的模式允许所有方法进入 Handler。
- `"/static/"` 匹配路径子树；访问 `/static` 通常会重定向到 `/static/`。
- `"/"` 是兜底子树模式，并非只匹配首页。
- Go 1.22 起支持 `"GET /go"`、`"GET /users/{id}"`，通过 `r.PathValue("id")` 读取路径参数；`GET` 模式也匹配 `HEAD`。
- Go 1.22 起 `"/{$}"` 可以只匹配根路径；无匹配方法时，路由器可以自动返回 `405` 和 `Allow` 头。
- 重复注册或存在冲突的模式可能导致 panic。

网上旧教程可能基于 Go 1.21 或更早版本，需注意路由行为差异；`GODEBUG=httpmuxgo121=1` 也会启用旧行为。下面的完整示例使用传统路径模式，并显式检查方法。

## 4. 改进示例：GET 与 JSON POST 服务端

可将下面代码单独保存为 `examples/server/main.go`，执行 `go run ./examples/server/main.go`。它与原服务端共用 8000 端口，运行前先停止原服务端。

示例提供 `/go?name=Alice` 和 `/echo`，并演示方法检查、请求体限额、JSON 校验和服务端超时。

```go
package main

import (
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "log"
    "mime"
    "net/http"
    "time"
)

func goHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet && r.Method != http.MethodHead {
        w.Header().Set("Allow", "GET, HEAD")
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
        return
    }
    name := r.URL.Query().Get("name")
    if name == "" {
        name = "Go"
    }
    w.Header().Set("Content-Type", "text/plain; charset=utf-8")
    if r.Method == http.MethodHead {
        return
    }
    if _, err := fmt.Fprintf(w, "Hello, %s!\n", name); err != nil {
        log.Printf("写响应失败: %v", err)
    }
}

func echoHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        w.Header().Set("Allow", "POST")
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
        return
    }
    mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
    if err != nil || mediaType != "application/json" {
        http.Error(w, "expected application/json", http.StatusUnsupportedMediaType)
        return
    }

    r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 最多 1 MiB
    var input struct {
        Message string `json:"message"`
    }
    decoder := json.NewDecoder(r.Body)
    decoder.DisallowUnknownFields()
    decodeErr := decoder.Decode(&input)
    if decodeErr == nil {
        // 只允许一个 JSON 值，拒绝后面追加的 JSON 或非法内容。
        var extra any
        if err := decoder.Decode(&extra); err != io.EOF {
            if err == nil {
                err = errors.New("multiple JSON values")
            }
            decodeErr = err
        }
    }
    if decodeErr != nil {
        var sizeErr *http.MaxBytesError
        if errors.As(decodeErr, &sizeErr) {
            http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
        } else {
            http.Error(w, "invalid JSON body", http.StatusBadRequest)
        }
        return
    }
    if input.Message == "" {
        http.Error(w, "message is required", http.StatusBadRequest)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    if err := json.NewEncoder(w).Encode(input); err != nil {
        log.Printf("写 JSON 响应失败: %v", err)
    }
}

func main() {
    mux := http.NewServeMux()
    mux.HandleFunc("/go", goHandler)
    mux.HandleFunc("/echo", echoHandler)
    server := &http.Server{
        Addr:              "127.0.0.1:8000",
        Handler:           mux,
        ReadHeaderTimeout: 5 * time.Second,
        ReadTimeout:       10 * time.Second,
        WriteTimeout:      10 * time.Second,
        IdleTimeout:       60 * time.Second,
    }
    log.Printf("监听 http://%s", server.Addr)
    if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
        log.Fatal(err)
    }
}
```

JSON 字段只有导出的字段才能被正常编解码，结构体标签控制 JSON 键名。仅一次 `Decode` 不保证请求体里只有一个 JSON 值，所以示例再次解码并检查 `io.EOF`。

这些超时值只适合演示：`ReadHeaderTimeout` 限制读请求头的时间，`ReadTimeout` 覆盖整个请求读取，`WriteTimeout` 限制响应写入，`IdleTimeout` 限制 keep-alive 等待下一次请求的时间。它们不是可强制停止业务计算的总执行时限；大文件上传、流式响应应另行设计。

## 5. 改进示例：可靠的 HTTP 客户端

现有 `client/cl.go` 有两个关键问题：

1. 忽略 `http.Get` 的错误，连接失败时可能在 `resp.Body` 处触发空指针 panic。
2. 循环在第一次正常 `Read` 后就 `break`，不能保证读取完整正文；即使响应不足 1024 字节，一次读取也不保证拿到全部内容。

对于小响应，可以使用 `io.ReadAll`；对不可信响应应加上大小限制。可将下面代码保存为 `examples/client/main.go`，服务端运行时执行 `go run ./examples/client/main.go`。

```go
package main

import (
    "context"
    "fmt"
    "io"
    "log"
    "net/http"
    "time"
)

func run() error {
    // 在实际应用中共享、复用 Client，而不是每次请求都创建。
    client := &http.Client{Timeout: 5 * time.Second}
    ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
    defer cancel()

    req, err := http.NewRequestWithContext(ctx, http.MethodGet,
        "http://127.0.0.1:8000/go?name=Alice", nil)
    if err != nil {
        return err
    }
    req.Header.Set("Accept", "text/plain")
    resp, err := client.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close() // 确认请求成功后再访问 Body

    const maxBody = 1 << 20
    body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
    if err != nil {
        return err
    }
    if len(body) > maxBody {
        return fmt.Errorf("响应体超过 %d 字节", maxBody)
    }
    if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        return fmt.Errorf("HTTP 请求失败: %s", resp.Status)
    }
    fmt.Println(resp.Status)
    fmt.Println(resp.Header.Get("Content-Type"))
    fmt.Print(string(body))
    return nil
}

func main() {
    if err := run(); err != nil {
        log.Fatal(err)
    }
}
```

注意：

- `404`、`500` 是 HTTP 响应，不会单凭状态码让 `Get` 或 `Do` 返回错误，必须检查 `StatusCode`。
- `http.Get` 使用默认客户端，其 `Client.Timeout` 为零，没有请求总超时；这不意味着底层所有阶段都没有超时。
- `Client.Timeout` 包括连接、重定向和读取响应体；请求上下文也能限制请求生命周期，两者设置后以先到期的为准。
- 成功获取响应后必须关闭 `resp.Body`，包括非 2xx 响应。通常读到 EOF 再关闭有利于 HTTP/1.x 连接复用；遇到超大响应则优先停止读取，不能为复用连接无限消耗资源。
- `io.LimitReader` 到达上限会返回 EOF，并不会自动报告“超限”，因此示例多读一个字节后检查长度。
- 下载大文件可使用 `io.Copy(file, resp.Body)` 避免全部载入内存，同时按需求限制大小、检查复制错误。手动 `Read` 循环必须先处理 `n > 0` 的数据，再处理 `err`，因为最后一批数据可能与 EOF 同时返回。
- `Client` 和 `Transport` 可供多个 goroutine 并发使用，应该复用；自定义请求头使用 `NewRequestWithContext` 配合 `client.Do`。

## 6. 验证改进示例

以下命令针对第 4 节的改进服务端，而不是原始 `server/httpserver.go`：

```bash
# 200，正文 Hello, Alice!
curl -i 'http://127.0.0.1:8000/go?name=Alice'

# 200，只有响应头
curl -I http://127.0.0.1:8000/go

# 200，返回 JSON {"message":"你好，Go"}
curl -i http://127.0.0.1:8000/echo \
  -H 'Content-Type: application/json' \
  --data '{"message":"你好，Go"}'

# 405，Allow: POST
curl -i http://127.0.0.1:8000/echo

# 415，媒体类型不受支持
curl -i http://127.0.0.1:8000/echo --data 'message=hello'

# 400，拒绝未知字段
curl -i http://127.0.0.1:8000/echo \
  -H 'Content-Type: application/json' \
  --data '{"message":"hello","extra":1}'

# 400，拒绝多个 JSON 值
curl -i http://127.0.0.1:8000/echo \
  -H 'Content-Type: application/json' \
  --data '{"message":"hello"} {}'
```

自动化测试可以使用标准库 `net/http/httptest`：`NewRequest` 配合 `NewRecorder` 测试 Handler；`NewServer` 启动临时服务器测试真实客户端交互，测试结束后关闭服务器。建议覆盖正常响应、错误方法、空正文、非法 JSON、超过大小限制，以及客户端超时等情况。

## 7. 从学习示例到实际项目

- **并发安全**：Handler 可能并发执行，共享 map、计数器等需要互斥锁或其他同步机制；不要在持锁状态下执行耗时网络操作。可用 `go test -race ./...` 辅助检查。
- **取消与超时**：向数据库、下游 HTTP 请求传递 `r.Context()`；耗时任务主动检查取消信号，不要假设客户端断开就会强制终止业务代码。
- **优雅退出**：捕获退出信号后，用独立且有超时的 context 调用 `server.Shutdown(ctx)`，停止接收新连接并等待活动请求完成。主 goroutine 要等待关闭流程结束；WebSocket 等被劫持的连接需要自行管理。上方示例未实现信号处理。
- **HTTPS**：可通过 `ListenAndServeTLS` 配置证书，也可以在可信反向代理处终止 TLS；客户端不要为了绕过证书错误而在正式环境关闭证书校验。
- **输入与输出安全**：限制正文、验证参数；输出 HTML 使用 `html/template`，不要直接拼接用户输入。CORS 是浏览器跨域访问机制，不等于认证或授权。
- **中间件**：通过 `func(http.Handler) http.Handler` 包装 Handler，集中处理日志、鉴权等逻辑。
- **排错**：连接拒绝先检查服务端是否启动；端口占用检查其他进程；404 检查路径；405 检查方法；正文不完整检查读取循环与超时。原示例忽略监听错误，端口冲突时可能直接退出而没有提示。

## 8. 在线参考资料

本文主要依据以下 Go 官方资料整理；在线文档会更新，具体 API 请结合本机 `go version` 和 `go doc` 查看。

1. [net/http 官方文档](https://pkg.go.dev/net/http)：客户端、服务端及连接复用的基本用法。
2. [Writing Web Applications](https://go.dev/doc/articles/wiki/)：官方 Web 入门教程，包含 Handler、模板与表单处理。
3. [ServeMux](https://pkg.go.dev/net/http#ServeMux)：路由匹配、优先级与 Go 1.22 兼容性说明。
4. [ResponseWriter](https://pkg.go.dev/net/http#ResponseWriter)：响应头、状态码和正文的写入规则。
5. [Client](https://pkg.go.dev/net/http#Client) / [Server](https://pkg.go.dev/net/http#Server)：超时、连接管理与优雅关闭。
6. [MaxBytesReader](https://pkg.go.dev/net/http#MaxBytesReader)：限制服务端请求体大小。
7. 延伸阅读：[net/http/httptest](https://pkg.go.dev/net/http/httptest)、[encoding/json](https://pkg.go.dev/encoding/json)、[io](https://pkg.go.dev/io)。
