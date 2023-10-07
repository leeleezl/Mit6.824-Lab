
## MapReduce 简介
MapReduce的思想是，应用程序设计人员和分布式运算的使用者，只需要写简单的Map函数和Reduce函数，而不需要知道任何有关分布式的事情，MapReduce框架会处理剩下的事情。
Map函数使用一个key和一个value作为参数。入参中，key是输入文件的名字，value是输入文件的内容。
Reduce函数的入参是某个特定key的所有实例（Map输出中的key-value对中，出现了一次特定的key就可以算作一个实例）。所以Reduce函数也是使用一个key和一个value作为参数，其中value是一个数组，里面每一个元素是Map函数输出的key的一个实例的value。
## MapReduce实现
### Coordinator
<a>mr/coodinator.go</a>
Coordinator充当一个协调者或者master的角色，主要负责将任务分配给worker
Coordinator 主要维护了两个任务列表，Maper任务和Reducer任务，以及两种任务的完成状态，我们来看一下Coordinator的结构体：
```go
type Coordinator struct {
	// Your definitions here.
	mu              sync.Mutex
	MapperFinished  bool           // mapper是否全部完成
	ReducerFinished bool           // reducer是否全部完成
	Mappers         []*MapperTask  // mapper任务
	Reducers        []*ReducerTask // reducer任务
}
```

其中MapperTask和ReducerTask都维护了可以完整代表一个map任务和reduce任务的属性：
```go
type MapperTask struct {
	Index        int       // 任务编号
	Assigned     bool      // 是否分配
	AssignedTime time.Time // 分配时间
	IsFinished   bool      // 是否完成

	InputFile    string // 输入文件
	ReducerCount int    // 有多少路reducer

	timeoutTimer *time.Timer // 任务超时
}

type ReducerTask struct {
	Index        int       // 任务编号
	Assigned     bool      // 是否分配
	AssignedTime time.Time // 分配时间
	IsFinished   bool      // 是否完成

	MapperCount int //	有多少路mapper

	timeoutTimer *time.Timer // 任务超时
}
```
Coodinator要暴露出两个方法给worker通过rpc进行调用，分别是FetchTask()和UpdateTask()
>Go RPC 的函数只有符合下面的条件才能被远程访问，不然会被忽略，详细的要求如下：
>	1. 函数必须是导出的 (首字母大写)
>	2. 必须有两个导出类型的参数
>	3. 第一个参数是接收的参数，第二个参数是返回给客户端的参数，第二个参数必须是指针类型的
>	4. 函数还要有一个返回值 error
>正确的 RPC 函数格式如下：
>`func (t *T) MethodName(argType T1, replyType *T2) error`

FetchTask为worker分配任务，当worker通过rpc调用FetchTask，coordinator会检查map任务是否全部完成，若没完成便分配map任务，若完成则同理检查reduce任务。
```go
func (c *Coordinator) FetchTask(request *FetchTaskRequest, response *FetchTaskResponse) (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.MapperFinished {   //如果mapper没完成
		for _, mapper := range c.Mappers {   //遍历mappers任务列表
			if mapper.Assigned || mapper.IsFinished {
				continue
			}
			c.startMapper(mapper)
			task := *mapper // 副本
			response.MapperTask = &task
			return
		}
		return // 所有mapper任务都分配出去了，那么暂时没有工作了
	}
	if !c.ReducerFinished {   
		for _, reducer := range c.Reducers {
			if reducer.Assigned || reducer.IsFinished {
				continue
			}
			c.startReducer(reducer)
			task := *reducer
			response.ReducerTask = &task
			return
		}
		return // 所有reducer任务都分配出去了，那么暂时没有工作了
	}
	response.AllFinished = true
	return
}
```
UpdateTask主要是来更新任务状态的，拿到worker的请求后，如果是Mapper任务，则遍历coordinator的Mapper任务列表，找到此mapper任务，然后更改任务状态；reducer任务同理。
**注意：每次修改一个任务的状态后要检查对应类型的任务是否全部完成**
```go
func (c *Coordinator) UpdateTask(request *UpdateTaskRequest, response *UpdateTaskResponse) (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if request.Mapper != nil {
		MapperFinished := true
		for _, mapper := range c.Mappers {
			if mapper.Index == request.Mapper.Index && mapper.Assigned && !mapper.IsFinished {
				c.finishMapper(mapper)
			}
			MapperFinished = MapperFinished && mapper.IsFinished
		}
		c.MapperFinished = MapperFinished   //检查mapper任务是否全部完成
	}
	if request.Reducer != nil {
		ReducerFinished := true
		for _, reducer := range c.Reducers {
			if reducer.Index == request.Reducer.Index && reducer.Assigned && !reducer.IsFinished {
				c.finishReducer(reducer)
			}
			ReducerFinished = ReducerFinished && reducer.IsFinished
		}
		c.ReducerFinished = ReducerFinished   //检查reducer任务是否完成
	}
	return
}
```
## Worker
<a>mr/worker.go</a>
worker 的逻辑相对简单一些主要就是不断轮训做任务即可，遇到Map任务调用doMapperTask()，遇到Reduce任务就调用doReduceTask().
```go
func Worker(mapf func(string, string) []KeyValue,
	reducef func(string, []string) string) {
	for {
		resp := CallFetchTask()
		if resp == nil {
			continue
		}
		if resp.AllFinished {
			return
		}
		if resp.MapperTask != nil {
			// 做mapper的事情
			doMapperTask(resp.MapperTask, mapf)
		}
		if resp.ReducerTask != nil {
			// 做reducer的事情
			doReducerTask(resp.ReducerTask, reducef)
		}
	}
}
```
doMapperTask()：
在doMapperTask()方法中，进行Mapper的shuffle，经过Mapper后，输出的是Key/Value形式的，将这个键值对的key做hash%nReduce，每个mapper输出nReduce个中间文件来存储这些键值对。
```go
func doMapperTask(mapperTask *MapperTask, mapf func(string, string) []KeyValue) {
	file, err := os.Open(mapperTask.InputFile)
	if err != nil {
		log.Fatalf("cannot open %v", mapperTask.InputFile)
	}
	content, err := ioutil.ReadAll(file)
	if err != nil {
		log.Fatalf("cannot read %v", mapperTask.InputFile)
	}
	file.Close()
	kva := mapf(mapperTask.InputFile, string(content))

	// mapper的shuffle
	// 10个reducer
	// 每1个mapper输出10路文件，对key做hash%10
	reducerKvArr := make([][]KeyValue, mapperTask.ReducerCount)

	for _, kv := range kva {
		reducerNum := ihash(kv.Key) % mapperTask.ReducerCount
		reducerKvArr[reducerNum] = append(reducerKvArr[reducerNum], kv)
	}

	for i, kvs := range reducerKvArr {
		sort.Sort(ByKey(kvs))

		filename := fmt.Sprintf("mr-%d-%d", mapperTask.Index, i)
		file, err := os.Create(filename + ".tmp")
		if err != nil {
			log.Fatalf("cannot write %v", filename+".tmp")
		}
		enc := json.NewEncoder(file)
		for _, kv := range kvs {
			err := enc.Encode(&kv)
			if err != nil {
				log.Fatalf("cannot jsonencode %v", filename+".tmp")
			}
		}
		file.Close()
		os.Rename(filename+".tmp", filename)
	}

	CallUpdateTaskForMapper(mapperTask)
}
```
doReducerTask():
在doReducerTask()方法中，reducer拿到自己的中间文件后，将自己对应的所有中间文件中的键值对全部取出后通过key进行排列，程序遍历排序后的中间数据，对于每一个唯一的中间 key 值，Reduce程序将这个 key 值和它相关的中间 value 值的集合传递给用户自定义的 Reduce 函数。Reduce 函数的输出被追加到所属分区的输出文件。
```go
func doReducerTask(reducerTask *ReducerTask, reducef func(string, []string) string) {
	kvs := make([]KeyValue, 0)
	for i := 0; i < reducerTask.MapperCount; i++ {
		filename := fmt.Sprintf("mr-%d-%d", i, reducerTask.Index)
		fp, err := os.Open(filename)
		if err != nil {
			log.Fatalf("cannot open %v", filename)
		}
		dec := json.NewDecoder(fp)
		for {
			var kv KeyValue
			if err := dec.Decode(&kv); err != nil {
				break
			}
			kvs = append(kvs, kv)
		}
	}

	sort.Sort(ByKey(kvs)) // 按key排序的k-v列表

	ofileName := fmt.Sprintf("mr-out-%d", reducerTask.Index)
	ofile, err := os.Create(ofileName + ".tmp")
	if err != nil {
		log.Fatalf("cannot open %v", ofileName+".tmp")
	}

	// [i,j]
	i := 0
	for i < len(kvs) {
		j := i + 1
		for j < len(kvs) && kvs[j].Key == kvs[i].Key {
			j++
		}
		values := []string{}
		for k := i; k < j; k++ {
			values = append(values, kvs[k].Value)
		}
		output := reducef(kvs[i].Key, values)
		fmt.Fprintf(ofile, "%v %v\n", kvs[i].Key, output)
		i = j
	}
	ofile.Close()
	os.Rename(ofileName+".tmp", ofileName)
	CallUpdateTaskForReducer(reducerTask)
}
```