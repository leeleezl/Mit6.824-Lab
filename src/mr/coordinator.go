package mr

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"sync"
	"time"
)

type MapperTask struct {
	Index        int
	Assigned     bool
	AssignedTime time.Time
	IsFinished   bool

	Inputfile    string
	ReducerCount int
	timeoutTimer *time.Timer
}

type ReducerTask struct {
	Index        int
	Assigned     bool
	AssignedTime time.Time
	IsFinished   bool

	MapperCount  int
	timeoutTimer *time.Timer
}

type Coordinator struct {
	// Your definitions here.
	mu              sync.Mutex
	MapperFinished  bool
	ReducerFinished bool
	Mappers         []*MapperTask
	Reducers        []*ReducerTask
}

// Your code here -- RPC handlers for the worker to call.

//
// an example RPC handler.
//
// the RPC argument and reply types are defined in rpc.go.
//
func (c *Coordinator) Example(args *ExampleArgs, reply *ExampleReply) error {
	reply.Y = args.X + 1
	return nil
}

func (c *Coordinator) FetchTask(request *FetchTaskRequest, response *FetchTaskResponse) (err error) {
	//上锁
	c.mu.Lock()
	defer c.mu.Unlock()
	//map没执行完给map  没有map了直接返回
	if !c.MapperFinished {
		//遍历 mapper数组
		for _, mapper := range c.Mappers {
			//查看mapper的状态
			if mapper.Assigned || mapper.IsFinished {
				continue
			}
			c.makeMapTask(mapper)
			task := *mapper
			response.MapperTask = &task
			return
		}
		return //所有map分配完了， 没有map了
	}
	//reduce没执行完给reduce 没有reduce了直接返回
	if !c.ReducerFinished {
		for _, reducer := range c.Reducers {
			if reducer.Assigned || reducer.IsFinished {
				continue
			}
			c.makeReduceTask(reducer)
			task := *reducer
			response.ReducerTask = &task
			return
		}
		return
	}
	response.AllFinished = true
	return
}

func (c *Coordinator) makeMapTask(mapper *MapperTask) {
	mapper.Assigned = true
	mapper.AssignedTime = time.Now()
	mapper.timeoutTimer = time.AfterFunc(10*time.Second, c.mapperTimeoutFun(mapper.Index))
}

func (c *Coordinator) makeReduceTask(reducer *ReducerTask) {
	reducer.Assigned = true
	reducer.AssignedTime = time.Now()
	reducer.timeoutTimer = time.AfterFunc(10*time.Second, c.reducerTimeoutFun(reducer.Index))
}

func (c *Coordinator) mapperTimeoutFun(index int) func() {
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()

		if !c.Mappers[index].IsFinished {
			c.Mappers[index].Assigned = false
		}
	}
}

func (c *Coordinator) reducerTimeoutFun(index int) func() {
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()

		if !c.Reducers[index].IsFinished {
			c.Reducers[index].Assigned = false
		}
	}
}

func (c *Coordinator) UpdateTask(req *UpdateTaskRequest, resp *UpdateTaskResponse) (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	//更新mapper任务的状态ssss
	if req.MapperTask != nil {
		MapperFinished := true
		for _, mapper := range c.Mappers {
			if mapper.Index == req.MapperTask.Index && mapper.Assigned && !mapper.IsFinished {
				c.finishMapper(mapper)
			}
			MapperFinished = MapperFinished && mapper.IsFinished
		}
		c.MapperFinished = MapperFinished
	}

	//更新reducer
	if req.ReducerTask != nil {
		ReducerFinished := true
		for _, reducer := range c.Reducers {
			if reducer.Index == req.ReducerTask.Index && reducer.Assigned && !reducer.IsFinished {
				c.finishReducer(reducer)
			}
			ReducerFinished = ReducerFinished && reducer.IsFinished
		}
		c.ReducerFinished = ReducerFinished
	}
	return
}

func (c *Coordinator) finishMapper(mapper *MapperTask) {
	mapper.IsFinished = true
	//结束计时器
	mapper.timeoutTimer.Stop()
}

func (c *Coordinator) finishReducer(reducer *ReducerTask) {
	reducer.IsFinished = true
	//结束计时器
	reducer.timeoutTimer.Stop()
}

//
// start a thread that listens for RPCs from worker.go
//
func (c *Coordinator) server() {
	rpc.Register(c)
	rpc.HandleHTTP()
	//l, e := net.Listen("tcp", ":1234")
	sockname := coordinatorSock()
	os.Remove(sockname)
	l, e := net.Listen("unix", sockname)
	if e != nil {
		log.Fatal("listen error:", e)
	}
	go http.Serve(l, nil)
}

//
// main/mrcoordinator.go calls Done() periodically to find out
// if the entire job has finished.
//
func (c *Coordinator) Done() bool {
	ret := false

	// Your code here.
	ret = c.MapperFinished && c.ReducerFinished
	return ret
}

//
// create a Coordinator.
// main/mrcoordinator.go calls this function.
// nReduce is the number of reduce tasks to use.
//
func MakeCoordinator(files []string, nReduce int) *Coordinator {
	c := Coordinator{}
	fmt.Println(nReduce)
	// Your code here.
	c.Mappers = make([]*MapperTask, 0)
	for i, file := range files {
		mapper := &MapperTask{
			Index:        i,
			Assigned:     false,
			AssignedTime: time.Now(),
			IsFinished:   false,
			Inputfile:    file,
			ReducerCount: nReduce,
		}
		c.Mappers = append(c.Mappers, mapper)
	}

	c.Reducers = make([]*ReducerTask, 0)
	for i := 0; i < nReduce; i++ {
		reducer := &ReducerTask{
			Index:        i,
			Assigned:     false,
			AssignedTime: time.Now(),
			IsFinished:   false,
			MapperCount:  len(files),
		}
		c.Reducers = append(c.Reducers, reducer)
	}
	c.server()
	return &c
}
