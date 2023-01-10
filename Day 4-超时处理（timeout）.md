## Day 4-超时处理（timeout）

- 增加连接超时的处理机制
- 增加服务端处理超时的处理机制

### 为什么需要超时处理？

如果缺少超时处理机制，无论是服务端还是客户端都容易因为网络或其他错误导致挂死，资源耗尽，这些问题的出现大大地降低了服务的可用性。因此，我们需要在 RPC 框架中加入超时处理的能力。

纵观整个远程调用的过程，需要客户端处理超时的地方有：

- 与服务端建立连接，导致的超时
- 发送请求到服务端，写报文导致的超时
- 等待服务端处理时，等待处理导致的超时（比如服务端已挂死，迟迟不响应）
- 从服务端接收响应时，读报文导致的超时

需要服务端处理超时的地方有：

- 读取客户端请求报文时，读报文导致的超时
- 发送响应报文时，写报文导致的超时
- 调用映射服务的方法时，处理报文导致的超时

整合下来，我们需要在三个地方添加超时机制：

- 客户端建立连接时
- 客户端去调用`client.Call()`的整个过程导致的超时（包括发报文、等待处理、接收报文）
- 服务端处理报文，即`Server.handleRequest`超时

### 创建连接超时（包括接收响应超时）

为了实现的方便，把超时设定放在Option中，且默认值为10s，`HandleTimeOut`默认为0，即不设限

```go
type Option struct {
	MagicNumber    int        // MagicNumber represents that it's a myRPC request
	CodecType      codec.Type // Client may choose different type to encode request
	ConnectTimeOut time.Duration
	HandleTimeOut  time.Duration
}

var DefaultOption = &Option{
	MagicNumber: MagicNumber,
	CodecType:   codec.GobType,
	ConnectTimeOut: time.Second * 10,
}
```

然后就是对原来的`Dial`方法的逻辑做修改，基于net包下的`DialTimeOut`方法进行封装：

这里传入的函数`f`是一个`Client`的构造函数式，**但是其实这种写法是会造成协程泄漏具体逻辑理一遍会发现如果先超时的话，主函数退出，协程会一直阻塞**

博客原写法：

```go
type clientResult struct {
	client *Client
	err    error
}

type newClientFunc func(conn net.Conn, opt *Option) (client *Client, err error)

func Dial(network, address string, opts ...*Option) (client *Client, err error) {
	return dialTimeOut(NewClient, network, address, opts...)
}

func dialTimeOut(f newClientFunc, network, address string, opts ...*Option) (client *Client, err error) {
	opt, err := parseOption(opts...)
	if err != nil {
		return nil, err
	}
	// case1: timeout when create connection
	conn, err := net.DialTimeout(network, address, opt.ConnectTimeOut)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = conn.Close()
		}
	}()
	ch := make(chan *Client)
	go func() {
		client, err = f(conn, opt)
		ch <- client
	}()
	// return directly if it has no timeout processing
	if opt.ConnectTimeOut == 0 {
		client = <-ch
		return client, err
	}
	// case2: timeout when create a new client
	select {
	case <-time.After(opt.ConnectTimeOut):
		return nil, fmt.Errorf("rpc client: connect timeout: expect within %s", opt.ConnectTimeOut)
	case client = <-ch:
		return client, err
	}
}
```

修改后：

用一个双向的channel`isReturn`，不进行写过程，仅仅`defer close(isReturn)`，当函数退出才执行这条语句，那么就说明要么超时退出，要么正常执行退出，此时要关闭之前定义的管道`ch`，以防止协程泄漏

```go
func dialTimeout(f newClientFunc, network, address string, opts ...*Option) (client *Client, err error) {
	opt, err := parseOption(opts...)
	if err != nil {
		return nil, err
	}
	// case1: timeout when create connection
	conn, err := net.DialTimeout(network, address, opt.ConnectTimeout)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = conn.Close()
		}
	}()

	ch := make(chan *Client)
	isReturn := make(chan struct{})
	defer close(isReturn)
	go func() {
		client, err = f(conn, opt)
		select {
		case <-isReturn:
			close(ch)
			return
		case ch <- client:
		}
	}()
	// return directly if it has no timeout processing
	if opt.ConnectTimeout == 0 {
		client = <-ch
		return client, err
	}
	// case2: timeout when create a new client
	select {
	case <-time.After(opt.ConnectTimeout):
		return nil, fmt.Errorf("rpc client: connect timeout: expect within %s", opt.ConnectTimeout)
	case client = <-ch:
		return client, err
	}
}
```



### Client.Call 超时

`Client.Call` 的超时处理机制，使用 context 包实现，控制权交给用户，控制更为灵活

```go
// Call can be Divided into two cases:
//  1. if send() works correctly without error
//     then read from call.Done will be blocked because it's null
//  2. if error occurs,call will put itself into call.done
//     and return call.Error to client
func (client *Client) Call(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	call := client.Go(serviceMethod, args, reply, make(chan *Call, 1))
	select {
	case <-ctx.Done():
		client.removeCall(call.Seq)
		return errors.New("rpc client: call failed: " + ctx.Err().Error())
	case call = <-call.Done:
		return call.Error
	}
	// call := <-client.Go(serviceMethod, args, reply, make(chan *Call, 1)).Done
	// return call.Error
}
```

### 服务端处理超时

服务端处理超时类似客户端，同样的要记得`close(called)`和`close(sent)`

```go
func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup, timeout time.Duration) {
	defer wg.Done()
	called, sent := make(chan struct{}), make(chan struct{})
	isReturn := make(chan struct{})
	defer close(isReturn)
	go func() {
		err := req.svc.call(req.mtype, req.argv, req.replyv)
		select {
		// this case will only happen after executing "defer close(isReturn)"
		case <-isReturn:
			close(called)
			close(sent)
			return
		case called <- struct{}{}:
			if err != nil {
				req.h.Error = err.Error()
				server.sendResponse(cc, req.h, invalidRequest, sending)
				sent <- struct{}{}
				return
			}
			server.sendResponse(cc, req.h, req.replyv.Interface(), sending)
			sent <- struct{}{}
		}
	}()
	// return directly if it has no timeout processing
	if timeout == 0 {
		<-called
		<-sent
		return
	}
	select {
	case <-time.After(timeout):
		req.h.Error = fmt.Sprintf("rpc server: request handle timeout: expect within %s", timeout)
		server.sendResponse(cc, req.h, invalidRequest, sending)
	case <-called:
		<-sent
	}
}
```

### 总结

超时处理主要是通过两种方式：

- `select{}`+`time.After()`

  其中我们用到了unbuffered channel来进行协程和主线程之间的同步，这种管道的特点就是没有存储的能力，只有读的协程和写的协程同时进行时整个过程才能正常执行，如果一方出现问题，另外一方就会被阻塞。

  我们利用了这种特点来进行同步操作，但是在这个过程中要注意是否会出现协程泄漏，如果主线程由于超时退出且不去处理unbuffered channel的话，协程就会因为没有读的一方而一直阻塞，从而无法回收造成泄漏。

  当然解决这个问题的方法也很简单，再设置一个`isReturn := make(chan struct{})`且只执行`defer close(isReturn)`，当我们能够从这个通道中取出值，说明`defer close(isReturn)`已经被执行完毕了，那么此时要么函数正常退出，要么是因为超时退出，从而对我们开启的其他协程进行处理

  - `close()`是用来关闭channel的，这个channel要么是双向的，要么是只写的，并且这个方法应该由发送方来调用而非接收方，**当channel被关闭后，所有读取过程都会非阻塞直接成功，返回channel元素的零值**

- 向协程传入`context`，调用`context.WithTimeout()`主动控制

