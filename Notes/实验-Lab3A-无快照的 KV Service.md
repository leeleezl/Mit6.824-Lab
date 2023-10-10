lab3A需要在lab2的raft基础上封装一个应用层，完成服务端和客户端的逻辑，实现一个分布式kv数据库，实现put，get，append功能。
以下Op所代表的是put，get，append操作。
### client的工作
只有一条，**保证成功发送Op给一个Server**。
会遇到两个问题：
1. 该Server不是Leader。
2. 网络延迟。
### server的工作
1. 保证成功接收到的Op不丢失的同步到集群中的所有server的日志中。
2. 更新kv数据。
3. 每一条Op在每个server上只执行一次。
4. 保证线性一致性。
会遇到的问题：
1. 先来的Op一定要先执行完，再执行下一条Op。
2. 因为网络会延迟，客户端对于同一个Op可能会发送多次。

### client 实现
用 (clientId, commandId)可以唯一标识一个客户端
client 只做一件事，就是成功发送 Op 给 Server
```go
func MakeClerk(servers []*labrpc.ClientEnd) *Clerk {
	return &Clerk{
		servers:   servers,
		leaderId:  0,
		clientId:  nrand(),
		commandId: 0,
	}
}

func (ck *Clerk) Get(key string) string {
	return ck.Command(&CommandRequest{Key: key, Op: OpGet})
}

func (ck *Clerk) Put(key string, value string) {
	ck.Command(&CommandRequest{Key: key, Value: value, Op: OpPut})
}
func (ck *Clerk) Append(key string, value string) {
	ck.Command(&CommandRequest{Key: key, Value: value, Op: OpAppend})
}

func (ck *Clerk) Command(request *CommandRequest) string {
	request.ClientId, request.CommandId = ck.clientId, ck.commandId
	for {
		var response CommandResponse
		if !ck.servers[ck.leaderId].Call("KVServer.Command", request, &response) || response.Err == ErrWrongLeader || response.Err == ErrTimeout {
			ck.leaderId = (ck.leaderId + 1) % int64(len(ck.servers))
			continue
		}
		ck.commandId++
		return response.Value
	}
}
```
服务端
首先看 KVServer 的结构体
```go
type KVServer struct {
	mu      sync.RWMutex
	me      int
	rf      *raft.Raft
	applyCh chan raft.ApplyMsg
	dead    int32 // set by Kill()
	maxraftstate int // snapshot if log grows this big
	// Your definitions here.
	lastApplied    int //最后一个提交applyCh的index
	stateMachine   KVStateMachine
	lastOperations map[int64]OperationContext    //通过记录最后一个命令Id和clientId对应的响应来确定日志是否重复
	notifyChans    map[int]chan *CommandResponse //唤醒客户端
}
```
服务端收到客户端的 RPC 请求后，首先会检查请求是否重复，这里我们通过 clientId 和 commandId 就可以轻松地判断请求是否是重复的，然后通过调用 Raft 的 Start 方法，将Command 发送给 Raft, Raft的Leader 再收到超过半数的 Follower 的成功消息后将会 commit 这个操作，将 Command 包装为 ApplyMsg 发送到 applyCh 中，服务端的 applier() 方法会监听 applyCh，当有消息时，如果是执行 Command 的消息，那么在判断消息没有重复的前提下会将 Command 应用到状态机上，将结果发送到 notifyChan 中，server 的 Command 会监听 notifyChan，当拿到结果后，返回客户端。
```go
func (kv *KVServer) Command(request *CommandRequest, response *CommandResponse) {
	kv.mu.RLock()
	//检查请求是否重复
	if request.Op != OpGet && kv.isDuplicateRequest(request.ClientId, request.CommandId) {
		lastResponse := kv.lastOperations[request.ClientId].LastResponse
		response.Value, response.Err = lastResponse.Value, lastResponse.Err
		kv.mu.RUnlock()
		return
	}
	kv.mu.RUnlock()
	//将操作发送给 raft
	index, _, isLeader := kv.rf.Start(Command{request})
	if !isLeader {
		response.Err = ErrWrongLeader
		return
	}
	kv.mu.Lock()
	ch := kv.getNotifyChan(index)
	kv.mu.Unlock()
	// 监听 notifyChan[index]，applier 将操作应用到状态机后会将结果发送到 notifyChan[index]
	select {
	case result := <-ch:
		response.Value, response.Err = result.Value, result.Err
	case <-time.After(ExecuteTimeout):
		response.Err = ErrTimeout
	}
	//释放
	go func() {
		kv.mu.Lock()
		kv.removeOutdatedNotifyChan(index)
		kv.mu.Unlock()
	}()
}
```

```go
//将提交了的操作应用到状态机中
func (kv *KVServer) applier() {
	for kv.killed() == false {
		// 监听 applyCh， raft的Leader节点在得到大部分节点的相应后可以提交操作，并将操作提交到 applyCh 中
		select {
		case message := <-kv.applyCh:
			if message.CommandValid {
				kv.mu.Lock()
				// 消息过时了
				if message.CommandIndex <= kv.lastApplied {
					kv.mu.Unlock()
					continue
				}
				kv.lastApplied = message.CommandIndex

				var response *CommandResponse
				command := message.Command.(Command)
				if command.Op != OpGet && kv.isDuplicateRequest(command.ClientId, command.CommandId) {
					response = kv.lastOperations[command.ClientId].LastResponse
				} else {
					//应用到状态机上
					response = kv.applyLogToStateMachine(command)
					//不是get操作记得更新lastOperation
					if command.Op != OpGet {
						kv.lastOperations[command.ClientId] = OperationContext{command.CommandId, response}
					}
				}
				// 发送到 notifyCh
				if currentTerm, isLeader := kv.rf.GetState(); isLeader && message.CommandTerm == currentTerm {
					ch := kv.getNotifyChan(message.CommandIndex)
					ch <- response
				}
				kv.mu.Unlock()
			}
		}
	}
}
```