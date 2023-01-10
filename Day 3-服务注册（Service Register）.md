## Day 3-服务注册（Service Register）

第一天我们实现了编解码器Codec，第二天实现了异步和并发接收回复和发送请求的客户端，但是我们对于整个调用过程，只是简单的打印出接收的参数，并没有实具体处理的逻辑，今天要实现的就是

- 通过反射实现服务注册
- 在服务端实现服务调用

### 如何将结构体映射为服务

RPC设计的初衷就是，希望客户端能像调用本地程序一样调用远程的服务，那么如何将一个程序映射为服务呢？同上节所讲，这个函数的形式仍然是

```go
func (t *T) MethodName(argType T1, replyType *T2) error
```

如果客户端发过来的请求是服务的方法名和已经序列化之后的字节流Argv

```json
{
	"ServiceMethod": "T.MethodName",
    "Argv": "01010010..."
}
```

服务端拿到这个数据后，如果采用硬编码的方式实现这个功能，大概就是

```go
switch req.ServiceMethod {
    case "T.MethodName":
        t := new(t)
        reply := new(T2)
        var argv T1
        gob.NewDecoder(conn).Decode(&argv)
        err := t.MethodName(argv, reply)
        server.sendMessage(reply, err)
    case "Foo.Sum":
        f := new(Foo)
        ...
}
```

那么对于每一个方法，我们都需要一个case去手动的解析并执行对应的逻辑，这个时候就需要我们在反射中学到的”自描述“和”自控制“的性质，引入反射来实现从`方法名 -> 调用对应方法`的自动映射

```
switch req.ServiceMethod {
    case "T.MethodName":
        t := new(t)
        reply := new(T2)
        var argv T1
        gob.NewDecoder(conn).Decode(&argv)
        err := t.MethodName(argv, reply)
        server.sendMessage(reply, err)
    case "Foo.Sum":
        f := new(Foo)
        ...
}
```

### 复习一下反射

反射主要是通过reflect包下的Type和Value两个结构体来完成的操作：

#### Type

```go
//TypeOf 返回一个以reflect.Type表示的i，这是使用类型反射的入口
reflect.TypeOf(i interface) Type

//Elem 返回接收者t内元素的Type
//注意：接收者t的Kind是Array Chan Map Ptr或Slice,其它类型会panic
func (t *rtype) Elem() Type

//Implements 返回接收者t是否实现了u所表示的Interface
func (t *rtype) Implements(u Type) bool

//Kind 返回接收者t的类型，有reflect.String、reflect.Struct、reflect.Ptr和reflect.Interface等
func (t *rtype) Kind() Kind

//NumFiled 返回接收者t表示的结构体内的字段数
//NumFiled 注意：接收者t的Kind必须是Struct，其它（包括结构体指针）会panic
func (t *rtype) NumFiled() int

//Filed 返回以StructField表示的接收者t表示的结构体内的第i个字段
//Filed 注意：接收者t的Kind必须是Struct，其它（包括结构体指针）会panic
func (t *rtype) Filed(i int) StructField

//NumMethod 返回接收者t的方法数
//NumMethod 注意方法的接收者是结构体还是结构体的指针 
func (t *rtype) NumMethod() int

//Method 返回以Method表示的接收者t所表示的第i个方法
func (t *rtype) Method(i int) Method

//NumIn 返回接收者t所表示的函数的参数的数目
//NumIn 接收者t的Kind必须是Func，其它类型会panic
func (t *rtype) NumIn() int

//In 返回接收者t所表示的函数的第i个参数的Type
//In 接收者t的Kind必须是Func，其它类型会panic
func (t *rtype) In(i int) Type

//NumOut 返回接收者t所表示的函数的返回值的数目
//NumOut 接收者t的Kind必须是Func，其它类型会panic
func (t *rtype) NumOut() int

//Out 返回接收者t所表示的函数的第i个返回值的Type
//Out 接收者t的Kind必须是Func，其它类型会panic
func (t *rtype) Out(i int) Type

//StructField 截选部分
type StructField struct {
	Name string			// Name is the field name.
	Type      Type      // field type
	Tag       StructTag // field tag string
	Anonymous bool      // is an embedded field
}

```

#### Value

```go
//ValueOf 返回一个以reflect.Value表示的i，这是使用值反射的入口
reflect.ValueOf(i interface) Value

//Interface 返回Value的Interface{}表示，会重新分配内存，然后可以强转成某种类型
//Interface 注意：如果接收者是通过访问未导出字段获得的，将会panic
func (v value) Interface() (i interface{})

//Kind 返回接收者v的类型，有reflect.String、reflect.Struct、reflect.Ptr和reflect.Interface等
func (v Value) Kind() Kind

//Elem 如果接收者v的Kind是Ptr，该方法将返回一个以Value表示的该指针指向的结构体
//如果接收者v的Kind是Interface，该方法将返回一个以Value表示的Interface所包含的结构体
func (v Value) Elem() Value

//NumFiled 返回接收者v表示的结构体内的字段数
//NumFiled 注意：接收者v的Kind必须是reflect.Struct，其它（包括结构体指针）会panic
func (v Value) NumFiled() int

//Filed 返回以Value表示的接收者v表示的结构体内的第i个字段
//Filed 注意：接收者v的Kind必须是reflect.Struct，其它（包括结构体指针）会panic
func (v Value) Filed(i int) Value

//NumMethod 返回接收者v的方法数
//NumMethod 注意方法的接收者是结构体还是结构体的指针 
func (v Value) NumMethod() int

//Method 返回以Value表示的接收者v表示的第i个方法
func (v Value) Method(i int) Value

//Call 调用接收者v所表示的函数，in为函数的参数
//Call 注意：接收者v的Kind必须是reflect.Func
func (v Value) Call(in []Value) []Value

//CanSet 返回接收者v的Value是否是可修改的，如果CanSet is false，调用Set方法会panic
//CanSet 注意：接收者必须是可寻址的（指针），且Set的字段为导出的
func (v Value) CanSet() bool

//SetString 修改接收者v的值，其中v的Kind必须是reflect.String，否则会panic
func (v Value) SetString(x string)

//String 返回接收者v的值，其中v的Kind要是reflect.String，其它则会返回"<T Value>"，T为v的Kind
func (v Value) String() string

```

### 通过反射实现Service

#### methodType

对于一个方法的类型，我们抽象出了一个结构体`methodType`来包含它的完整信息：

- method：方法本身
- ArgType：第一个参数的类型（这里的第1个是因为我们在用反射读入参数时，第0个入参是method自身）
- ReplyType：第二个参数的类型
- numcalls：后续统计方法调用次数时会用到

```go
type methodType struct {
	method    reflect.Method
	ArgType   reflect.Type
	ReplyType reflect.Type
	numCalls  uint64
}
```

然后就是对应的构造函数，由于第二个参数必须传入一个指针，所以对应的处理逻辑也有略微的差异，这里要说明两个方法：

1. `reflect.New()`它在包中的声明是`func New(typ Type) Value`入参是一个`Type`，返回值是一个指向指定类型的新零值的指针
2. `Type.Elem()`，对一个type的Elem方法，即为获取这个指针指向的元素类型，等效于对一个指针变量做取值操作`*`

```go
func (m *methodType) newArgv() (argv reflect.Value) {
	// argv may be a Ptr type or just a Value type
	if m.ArgType.Kind() == reflect.Ptr {
		argv = reflect.New(m.ArgType.Elem())
	} else {
		argv = reflect.New(m.ArgType).Elem()
	}
	return argv
}

func (m *methodType) newReplyv() reflect.Value {
	// reply must be a pointer type
	replyv := reflect.New(m.ReplyType.Elem())
	switch m.ReplyType.Elem().Kind() {
	case reflect.Map:
		replyv.Elem().Set(reflect.MakeMap(m.ReplyType.Elem()))
	case reflect.Slice:
		replyv.Elem().Set(reflect.MakeSlice(m.ReplyType.Elem(), 0, 0))
	}
	return replyv
}

```

#### Service

接下来就是抽象出服务结构体

- name：服务名，即`T.MethodName`中的`T`
- typ：结构体的类型
- rcvr：结构体实例本身，用于后续获取每个方法和结构体名
- method：一个`string-*methodType`的map，用于存储映射结构体所有符合条件的方法

```go
type service struct {
	name   string
	typ    reflect.Type
	rcvr   reflect.Value
	method map[string]*methodType
}
```

然后仍然是构造函数，`newService`的入参是一个空接口，传入一个结构体，然后通过反射依次获取

```
1.reflect.ValueOf()
这个方法的入参是interface{}，出参是Value，官方给的注释如下，即返回存储在interface中具体的值，也就是获取值对象
// ValueOf returns a new Value initialized to the concrete value
// stored in the interface i. ValueOf(nil) returns the zero Value.

2.为什么使用reflect.Indirect(v).Type().Name()?
这里传入的rcvr可能是一个结构体实例，也可能是一个结构体指针，用reflect.Indirect().Type().Name(),不管它是哪种类型，最后都能正确地获得到服务的名字
```

这里的`registerMethods`方法把传入的结构体中的方法进行遍历并过滤出符合要求的方法放入service的map中

- 两个导出或内置类型的入参（结构体自身和argType）
- 一个出参，且必须是`error`类型

```go
func newService(rcvr interface{}) (s *service) {
	s.rcvr = reflect.ValueOf(rcvr)
	s.name = reflect.Indirect(s.rcvr).Type().Name()
	s.typ = reflect.TypeOf(rcvr)
	if !ast.IsExported(s.name) {
		log.Fatalf("rpc server: %s is not a valid service name", s.name)
	}
	s.registerMethods()
	return
}

func (s *service) registerMethods() {
	s.method = make(map[string]*methodType)
	for i := 0; i < s.typ.NumMethod(); i++ {
		method := s.typ.Method(i)
		mType := method.Type
		if mType.NumIn() != 3 || mType.NumOut() != 1 {
			continue
		}
		if mType.Out(0) != reflect.TypeOf((*error)(nil)).Elem() {
			continue
		}
		argType, replyType := mType.In(1), mType.In(2)
		if !isExportedOrBuiltinType(argType) || !isExportedOrBuiltinType(replyType) {
			continue
		}
		s.method[method.Name] = &methodType{
			method:    method,
			ArgType:   argType,
			ReplyType: replyType,
		}
		log.Printf("rpc server: register %s.%s\n", s.name, method.Name)
	}
}

func isExportedOrBuiltinType(t reflect.Type) bool {
	return ast.IsExported(t.Name()) || t.PkgPath() == ""
}
```

#### 实现调用函数call

最后就是实现对函数的调用

```go
func (s *service) call(m *methodType, argv, replyv reflect.Value) error {
	atomic.AddUint64(&m.numCalls, 1)
	f := m.method.Func
	returnValues := f.Call([]reflect.Value{s.rcvr, argv, replyv})
	if errInter := returnValues[0].Interface(); errInter != nil {
		return errInter.(error)
	}
	return nil
}
```

### 集成到服务端

我们已经通过反射把结构体映射为自控制的服务，但是还没有实现处理请求的过程，对此，我们需要做的工作有：

1. 根据入参类型，把请求中的body反序列化
2. 调用`service.call`完成方法的调用
3. 将`reply`序列化为字节流，构造响应报文并返回

涉及到的函数主要是`readRequest `和`handleRequest`

#### 把service注册到Server中

由于服务端是并发的，所以我们需要一个线程安全的数据结构`sync.Map`来注册服务，同时`Register`方法帮助我们把服务添加到Map中去，`findService`则是根据传入的`Service.MethodName`返回对应的`*service, *methodType`

```go
// Server represents an RPC server
type Server struct {
	serviceMap sync.Map
}

func (server *Server) Register(rcvr interface{}) error {
	s := newService(rcvr)
	if _, dup := server.serviceMap.LoadOrStore(s.name, s); dup {
		return errors.New("rpc: service already defined: " + s.name)
	}
	return nil
}

func (server *Server) findService(serviceMethod string) (svc *service, mtype *methodType, err error) {
	dot := strings.LastIndex(serviceMethod, ".")
	if dot < 0 {
		err = errors.New("rpc server: service/method request ill-formed: " + serviceMethod)
		return
	}
	serviceName, methodName := serviceMethod[:dot], serviceMethod[dot+1:]
	svci, ok := server.serviceMap.Load(serviceName)
	if !ok {
		err = errors.New("rpc server: can't find service " + serviceName)
		return
	}
	svc = svci.(*service)
	mtype = svc.method[methodName]
	if mtype == nil {
		err = errors.New("rpc server: can't find method " + methodName)
	}
	return
}
```

#### 补全readRequest

首先要在一次请求中加上`methodType`和`service`，然后就是调用`findService`来获取对应的`mtype`和`svc`，根据获取到的`mtype`来调用`newArgv()` 和 `newReplyv()` 两个方法创建出两个入参实例，然后通过 `cc.ReadBody()` 将请求报文反序列化为第一个入参 `argv`，在这里同样需要注意 `argv `可能是值类型，也可能是指针类型，所以处理方式有点差异

```go
// request stores all information of a call
type request struct {
	h            *codec.Header // header of request
	argv, replyv reflect.Value // argv and replyv of request
	mtype        *methodType
	svc          *service
}

func (server *Server) readRequest(cc codec.Codec) (*request, error) {
	h, err := server.readRequestHeader(cc)
	if err != nil {
		return nil, err
	}
	req := &request{h: h}
	req.svc, req.mtype, err = server.findService(h.ServiceMethod)
	if err != nil {
		return req, nil
	}
	req.argv = req.mtype.newArgv()
	req.replyv = req.mtype.newReplyv()

	argvi := req.argv.Interface()
	if req.argv.Type().Kind() != reflect.Ptr {
		argvi = req.argv.Addr().Interface()
	}

	if err = cc.ReadBody(argvi); err != nil {
		log.Println("rpc server: read argv err:", err)
		return req, err
	}
	return req, nil
}
```

#### 补全handleRequest

这个很简单，只需要通过`svc`调用对应的方法，然后把参数传进去，最后还是通过`sendResponse`序列化即可

```go
func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup) {
	defer wg.Done()
	err := req.svc.call(req.mtype, req.argv, req.replyv)
	if err != nil {
		req.h.Error = err.Error()
		server.sendResponse(cc, req.h, invalidRequest, sending)
		return
	}
	server.sendResponse(cc, req.h, req.replyv.Interface(), sending)
}
```

## 总结

整个流程大致可以概括为：

1. 首先定义一个结构体（服务），和对应的结构体方法
2. 通过暴露给用户的`Register`方法把这个服务注册到实例化`Server`的线程安全的`Map`中去
3. 客户端通过暴露的`Call`方法调用服务，传入`Service.Method` 、`Args `、 `Reply` 三个参数
4. 编解码过程略去
5. 服务端先从序列化后的`request`中调用`readRequestHeader`读出`Service.Method`
6. 然后调用`findService`从`sync.Map`中取出对应的服务，将入参和出参实例化出来
7. 再从取出的服务中去取对应的方法

