## Day 6-负载均衡（Load Balance）

### 什么是负载均衡？

假设现在我们有多个服务实例，每个实例部署在不同的服务器上，且它们提供相同的服务。对于一个客户端来说，我们可以选择其中任意一个实例进行调用从而完成业务，那么我们如何来选择这几个实例呢？这就取决于我们负载均衡的策略。几个不成熟的想法：

- 随机选取
- 轮询算法（Round Robin）：每次调度不同的服务器，调度时执行 `i = (i + 1) mod n`
- 加权轮询（Weight Round Robin）：在轮询算法的基础上，为每一个服务实例增加一个权重，性能高的权重大，也可以根据当前实例的负载情况动态调整，比如近几分钟内服务器的CPU、内存占用情况等
- 哈希/一致性哈希策略：依据请求的某些特征，计算一个 hash 值，根据 hash 值将请求发送到对应的机器。一致性 hash 还可以解决服务实例动态添加情况下，调度抖动的问题

### 如何实现负载均衡

#### 实现服务发现

前文提到，想要实现负载均衡的前提是有多个提供相同服务的实例，那么我们就需要实现一个最基础的服务发现模块，也就是新注册到服务端的服务能够及时地被客户端发现并进行调用

为了与通信部分解耦，我们新建一个子目录进行编写

- SelectMode：代表不同的负载均衡策略
- Discovery：一个接口类型，包含了实现服务发现的基本接口
  - Refresh()：从注册中心更新服务列表
  - Update(servers []string)：手动更新服务列表
  - Get(mode SelectMode)：根据选择的策略返回一个服务实例
  - GetAll()：返回所有服务实例

```go
type SelectMode int

const (
	RandomSelect     SelectMode = iota // select randomly from existing service
	RoundRobinSelect                   // select using robin algorithm
)

type Discovery interface {
	Refresh() error // refresh from remote registry
	Update(servers []string) error
	Get(mode SelectMode) (string, error)
	GetAll() ([]string, error)
}
```

#### MultiServersDiscovery

这个结构体是不需要注册中心的，服务列表由我们手动去维护的一个结构体

- r：一个随机数，种子选用当前时间戳，避免产生相同的随机数序列
- index：记录当前算法轮询到的位置，为了避免每次从0开始，初始化时设定一个随机的值
- mu：这是一个读写锁

**读写锁相比于互斥锁`MuteX`来说，它的粒度更小，并发度更高，在互斥锁中，永远只有一个进程能够获取锁，其他的都会被阻塞，而读写锁则支持多个读操作并发，但对于写操作是完全互斥的，也就是说当一个协程进行写操作时，其他协程既不能读也不能写**

```go
// MultiServersDiscovery is a discovery for multi servers without a registry center
// user provides the server address explicitly instead
type MultiServersDiscovery struct {
	r       *rand.Rand   // generate a random number
	mu      sync.RWMutex // protect following
	servers []string
	index   int // record the selected position for robin algorithm
}

func NewMultiServersDiscovery(servers []string) *MultiServersDiscovery {
	d := &MultiServersDiscovery{
		servers: servers,
		r:       rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	d.index = d.r.Intn(math.MaxInt32 - 1)
	return d
}
```

然后就是实现对应的接口：

由于没有注册中心，所以我们暂时还不需要实现Refresh()

```go
var _ Discovery = &MultiServersDiscovery{}

func (d *MultiServersDiscovery) Refresh() error {
	//TODO implement me
	return nil
}

func (d *MultiServersDiscovery) Update(servers []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.servers = servers
	return nil
}

func (d *MultiServersDiscovery) Get(mode SelectMode) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := len(d.servers)
	if n == 0 {
		return "", errors.New("rpc discovery: no available servers")
	}
	switch mode {
	case RandomSelect:
		return d.servers[d.r.Intn(n)], nil
	case RoundRobinSelect:
		s := d.servers[d.index%n]
		d.index = (d.index + 1) % n
		return s, nil
	default:
		return "", errors.New("rpc discovery: not supported select mode")
	}
}

func (d *MultiServersDiscovery) GetAll() ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	
	n := len(d.servers)
	servers := make([]string, n)
	copy(servers, d.servers)
	return servers, nil
}
```

#### XClient

接下来就是向用户暴露一个实现了负载均衡的客户端

它的构造函数要传入三个参数，分别是服务发现实例`Discovery`，负载均衡策略`SelectMode`，以及协议的选项`Option`，为了复用已经创建好的Socket连接，我们使用一个map来保存已经创建成功的Client实例，并且要在连接完成之后释放这些Client实例的连接

```go
import (
	"io"
	. "myRPC"
	"sync"
)

type XClient struct {
	d       Discovery
	mode    SelectMode
	opt     *Option
	mu      sync.Mutex
	clients map[string]*Client
}

var _ io.Closer = &XClient{}

func NewXClient(d Discovery, mode SelectMode, opt *Option) *XClient {
	return &XClient{d: d, mode: mode, opt: opt, clients: make(map[string]*Client)}
}

func (xc *XClient) Close() error {
	xc.mu.Lock()
	defer xc.mu.Unlock()
	for index, client := range xc.clients {
		_ = client.Close()
		delete(xc.clients, index)
	}
	return nil
}
```

接下来就是实现调用函数Call和相关的函数

- dial：首先检查map中是否有已经创建的实例，有则检查是否可用，可用直接取出，不可用则从map中删除，没有再通过`XDial()`方法去创建一个新实例
- call：根据dial返回的结果去调用客户端的`Call`方法
- Call：对call进行封装，得到负载均衡策略并调用call，暴露给用户

```go
func (xc *XClient) dial(rpcAddr string) (*Client, error) {
	xc.mu.Lock()
	defer xc.mu.Unlock()

	client, ok := xc.clients[rpcAddr]
	// in case of client is unavailable
	if ok && !client.IsAvailable() {
		_ = client.Close()
		delete(xc.clients, rpcAddr)
		client = nil
	}
	if client == nil {
		var err error
		client, err = XDial(rpcAddr, xc.opt)
		if err != nil {
			return nil, err
		}
		xc.clients[rpcAddr] = client
	}
	return client, nil
}

// Call invokes the named function, waits for it to complete,
// and returns its error status.
// xc will choose a proper server.
func (xc *XClient) Call(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	rpcAddr, err := xc.d.Get(xc.mode)
	if err != nil {
		return err
	}
	return xc.call(rpcAddr, ctx, serviceMethod, args, reply)
}

func (xc *XClient) call(rpcAddr string, ctx context.Context, serviceMethod string, args, reply interface{}) error {
	client, err := XDial(rpcAddr)
	if err != nil {
		return err
	}
	return client.Call(ctx, serviceMethod, args, reply)
}
```

#### Boardcast

为XClient添加一个常用功能Broadcast：

与Call相比，前者是通过负载均衡策略去从所有服务实例中选择一个进行调用，那么后者就是通过广播，调用所有注册在discovery中的服务的方法

这里需要说明的逻辑是

如果我们传入的reply是一个interface{}的话，那我们就没有必要储存reply的值

反之，如果我们传入的是一个具体的类型的话，我们需要一个`clonedReply`来复制一份当前`reply`的值，这是因为reply是这个主线程中共享的变量，而`clonedReply`是每个协程中声明的新变量，如果我们在协程中的`xc.call()`方法直接传入`reply`而不是`clonedReply`的话，每个协程就可能得到错误的结果

**// TODO：这里写成`reflect.ValueOf(reply).Elem().Set(reflect.ValueOf(clonedReply).Elem())`而不是`reply = clonedReply`的原因，我个人猜想应该是像在想在同一块内存区域操作，前者和后者的唯一区别就是前者修改的是地址指向内存区域的值，后者直接修改了变量的内存地址，github上有人讨论后者会导致内存逃逸，执行命令`go build -gcflags '-m -l' xclient.go`后得到的结果如下，并没有发现内存逃逸的情况**

```
# command-line-arguments
.\xclient.go:12:10: undefined: Discovery
.\xclient.go:13:10: undefined: SelectMode
.\xclient.go:21:19: undefined: Discovery
.\xclient.go:21:35: undefined: SelectMode
```



```go
// Broadcast invokes the named function for every server registered in discovery
func (xc *XClient) Broadcast(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	servers, err := xc.d.GetAll()
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	var mu sync.Mutex // protect e and replyDone
	var e error
	replyDone := reply == nil // if reply is nil, don't need to set value
	ctx, cancel := context.WithCancel(ctx)
	for _, rpcAddr := range servers {
		wg.Add(1)
		go func(rpcAddr string) {
			defer wg.Done()
			var clonedReply interface{}
			if reply != nil {
				clonedReply = reflect.New(reflect.ValueOf(reply).Elem().Type()).Interface()
			}
			err := xc.call(rpcAddr, ctx, serviceMethod, args, clonedReply)
			mu.Lock()
			if err != nil && e == nil {
				e = err
				cancel() // if any call failed, cancel unfinished calls
			}
			if err == nil && !replyDone {
				reflect.ValueOf(reply).Elem().Set(reflect.ValueOf(clonedReply).Elem())
				replyDone = true
			}
			mu.Unlock()
		}(rpcAddr)
	}
	wg.Wait()
	return e
}
```

