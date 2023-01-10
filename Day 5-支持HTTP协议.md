## Day 5-支持HTTP协议

大概意思就是，在Day5之前，我们的Server-Client都是基于TCP连接的，为了协议后续的可扩展性，比如根据不同的PATH提供不同的HTTP服务，我们需要把原有的基于TCP连接的过程包装成基于HTTP协议连接，这两个过程其实并不冲突，因为TCP协议是用在传输层的，更多关注数据的传输，而HTTP协议用在应用层，更多关注传输数据的内容和规范

所以其实就是传输层协议TCP不变，在整个报文的头部多包一层HTTP的Header

### 支持HTTP协议需要做什么？

Web 开发中，我们经常使用 HTTP 协议中的 HEAD、GET、POST 等方式发送请求，等待响应。但 RPC 的消息格式与标准的 HTTP 协议并不兼容，在这种情况下，就需要一个协议的转换过程。HTTP 协议的 CONNECT 方法恰好提供了这个能力，CONNECT 一般用于代理服务

假设浏览器与服务器之间的 HTTPS 通信都是加密的，浏览器通过代理服务器发起 HTTPS 请求时，由于请求的站点地址和端口号都是加密保存在 HTTPS 请求报文头中的，代理服务器如何知道往哪里发送请求呢？为了解决这个问题，浏览器通过 HTTP 明文形式向代理服务器发送一个 CONNECT 请求告诉代理服务器目标地址和端口，代理服务器接收到这个请求后，会在对应端口与目标站点建立一个 TCP 连接，连接建立成功后返回 HTTP 200 状态码告诉浏览器与该站点的加密通道已经完成。接下来代理服务器仅需透传浏览器和服务器之间的加密数据包即可，代理服务器无需解析 HTTPS 报文

### 服务端支持 HTTP 协议

客户端向 RPC 服务器发送 CONNECT 请求

```
CONNECT 10.0.0.1:9999/_geerpc_ HTTP/1.0
```

RPC 服务器返回 HTTP 200 状态码表示连接建立。

```
HTTP/1.0 200 Connected to Gee RPC
```

客户端使用创建好的连接发送 RPC 报文，先发送 Option，再发送 N 个请求报文，服务端处理 RPC 请求并响应。

```go
const (
	MagicNumber      = 0x3bef5c
	connected        = "200 Connected to myRPC"
	defaultRPCPath   = "/_myRPC_"
	defaultDebugPath = "/debug/myRPC"
)

// ServeHTTP implements a http.Handler that answers RPC requests.
func (server *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != "CONNECT" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = io.WriteString(w, "405 must CONNECT\n")
		return
	}
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		log.Print("rpc hijacking ", req.RemoteAddr, ": ", err.Error())
		return
	}
	_, _ = io.WriteString(conn, "HTTP/1.0 "+connected+"\n\n")
	server.ServeConn(conn)
}

// HandleHTTP registers an HTTP handler for RPC messages on rpcPath.
// It is still necessary to invoke http.Serve(), typically in a go statement.
func (server *Server) HandleHTTP() {
	http.Handle(defaultRPCPath, server)
}

// HandleHTTP is a convenient approach for default server to register HTTP handlers
func HandleHTTP() {
	DefaultServer.HandleHTTP()
}
```

在 Go 语言中处理 HTTP 请求是非常简单的一件事，Go 标准库中 `http.Handle` 的实现如下：

```go
package http
// Handle registers the handler for the given pattern
// in the DefaultServeMux.
// The documentation for ServeMux explains how patterns are matched.
func Handle(pattern string, handler Handler) { DefaultServeMux.Handle(pattern, handler) }
```

第一个参数是支持通配的字符串 pattern，在这里，我们固定传入 `/_geeprc_`，第二个参数是 Handler 类型，Handler 是一个接口类型，定义如下：

```go
type Handler interface {
    ServeHTTP(w ResponseWriter, r *Request)
}
```

也就是说，只需要实现接口 Handler 即可作为一个 HTTP Handler 处理 HTTP 请求。接口 Handler 只定义了一个方法 `ServeHTTP`，实现该方法即可。

### 客户端支持HTTP协议

服务端已经能够接受 CONNECT 请求，并返回了 200 状态码 `HTTP/1.0 200 Connected to Gee RPC`，客户端要做的，发起 CONNECT 请求，检查返回状态码即可成功建立连接。

```go
// NewHTTPClient new a Client instance via HTTP as transport protocol
func NewHTTPClient(conn net.Conn, opt *Option) (*Client, error) {
	_, _ = io.WriteString(conn, fmt.Sprintf("CONNECT %s HTTP/1.0\n\n", defaultRPCPath))

	// Require successful HTTP response
	// before switching to RPC protocol.
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
	if err == nil && resp.Status == connected {
		return NewClient(conn, opt)
	}
	if err == nil {
		err = errors.New("unexpected HTTP response: " + resp.Status)
	}
	return nil, err
}

// DialHTTP connects to an HTTP RPC server at the specified network address
// listening on the default HTTP RPC path.
func DialHTTP(network, address string, opts ...*Option) (*Client, error) {
	return dialTimeout(NewHTTPClient, network, address, opts...)
}
```

