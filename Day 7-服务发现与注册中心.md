## Day 7-服务发现与注册中心

### 什么是注册中心？

注册中心，用简单的话说就是服务端和客户端之间的中间者，它的存在使得服务端和客户端彼此不用感知对方的存在，具体来说就是：

1. 服务端启动后，向注册中心发送注册消息，注册中心得知该服务已经启动，处于可用状态。一般来说，服务端还需要定期向注册中心发送心跳，证明自己还活着。
2. 客户端向注册中心询问，当前哪天服务是可用的，注册中心将可用的服务列表返回客户端。
3. 客户端根据注册中心得到的服务列表，选择其中一个发起调用。

如果没有注册中心，那么客户端就需要硬编码服务端的地址，并且没有对应的机制来检查服务端是否处于可用状态

### CenterRegistry

首先要实现发现服务和注册服务的注册中心：

这里的timeout字段用于判断注册中心的每一个实例是否超时，servers是一个map，用于保存已经注册的实例

构造函数没有什么特殊的，这里就不再赘述了

```go
type CenterRegistry struct {
	timeout time.Duration
	mu      sync.Mutex
	servers map[string]*ServerItem
}

type ServerItem struct {
	Addr  string
	start time.Time
}

const (
	defaultPath    = "/myRPC/registry"
	defaultTimeout = time.Second * 5
)

func New(timeout time.Duration) *CenterRegistry {
	return &CenterRegistry{
		timeout: timeout,
		servers: make(map[string]*ServerItem),
	}
}

var DefaultRegister = New(defaultTimeout)
```

接下来实现两个最基本的方法：`putServer`和`getAliveServers`

前者用于添加服务实例，如果实例已经存在，那么更新服务的开始时间`server.start`

后者用于返回可用的实例列表，如果存在已经超时的服务就删除

```go
func (r *CenterRegistry) putServer(addr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	server := r.servers[addr]
	if server == nil {
		r.servers[addr] = &ServerItem{Addr: addr, start: time.Now()}
	} else {
		server.start = time.Now()
	}
}

func (r *CenterRegistry) getAliveServers() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var alive []string
	for addr, server := range r.servers {
		if r.timeout == 0 || server.start.Add(r.timeout).After(time.Now()) {
			alive = append(alive, addr)
		} else {
			delete(r.servers, addr)
		}
	}
	sort.Strings(alive)
	return alive
}
```

为了实现的简单，我们的注册中心采用HTTP提供服务，即客户端和服务端与注册中心之间的协议是HTTP协议，且我们把所有的有用信息都承载在HTTP Header中

其中

“GET”：用于返回所有可用的服务列表，通过自定义字段`X-Myrpc-Servers`承载

“POST”：用于添加服务实例或发送心跳，通过自定义字段`X-Myrpc-Server`承载

这里的三个方法分别如下：

- HandleHTTP：暴露给用户的接口，新建一个默认的注册中心`defaultRegistry`调用`r.HandleHTTP()`
- r.HandleHTTP：这个方法调用了http包中的`http.Handle(pattern, handler)`方法，前者是我们传入的相对路径`/myRPC/registry`，后者`handler`是一个`interface{}`，包括了一个方法`ServeHTTP()`它将一个路径映射到给定的handler上，当我们请求这个路径时，就会调用对应Handler的`ServeHTTP()`方法
  - r.ServeHTTP：实现接口`http.Handler()`的方法`ServeHTTP()`

```go
// Runs at /myRPC/registry
func (r *CenterRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case "GET":
		w.Header().Set("X-Myrpc-Servers", strings.Join(r.getAliveServers(), ","))
	case "POST":
		addr := req.Header.Get("X-Myrpc-Server")
		if addr == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		r.putServer(addr)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// HandleHTTP registers an HTTP handler for CenterRegistry messages on registryPath
// http.Handle(pattern, handler): handler is an interface{}, which should implement method ServeHTTP()
func (r *CenterRegistry) HandleHTTP(registryPath string) {
	http.Handle(registryPath, r)
	log.Println("rpc registry path:", registryPath)
}

func HandleHTTP() {
	DefaultRegister.HandleHTTP(defaultPath)
}
```

### Heartbeat

接下来就是Heartbeat方法，用于服务启动时定时向注册中心发送心跳，默认的发送周期比注册中心设置的过期时间少1min

- sendHeartbeat：用于服务端向注册中心发送POST请求，本质上就是封装了`http.Do()`方法，这个方法用于发送较为复杂的POST请求
- Heartbeat：暴露给用户的接口，传入注册中心和服务端的地址，启动的时候调用一次`sendHeartbeat()`，然后启动一个协程去循环地向注册中心发送心跳，这里的`time.NewTicker(Time.Duration)`是一个计时器，它的返回值是一个长度为1的管道`chan Time`，当经过设定的时间后，`<-t.C`将不会被阻塞，从而完成定时发送心跳的逻辑

```go
// Heartbeat send a heartbeat message every once in a while
// it's a helper function for a server to register or send heartbeat
func Heartbeat(registryAddr, serverAddr string, duration time.Duration) {
	// set default send cycle
	if duration == 0 {
		// make sure there is enough time to send heart beat
		// before it's removed from registry
		duration = defaultTimeout - time.Duration(1)*time.Minute
	}
	var err error
	err = sendHeartbeat(registryAddr, serverAddr)
	go func() {
		t := time.NewTicker(duration)
		for err == nil {
			<-t.C
			err = sendHeartbeat(registryAddr, serverAddr)
		}
	}()
}

func sendHeartbeat(registryAddr, serverAddr string) error {
	log.Println(serverAddr, "send heart beat to registry", registryAddr)
	httpClient := &http.Client{}
	req, _ := http.NewRequest("POST", registryAddr, nil)
	req.Header.Set("X-Myrpc-Server", serverAddr)
	if _, err := httpClient.Do(req); err != nil {
		log.Println("rpc server: heart beat err:", err)
		return err
	}
	return nil
}
```

### CenterRegistryDiscovery

最后把注册中心集成到之前写的服务发现的模块中去，复用我们之前写的`*MultiServersDiscovery`，设置默认更新时间为10s，然后就是构造函数

- 

```go
type CenterRegistryDiscovery struct {
	*MultiServersDiscovery
	registryAddr string
	timeout      time.Duration
	lastUpdate   time.Time
}

const defaultUpdateTimeout = time.Second * 10

func NewCenterRegistryDiscovery(registerAddr string, timeout time.Duration) *CenterRegistryDiscovery {
	if timeout == 0 {
		timeout = defaultUpdateTimeout
	}
	d := &CenterRegistryDiscovery{
		MultiServersDiscovery: NewMultiServersDiscovery(make([]string, 0)),
		registryAddr:          registerAddr,
		timeout:               timeout,
	}
	return d
}
```

接着就是实现几个方法：

- Update：手动刷新服务列表，并且把上一次更新时间`lastUpdate`更新为`time.Now()`	
- Refresh：如果周期还没到，那么就直接返回，否则将返回的结果放入`d.servers`，并且更新`lastUpdate`
- Get：略
- GetAll：略

```go
func (d *CenterRegistryDiscovery) Update(servers []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.servers = servers
	d.lastUpdate = time.Now()
	return nil
}

func (d *CenterRegistryDiscovery) Refresh() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lastUpdate.Add(d.timeout).After(time.Now()) {
		return nil
	}
	log.Println("rpc registry: refresh servers from registry", d.registryAddr)
	resp, err := http.Get(d.registryAddr)
	if err != nil {
		log.Println("rpc registry refresh err:", err)
		return err
	}
	servers := strings.Split(resp.Header.Get("X-Myrpc-Servers"), ",")
	d.servers = make([]string, 0, len(servers))
	for _, server := range servers {
		if strings.TrimSpace(server) != "" {
			d.servers = append(d.servers, strings.TrimSpace(server))
		}
	}
	d.lastUpdate = time.Now()
	return nil
}

func (d *CenterRegistryDiscovery) Get(mode SelectMode) (string, error) {
	if err := d.Refresh(); err != nil {
		return "", err
	}
	return d.MultiServersDiscovery.Get(mode)
}

func (d *CenterRegistryDiscovery) GetAll() ([]string, error) {
	if err := d.Refresh(); err != nil {
		return nil, err
	}
	return d.MultiServersDiscovery.GetAll()
}
```

