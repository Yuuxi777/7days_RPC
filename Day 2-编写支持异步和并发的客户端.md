## Day 2-编写支持异步和并发的客户端

### 写在开头的总结

#### 这个客户端为什么支持异步和并发？

##### 异步

设计的这个客户端能够支持异步的原因是用了信道`Call.Done`，这是一个`chan *Call`，当服务端发起调用时，可以不必等待这个调用（也就是`Go`方法返回的`call`）是否执行完毕（即这个`call`是否调用了`call.done()`），要让他变成同步也很简单，就是调用`Call`方法去读取信道`Call.Done`，由于信道在没有数据时读取会阻塞，从而达到了同步的目的

##### 并发

并发就很简单了，首先每一个Client都会开启一个子协程去执行`receive`方法，从而获得返回值，同时我们可以开启多个协程，每一个协程去新建一个Client并向服务端发起请求

### 实现过程

#### 满足远程调用的条件

在net/rpc包中，有这样一段话：

```go
/*
Only methods that satisfy these criteria will be made available for remote access;
other methods will be ignored:
  - the method's type is exported.
  - the method is exported.
  - the method has two arguments, both exported (or builtin) types.
  - the method's second argument is a pointer.
  - the method has return type error.
*/
```

意思就是只有满足这五个条件的方法才能进行远程调用，更直观地说就是：

```go
func (t *T) MethodName(argType arg1, replyType *rpl) error{}
```

#### 设计Call结构体

所以我们首先要抽象出一次调用的Call结构体，其中比较特殊的点在于`Call.Done`，这是一个`*Call`类型的channel，当我们一次调用完成时，我们会把当前这个Call实例放进Done信道中以通知Client已完成调用，很明显，这样的设计是为了客户端可以支持异步调用，当`Done`满时，如果我们继续写，就会阻塞，从而达到通知的效果

```go
type Call struct {
	Seq           uint64      // to uniquely identify a call
	ServiceMethod string      // format "<service>.<method>"
	Args          interface{} // arguments passed in
	Reply         interface{} // values returned
	Error         error       // in case if error occurs
	Done          chan *Call  // strobes when call is complete
}

// done is written to support asynchronous call
func (call *Call) done() {
	call.Done <- call
}
```

#### 编写Client客户端

完成了Call结构体，接下来就是Client客户端的编写：

- seq：给发送的请求编号，每个请求有一个唯一编号
- codec：客户端的编解码器
- opt：发送请求时携带的编解码选项和验证码
- sending：一个互斥锁，用于发送请求时，保证发送的请求的完整性
- header：每个请求的消息头，我们从call中取出需要的字段构建出`header`并传给服务端，并且由于每个请求的发送是互斥的，所以这里不需要数组，每次发送请求时复用即可
- mu：客户端自己的互斥锁，用于客户端自己的`Close`方法和`IsAvailable`方法
- pending：一个`seq-*Call`类型的map，用于存储还没处理完的请求，**个人觉得，这里不用链表的原因是，所有请求是并发的，每一个请求不需要按序，只需要从map中直接取出，且从map中查询的时间复杂度是O(1)，小于从链表中遍历的时间**
- isClosed：当前客户端是否关闭，通常由用户调用Close方法设置
- isShutdown：有错误发生时，该字段将被置为true

`ErrShutDown`是我们自定义的一个错误类型，意为连接意外关闭

`Close`方法就是用户主动关闭当前Client

`IsAvailable`方法用于检查当前Client是否正常工作

```go
type Client struct {
	seq        uint64
	codec      codec.Codec
	opt        *Option
	sending    sync.Mutex
	header     *codec.Header
	mu         *sync.Mutex
	pending    map[uint64]*Call
	isClosed   bool
	isShutdown bool
}

var ErrShutdown = errors.New("connection is shut down")

func (client *Client) Close() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.isClosed {
		return ErrShutdown
	}
	client.isClosed = true
	return client.codec.Close()
}

var _ io.Closer = &Client{}

// IsAvailable returns true if the client is working
func (client *Client) IsAvailable() bool {
	client.mu.Lock()
	defer client.mu.Unlock()

	return !client.isShutdown && !client.isClosed
}
```

#### 实现Client方法

接下来就是实现Client的几个方法了

- registerCall：把当前的请求放入客户端的pending中
- removeCall：传入一个请求的`seq`字段并将其从pending中移除
- terminateCall：当客户端发生错误中止时要调用这个方法告知并中止pending中的所有call

```go
// registerCall is used to put a call into pending in a working client
func (client *Client) registerCall(call *Call) (uint64,error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.isShutdown || client.isClosed {
		return 0, ErrShutdown
	}
	call.Seq = client.seq
	client.pending[call.Seq] = call
	client.seq++
	return call.Seq, nil
}

// removeCall is used to get a call by its seq to handle
// after handle,remove it from pending
func (client *Client) removeCall(seq uint64) *Call {
	client.mu.Lock()
	defer client.mu.Unlock()
	call := client.pending[seq]
	delete(client.pending,seq)
	return call
}

// terminateCalls is used to terminate all calls in pending
// when client is shutdown
func (client *Client) terminateCalls(err error)  {
	client.sending.Lock()
	defer client.sending.Unlock()
	client.mu.Lock()
	defer client.mu.Unlock()
	client.isShutdown = true
	for _, call := range client.pending {
		call.Error = err
		call.done()
	}
}
```

#### 实现接收响应

最后就是实现客户端的两个核心功能：接收响应和发送请求

首先是receive函数

分为三种情况：

- call 不存在，可能是请求没有发送完整，或者因为其他原因被取消，但是服务端仍旧处理了。
- call 存在，但服务端处理出错，即 `h.Error` 不为空。
- call 存在，服务端处理正常，那么需要从 body 中读取 Reply 的值。

```go
// receive the reply from server
func (client *Client) receive() {
	var err error
	for err == nil {
		var h codec.Header
		if err = client.codec.ReadHeader(&h); err != nil {
			break
		}
		call := client.removeCall(h.Seq)
		switch {
		case call == nil:
			err = client.codec.ReadBody(nil)
		case h.Error != "":
			call.Error = fmt.Errorf(h.Error)
			err = client.codec.ReadBody(nil)
			call.done()
		default:
			err = client.codec.ReadBody(call.Reply)
			if err != nil {
				call.Error = errors.New("reading body " + err.Error())
			}
			call.done()
		}
	}
	client.terminateCalls(err)
}
```

#### 构造Client

然后是Client的构造函数，首先创建Client的时候需要和服务端商量编解码协议，所以需要`Option`字段，当`Option`字段的解析完成并且没有报错时，就调用真正的构造函数创建一个Client实例并且开启一个协程调用`receive`方法去循环接收响应

```go
func NewClient(conn net.Conn, opt *Option) (*Client, error) {
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		err := fmt.Errorf("invalid codec type %s", opt.CodecType)
		log.Println("rpc client: codec error:", err)
		return nil, err
	}
	if err := json.NewEncoder(conn).Encode(opt); err != nil {
		log.Println("rpc client: options error: ", err)
		_ = conn.Close()
		return nil, err
	}
	return NewClientCodec(f(conn), opt), nil
}

func NewClientCodec(codec codec.Codec, opt *Option) (client *Client) {
	client = &Client{
		seq:     1,
		codec:   codec,
		opt:     opt,
		pending: make(map[uint64]*Call),
	}
	go client.receive()
	return 
}
```

#### 暴露给用户的Dial接口

剩下的就是实现一个连接函数`Dial`方便用户去传入服务端的地址，并且由于Option是一个可选参数，所以用`...`

```go
func Dial(network, address string, opts ...*Option) (client *Client, err error) {
	opt, err := parseOption(opts...)
	if err != nil {
		return nil, err
	}
	conn, err := net.Dial(network, address)
	if err != nil {
		return nil, err
	}
	defer func() {
		if client == nil {
			_ = conn.Close()
		}
	}()
	return NewClient(conn, opt)
}

func parseOption(opts ...*Option) (*Option, error) {
	if len(opts) == 0 || opts[0] == nil {
		return DefaultOption, nil
	}
	if len(opts) != 1 {
		return nil, errors.New("number of options is more than 1")
	}
	opt := opts[0]
	opt.MagicNumber = DefaultOption.MagicNumber
	if opt.CodecType == "" {
		opt.CodecType = DefaultOption.CodecType
	}
	return opt, nil
}
```

#### 实现发送请求

最后就是实现发送请求的功能：

这里Call方法的解释已经由注释给出，通过channel的阻塞实现同步

Go方法是一个异步方法

Send方法的实现过程也已由注释给出

```go
// Call can be Divided into two cases:
//  1. if send() works correctly without error
//     then read from call.Done will be blocked because it's null
//  2. if error occurs,call will put itself into call.done
//     and return call.Error to client
func (client *Client) Call(serviceMethod string, args, reply interface{}) error {
	call := <-client.Go(serviceMethod, args, reply, make(chan *Call, 1)).Done
	return call.Error
}

func (client *Client) Go(serviceMethod string, args, reply interface{}, done chan *Call) *Call {
	if done == nil {
		done = make(chan *Call, 10)
	} else if cap(done) == 0 {
		log.Panic("rpc client: done channel is unbuffered")
	}
	call := &Call{
		ServiceMethod: serviceMethod,
		Args:          args,
		Reply:         reply,
		Done:          done,
	}
	client.send(call)
	return call
}

func (client *Client) send(call *Call) {
	// make sure to send a complete request
	client.sending.Lock()
	defer client.sending.Unlock()

	// step1: register call to pending in client
	seq, err := client.registerCall(call)
	if err != nil {
		call.Error = err
		call.done()
		return
	}

	// step2: set client header to send to server
	client.header.ServiceMethod = call.ServiceMethod
	client.header.Seq = seq
	client.header.Error = ""

	// step3: send header and args to server
	// 		  remove call from client.pending if it occurs error
	if err = client.codec.Write(&client.header, call.Args); err != nil {
		call = client.removeCall(seq)
		if call != nil {
			call.Error = err
			call.done()
		}
	}
}
```

