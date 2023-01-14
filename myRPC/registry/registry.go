package registry

import (
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

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
	defaultTimeout = time.Minute * 5
)

func New(timeout time.Duration) *CenterRegistry {
	return &CenterRegistry{
		timeout: timeout,
		servers: make(map[string]*ServerItem),
	}
}

var DefaultRegister = New(defaultTimeout)

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
