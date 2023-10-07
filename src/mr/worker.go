package mr

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io/ioutil"
	"log"
	"net/rpc"
	"os"
	"sort"
)

//
// Map functions return a slice of KeyValue.
//
type KeyValue struct {
	Key   string
	Value string
}

//
// use ihash(key) % NReduce to choose the reduce
// task number for each KeyValue emitted by Map.
//
func ihash(key string) int {
	h := fnv.New32a()
	h.Write([]byte(key))
	return int(h.Sum32() & 0x7fffffff)
}

//
// main/mrworker.go calls this function.
//
func Worker(mapf func(string, string) []KeyValue,
	reducef func(string, []string) string) {

	// Your worker implementation here.
	//woker 轮询做任务即可
	for {
		resp := CallFetchTask()
		if resp == nil {
			continue
		}
		if resp.AllFinished {
			return
		}
		if resp.MapperTask != nil {
			doMapperTask(resp.MapperTask, mapf)
		}
		if resp.ReducerTask != nil {
			doReducerTask(resp.ReducerTask, reducef)
		}
	}

	// uncomment to send the Example RPC to the coordinator.
	// CallExample()

}

//rpc调用FetchTask
func CallFetchTask() (ret *FetchTaskResponse) {
	request := FetchTaskRequest{}
	response := FetchTaskResponse{}

	if call("Coordinator.FetchTask", &request, &response) {
		ret = &response
	}
	return

}

func doMapperTask(mapper *MapperTask, mapf func(string, string) []KeyValue) {
	//加载文件
	file, err := os.Open(mapper.Inputfile)
	if err != nil {
		log.Fatalf("cannot open %v", mapper.Inputfile)
	}
	//读文件
	content, err := ioutil.ReadAll(file)
	if err != nil {
		log.Fatalf("cannot red %v", mapper.Inputfile)
	}
	file.Close()
	//交给mapf
	kva := mapf(mapper.Inputfile, string(content)) //kav = (a, 1),(b, 1)...
	//shuffle
	//10个reducer 每个mapper输出十个文件，对key做hash(key)
	reducerKvArr := make([][]KeyValue, mapper.ReducerCount) //生成nReduce个文件
	//将kv加入到对应的reducerKvArr中
	for _, kv := range kva {
		reduceNum := ihash(kv.Key) % mapper.ReducerCount
		reducerKvArr[reduceNum] = append(reducerKvArr[reduceNum], kv)
	}

	//输出中间文件
	for i, kvs := range reducerKvArr {
		sort.Sort(ByKey(kvs))

		filename := fmt.Sprintf("mr-%d-%d", mapper.Index, i)
		file, err := os.Create(filename + ".tmp")
		if err != nil {
			log.Fatalf("cannot create file %v", filename)
		}
		enc := json.NewEncoder(file)
		for _, kv := range kvs {
			err := enc.Encode(&kv)
			if err != nil {
				log.Fatalf("cannot jsonencode %v", filename+".tmp")
			}
		}
		file.Close()
		os.Rename(filename+".tmp", filename) //奖临时文件转为非临时
	}
	CallUpdateTaskForMapper(mapper)
}

type ByKey []KeyValue

func (a ByKey) Len() int {
	return len(a)
}
func (a ByKey) Swap(i, j int) {
	a[i], a[j] = a[j], a[i]
}
func (a ByKey) Less(i, j int) bool {
	return a[i].Key < a[j].Key
}

//
// example function to show how to make an RPC call to the coordinator.
//
// the RPC argument and reply types are defined in rpc.go.
//
func CallUpdateTaskForMapper(mapper *MapperTask) {
	req := UpdateTaskRequest{MapperTask: mapper}
	resp := UpdateTaskResponse{}
	call("Coordinator.UpdateTask", &req, &resp)
}

func doReducerTask(reducer *ReducerTask, reducef func(string, []string) string) {
	kvs := make([]KeyValue, 0)
	for i := 0; i < reducer.MapperCount; i++ {
		filename := fmt.Sprintf("mr-%d-%d", i, reducer.Index)
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

	ofileName := fmt.Sprintf("mr-out-%d", reducer.Index)
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
	// fmt.Printf("Reducer[%d] InputFiles=[%v]\n", reducerTask.Index, reduceFiles)
	CallUpdateTaskForReducer(reducer)
}

func CallUpdateTaskForReducer(reducer *ReducerTask) {
	req := UpdateTaskRequest{ReducerTask: reducer}
	resp := UpdateTaskResponse{}
	call("Coordinator.UpdateTask", &req, &resp)
}
func CallExample() {

	// declare an argument structure.
	args := ExampleArgs{}

	// fill in the argument(s).
	args.X = 99

	// declare  a reply structure.
	reply := ExampleReply{}

	// send the RPC request, wait for the reply.
	// the "Coordinator.Example" tells the
	// receiving server that we'd like to call
	// the Example() method of struct Coordinator.
	ok := call("Coordinator.Example", &args, &reply)
	if ok {
		// reply.Y should be 100.
		fmt.Printf("reply.Y %v\n", reply.Y)
	} else {
		fmt.Printf("call failed!\n")
	}
}

//
// send an RPC request to the coordinator, wait for the response.
// usually returns true.
// returns false if something goes wrong.
//
func call(rpcname string, args interface{}, reply interface{}) bool {
	// c, err := rpc.DialHTTP("tcp", "127.0.0.1"+":1234")
	sockname := coordinatorSock()
	c, err := rpc.DialHTTP("unix", sockname)
	if err != nil {
		log.Fatal("dialing:", err)
	}
	defer c.Close()

	err = c.Call(rpcname, args, reply)
	if err == nil {
		return true
	}

	fmt.Println(err)
	return false
}
