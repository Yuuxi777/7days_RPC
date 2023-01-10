## Day 1-实现序列化与反序列化

首先要搞清楚为什么需要序列化，序列化和反序列化听起来很晦涩，其实在平时的开发场景中就是前后端进行数据交互的时候，后端把前端需要的数据进行序列化（通常就是JSON格式），并返回给前端，前端根据收到的JSON格式的数据进行反序列化，然后继续进行接下来的开发，这就是一个序列化后反序列化的过程

### 为什么需要序列化？

在我们后端开发过程中，各种数据是保存在不同数据结构中的，可能是栈、队列、树、哈希表、链表等等，这些数据结构在内存中运行时针对CPU访问和操作进行了优化

但是如果我们需要把这些数据写入文件，或者通过网络发送，那么我们就需要把这些数据编码成某种格式的字节序列（JSON/XML/proto）等，这就引出了序列化

### RPC中如何实现序列化？

#### Header

RPC中，一次典型的请求可以这样表示：

客户端发送

- Service：服务名
- Method：方法名
- args：入参
- reply：返回值

```go
err = client.Call("Service.Method",	args, &reply)
```

由于RPC同样通过网络发送数据，那么我们就需要定义协议来规范发送数据的格式，常见的情况当然就是一个请求中包括请求头`Header`和请求体`Body`，而通常`Header`其实就是对Body各个字段的描述，从而能够区别出不同的消息，这样我们可以抽象出一个Header结构体：

```go
type Header struct {
	ServiceMethod string // format "Service.Method"
	Seq           uint64 // sequence number chosen by client
	Error         string
}
```

Body涉及到不同入参的问题，Day1先不写

#### Codec

接下为了方便代码的复用性和耦合度，我们需要设计一个编解码的接口`Codec`，这个接口需要有几个方法：

- io.Closer：go提供的io包下的一个接口，其中有一个Close方法用于关闭资源流
- ReadHeader：从请求中读取Header
- ReadBody：从请求中读取Body
- Write：写入返回值

```go
type Codec interface {
	io.Closer
	ReadHeader(*Header) error
	ReadBody(interface{}) error
	Write(*Header, interface{}) error
}
```

然后就是抽象出Codec的构造函数，这里用了type关键字定义，同时定义了两个常量来在后续过程中选择编解码的方式（gob or json），这里的`NewCodecFuncMap`是一个Type-Function类型的Map，意思就是它的返回值不是一个实例而是一个构造函数，不太清楚为什么这样写

```go
type NewCodecFunc func(io.ReadWriteCloser) Codec

type Type string

const (
	GobType  Type = "application/gob"
	JsonType Type = "application/json"
)

var NewCodecFuncMap map[Type]NewCodecFunc

// init function can initialize a GobType constructor by default
func init() {
	NewCodecFuncMap = make(map[Type]NewCodecFunc)
	NewCodecFuncMap[GobType] = NewGobCodec
}
```

#### GobCodec

然后就是定义我们默认条件下的序列方式`GobCodec`，这个结构体要实现我们的`Codec`接口，同样这个结构体的字段也是为了实现接口而定义的：

- conn：也是go的io包下的一个接口，通俗来说就是连接后返回的一个实例
- buf：这是一个写入时避免阻塞而设置的缓冲区，主要是为了提高性能
- dec：解码器
- enc：编码器

```go
type GobCodec struct {
	conn io.ReadWriteCloser
	buf  *bufio.Writer
	dec  *gob.Decoder
	enc  *gob.Encoder
}
```

接下来就是`GobCodec`的构造函数和通过`encoding/gob`包下的编解码器来实现Read和Write函数

```go
var _ Codec = &GobCodec{}

func NewGobCodec(conn io.ReadWriteCloser) Codec {
	buf := bufio.NewWriter(conn)
	return &GobCodec{
		conn: conn,
		buf:  buf,
		dec:  gob.NewDecoder(conn),
		enc:  gob.NewEncoder(buf),
	}
}

func (c *GobCodec) Close() error {
	return c.conn.Close()
}

func (c *GobCodec) ReadHeader(header *Header) error {
	return c.dec.Decode(header)
}

func (c *GobCodec) ReadBody(body interface{}) error {
	return c.dec.Decode(body)
}

func (c *GobCodec) Write(header *Header, body interface{}) (err error) {
	defer func() {
		_ = c.buf.Flush()
		if err != nil {
			_ = c.Close()
		}
	}()
	if err = c.enc.Encode(header); err != nil {
		log.Println("rpc:gob error encoding header:", err)
		return
	}
	if err = c.enc.Encode(body); err != nil {
		log.Println("rpc:gob error encoding body:", err)
		return
	}
	return
}
```

#### 通信过程

编解码的过程完成了，接下来就是完成通信过程，对于我们这个RPC来说，双方唯一需要协商的就是用哪种方法进行消息的编解码，这部分放入结构体`Option`中

新建一个Option作为默认的配置`DefaultOption`

```go
const MagicNumber = 0x3bef5c

type Option struct {
	MagicNumber int        // MagicNumber represents that it's a myRPC request
	CodecType   codec.Type // Client may choose different type to encode request
}

var DefaultOption = &Option{
	MagicNumber: MagicNumber,
	CodecType:   codec.GobType,
}
```

接下来就是服务端的实现：

Server是一个空的结构体，需要去实现Accept方法，for循环等待socket连接建立，同时开启子协程处理，处理过程交给`ServerConn`方法

```go
// Server represents an RPC server
type Server struct {
}

// NewServer can return a new server
func NewServer() *Server {
	return &Server{}
}

// DefaultServer is the default instance of server
var DefaultServer = NewServer()

// Accept accepts connections on the listener and serves requests
// for each incoming connection
func (server *Server) Accept(lis net.Listener) {
	for {
		conn, err := lis.Accept()
		if err != nil {
			log.Println("rpc server: accept error:", err)
			return
		}
		go server.ServeConn(conn)
	}
}

// Accept accepts connections on the listener and serves requests
// for each incoming connection
func Accept(lis net.Listener) {
	DefaultServer.Accept(lis)
}
```

`ServeConn`的主要作用就是首先对`Option`进行解码，然后从中取出`MagicNumber`验证该请求是否是RPC请求，接下来检查`CodeType`，然后根据这个值生成对应的编解码器，交给`ServerCodec`

```go
// ServeConn runs the server on a single connection.
// ServeConn blocks, serving the connection until the client hangs up
func (server *Server) ServeConn(conn io.ReadWriteCloser) {
	defer func() {
		_ = conn.Close()
	}()
	var opt Option
	if err := json.NewDecoder(conn).Decode(&opt); err != nil {
		log.Println("rpc server: options error: ", err)
		return
	}
	if opt.MagicNumber != MagicNumber {
		log.Printf("rpc server: invalid magic number %x", opt.MagicNumber)
		return
	}

	// f is a constructor(function) for Codec
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		log.Printf("rpc server: invalid codec type %s", opt.CodecType)
		return
	}
	server.ServeCodec(f(conn))
}
```

`ServerCodec`这个函数负责三个过程：

- 读取请求 readRequest
- 处理请求 handleRequest
- 回复请求 replyRequest

```go
func (server *Server) ServeCodec(cc codec.Codec) {
	sending := new(sync.Mutex) // make sure to send a complete response
	wg := new(sync.WaitGroup)  // wait until all request are handled
	for {
		req, err := server.readRequest(cc)
		if err != nil {
			if req == nil {
				break // it's not possible to recover, so close the connection
			}
			req.h.Error = err.Error()
			server.sendResponse(cc, req.h, invalidRequest, sending)
			continue
		}
		wg.Add(1)
		go server.handleRequest(cc, req, sending, wg)
	}
	wg.Wait()
	_ = cc.Close()
}

// request stores all information of a call
type request struct {
	h            *codec.Header // header of request
	argv, replyv reflect.Value // argv and replyv of request
}

func (server *Server) readRequest(cc codec.Codec) (*request, error) {
	h, err := server.readRequestHeader(cc)
	if err != nil {
		return nil, err
	}
	req := &request{h: h}
	// TODO: now we don't know the type of request argv
	// day 1, just suppose it's string
	req.argv = reflect.New(reflect.TypeOf(""))
	if err = cc.ReadBody(req.argv.Interface()); err != nil {
		log.Println("rpc server: read argv err:", err)
	}
	return req, nil
}

func (server *Server) readRequestHeader(cc codec.Codec) (*codec.Header, error) {
	var h codec.Header
	if err := cc.ReadHeader(&h); err != nil {
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Println("rpc server: read header error:", err)
		}
		return nil, err
	}
	return &h, nil
}

func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup) {
	// TODO, should call registered rpc methods to get the right replyv
	// day 1, just print argv and send a hello message
	defer wg.Done()
	log.Println(req.h, req.argv.Elem())
	req.replyv = reflect.ValueOf(fmt.Sprintf("geerpc resp %d", req.h.Seq))
	server.sendResponse(cc, req.h, req.replyv.Interface(), sending)
}

func (server *Server) sendResponse(cc codec.Codec, h *codec.Header, body interface{}, sending *sync.Mutex) {
	sending.Lock()
	defer sending.Unlock()
	if err := cc.Write(h, body); err != nil {
		log.Println("rpc server: write response error:", err)
	}
}
```

