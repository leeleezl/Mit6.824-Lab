一旦领导者被选举出来，他就开始为客户端提供服务，客户端的请求包含一条被复制状态机执行的指令，领导者把这条指令作为一条新的日志条目附加到日志中去，然后并行的发送RPCs给其他服务器。
### 论文中的一些注意点
1. 每一条日志都存储一条状态机指令和和从领导者收到这个指令时期的任期号.
```go
type Entity struct {
	Command interface{}
	Term int
}
```
2. 领导者将创建的日志复制到大多是的服务器上的时候，日志就可以被提交.
3. 领导者跟踪了最大的将会被提交的日志的索引，并且索引值会被包含在未来的所有附加日志 RPCs（包括心跳），这样服务器才能最终知道领导者的提交位置.
4. 匹配特性：
	1. 如果在不同的日志中的两个条目拥有相同的索引和任期，那么他们存储了相同的指令
	2. 如果在不同的日志中的两个条目拥有相同的索引和任期，那么他们之前的所有日志条目也都相同
5. 在附加日志 RPC 的时候，领导者会把新的日志条目紧接着之前的日志条目的索引号和任期号也发送过去，如果 follower 在自己的日志中找不到包含相同索引和任期号的日志条目，那么他就会拒绝接收新的日志条目。
6. 领导者处理不一致行为的方法是强制 follower 直接复制自己的日志，所以 follower 中的冲突日志会被领导者的日志覆盖。
### 程序的执行流程
在 `Make()` 时，为每一个节点启动一个 replicator 协程
```go
for i := 0; i < len(peers); i++ {
	rf.matchIndex[i], rf.nextIndex[i] = 0, lastLog.Index+1
	if i != rf.me {
		rf.replicatorCond[i] = sync.NewCond(&sync.Mutex{})
		// start replicator goroutine to replicate entries in batch
		go rf.replicator(i)
	}
}
```
在 `replicator(peer int)` 方法中，我们去循环判断传进来的 peer 是否需要进行日志复制，不需要则让出 CPU
```go
func (rf *Raft) replicator(peer int) {
	rf.replicatorCond[peer].L.Lock()
	defer rf.replicatorCond[peer].L.Unlock()
	for rf.killed() == false {
		for !rf.needReplicating(peer) {
			rf.replicatorCond[peer].Wait()
		}
		rf.replicateOneRound(peer)
	}
}
```
若需要进行日志的同步，则调用 `replicateOneRound(peer int)` 方法，在这个方法中，我们发送 RPC 消息并调用`handleAppendEntriesResponse(peer, request, response)`处理RPC结果
```go
func (rf *Raft) replicateOneRound(peer int) {
	rf.mu.RLock()
	if rf.state != StateLeader {
		rf.mu.RUnlock()
		return
	}
	prevLogIndex := rf.nextIndex[peer] - 1
	
	request := rf.genAppendEntriesRequest(prevLogIndex)
	rf.mu.RUnlock()
	response := new(AppendEntriesResponse)
	if rf.sendAppendEntries(peer, request, response) {
		rf.mu.Lock()
		rf.handleAppendEntriesResponse(peer, request, response)
		rf.mu.Unlock()
	}
}
```
发送消息后，Follower会收到AppendEntries RPC，并经过 Follower 的 `AppendEntries()`方法处理，在2B中，Follower 会进行一致性检查
```go
// 一致性检查
if !rf.matchLog(request.PrevLogTerm, request.PrevLogIndex) {
	response.Term, response.Success = rf.currentTerm, false
	lastIndex := rf.getLastLog().Index
	//如果lastLogIndex 小于 leader 的PrelogIndex 说明Follower的日志不够长，有空槽
	if lastIndex < request.PrevLogIndex {
		response.ConflictTerm, response.ConflictIndex = -1, lastIndex+1
	} else { //rf.logs[index-rf.getFirstLog().Index].Term != term follower对应位置的日志任期和leader不同，要返回这个任期的第一个日志的索引
		firstIndex := rf.getFirstLog().Index
		response.ConflictTerm = rf.logs[request.PrevLogIndex-firstIndex].Term
		index := request.PrevLogIndex - 1
		for index >= firstIndex && rf.logs[index-firstIndex].Term == response.ConflictTerm {
			index--
		}
		response.ConflictIndex = index
	}
	return
}
```
一致性检查通过后才可以进行日志的提交
```go
// 通过一致性检查
firstIndex := rf.getFirstLog().Index
for index, entry := range request.Entries {
	if entry.Index-firstIndex >= len(rf.logs) || rf.logs[entry.Index-firstIndex].Term != entry.Term {
		rf.logs = shrinkEntriesArray(append(rf.logs[:entry.Index-firstIndex], request.Entries[index:]...))
		break
	}
}
//选择leaderCommit和  rf.getLastLog().Index 中较小的一个 以便与leader保持一致
rf.advanceCommitIndexForFollower(request.LeaderCommit)
response.Term, response.Success = rf.currentTerm, true
```
Leader 通过 `handleAppendEntriesResponse`方法来处理RPC的结果，如果回复 success，则更新对用 peer 的matchIndex 和 nextIndex，并提交记录。若success为false，如果遇到了更高的 Term 则转变为 Follower，如果Term相等则为日志冲突，随后处理冲突日志
```go
func (rf *Raft) handleAppendEntriesResponse(peer int, request *AppendEntriesRequest, response *AppendEntriesResponse) {
	if rf.state == StateLeader && rf.currentTerm == request.Term {
		if response.Success {
			rf.matchIndex[peer] = request.PrevLogIndex + len(request.Entries) //更新 matchIndex
			rf.nextIndex[peer] = rf.matchIndex[peer] + 1                      //更新nextIndex
			rf.advanceCommitIndexForLeader()
		} else {
			if response.Term > rf.currentTerm {
				//重新选举
				rf.ChangeState(StateFollower)
				rf.currentTerm, rf.votedFor = response.Term, -1
				rf.persist()
			} else if response.Term == rf.currentTerm {
				rf.nextIndex[peer] = response.ConflictIndex
				if response.ConflictTerm != -1 { //对应的槽位没有Log
					firstIndex := rf.getFirstLog().Index
					for i := request.PrevLogIndex; i >= firstIndex; i-- {
						if rf.logs[i-firstIndex].Term == response.ConflictTerm { //找到相等的term
							rf.nextIndex[peer] = i + 1
							break
						}
					}
				}
			}
		}
	}
}
```
最后就是 applier，在程序启动时就循环等待，当`rf.lastApplied < rf.commitIndex`时，则将提交的日志应用到状态机中
```go
func (rf *Raft) applier() {
	for rf.killed() == false {
		rf.mu.Lock()
		for rf.lastApplied >= rf.commitIndex {
			rf.applyCond.Wait()
		}
		firstIndex, commitIndex, lastApplied := rf.getFirstLog().Index, rf.commitIndex, rf.lastApplied
		entries := make([]Entry, commitIndex-lastApplied)
		copy(entries, rf.logs[lastApplied+1-firstIndex:commitIndex+1-firstIndex])
		rf.mu.Unlock()
		for _, entry := range entries {
			rf.applyCh <- ApplyMsg{
				CommandValid: true,
				Command:      entry.Command,
				CommandTerm:  entry.Term,
				CommandIndex: entry.Index,
			}
		}
		rf.mu.Lock()
		rf.lastApplied = Max(rf.lastApplied, commitIndex)
		rf.mu.Unlock()
	}
}
```