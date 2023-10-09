对于一个长期运行的服务来说，永远记住完整的 Raft 日志是不切实际的。如果没有压缩⽇志的⽅法，最终将导致可⽤性问题：即服务器存储空间不⾜，或者 启动时间太⻓。因此，任何实际系统都需要某种形式的⽇志压缩。
### 实现
1. 当Raft认为 Log 的体积过于庞大时，Raft 会要求应用程序在 Log 的某个特定位置，对其状态做一次快照，之后会丢弃这个点之前的 Log，`Snapshot()`方法即为执行快照的方法，为了将快照点之前的 Log 删掉并且将切片的这部分空间占用的内存回收掉，对切片进行内存优化在方法`shrinkEntriesArray()`中实现。
```go
func (rf *Raft) Snapshot(index int, snapshot []byte) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	snapshotIndex := rf.getFirstLog().Index
	if index <= snapshotIndex {
		return
	}
	//丢弃index 之前的log
	rf.logs = shrinkEntriesArray(rf.logs[index-snapshotIndex:])
	rf.logs[0].Command = nil
	rf.persister.SaveStateAndSnapshot(rf.encodeState(), snapshot)
}
```
2. 当follower的Log过于落后，和leader相比，部分缺少的log在leader那儿都已经进快照了，那么leader就需要给follower调用InstallSnapshot RPC。Follower在接到 InstallSnapshot RPC 后，会将应用 Snapshot 的信息发送到 ApplyCh 进行异步处理，所以此 Rpc 的入口即为 在 AppendEntries 处理日志冲突时，节点的 prevLogIndex 小于 Leader 的 FirstLogIndex，这个时候 Leader 就需要发送 快照信息给 Follower。
```go
func (rf *Raft) InstallSnapshot(request *InstallSnapshotRequest, response *InstallSnapshotResponse) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	response.Term = rf.currentTerm
	if request.Term < rf.currentTerm {
		return
	}
	if request.Term > rf.currentTerm {
		rf.currentTerm = request.Term
		rf.votedFor = -1
		rf.persist()
	}
	rf.ChangeState(StateFollower)
	rf.electionTimer.Reset(RandomizedElectionTimeout())
	//快照太旧了
	if request.LastIncludedIndex <= rf.commitIndex {
		return
	}
	go func() {
		rf.applyCh <- ApplyMsg{
			SnapshotValid: true,
			Snapshot:      request.Data,
			SnapshotTerm:  request.LastIncludedTerm,
			SnapshotIndex: request.LastIncludedIndex,
		}
	}()
}
```
3. 在 config.go 中的`applierSnap()`方法中我们可以看到，它会监听 applyCh 中的消息，发现 ApplyMsg 的 SnapshotValid 为 true 时，就会调用此 Raft 节点的 `CondInstallSnapshot()`方法，此方法和 `Snapshot()`方法大同小异
```go
func (rf *Raft) CondInstallSnapshot(lastIncludedTerm int, lastIncludedIndex int, snapshot []byte) bool {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	// outdated snapshot
	if lastIncludedIndex <= rf.commitIndex {
		return false
	}
	if lastIncludedIndex > rf.getLastLog().Index {
		rf.logs = make([]Entry, 1)
	} else {
		rf.logs = shrinkEntriesArray(rf.logs[lastIncludedIndex-rf.getFirstLog().Index:])
		rf.logs[0].Command = nil
	}
	// update dummy entry with lastIncludedTerm and lastIncludedIndex
	rf.logs[0].Term, rf.logs[0].Index = lastIncludedTerm, lastIncludedIndex
	rf.lastApplied, rf.commitIndex = lastIncludedIndex, lastIncludedIndex

	rf.persister.SaveStateAndSnapshot(rf.encodeState(), snapshot)
	return true
}
```
