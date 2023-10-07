### 任务
通过添加保存和恢复持久状态的代码，完成raft.go中的`persist()` 和 `readPersist()` 函数，需要将状态编码（或“序列化”）为字节数组，以便将其传递给Persister。使用labgob编码器。根据Raft论文，我们只需要持久化 currentTerm, votedFor 和 logs 三个数据。

`persist()`：将状态持久化到磁盘中
`readPersist()`：当节点重启时，会重新读取状态恢复

在更改 currentTerm, votedFor 和 logs 的地方我们都需要调用`persist()`方法进行持久化
### 代码
```go
func (rf *Raft) persist() {
	rf.persister.SaveRaftState(rf.encodeState())
}

func (rf *Raft) readPersist(data []byte) {
	if data == nil || len(data) == 0 {
		return
	}
	r := bytes.NewBuffer(data)
	d := labgob.NewDecoder(r)
	var currentTerm, votedFor int
	var logs []Entry
	if d.Decode(&currentTerm) != nil ||
		d.Decode(&votedFor) != nil ||
		d.Decode(&logs) != nil {
	}
	rf.currentTerm, rf.votedFor, rf.logs = currentTerm, votedFor, logs
	//刚刚读取完持久化信息，日志还没被执行
	rf.lastApplied, rf.commitIndex = rf.logs[0].Index, rf.logs[0].Index
}

//将状态序列化为字节数组
//只需要持久化 currentTerm， votedFor， logs
func (rf *Raft) encodeState() []byte {
	w := new(bytes.Buffer)
	e := labgob.NewEncoder(w)
	e.Encode(rf.currentTerm)
	e.Encode(rf.votedFor)
	e.Encode(rf.logs)
	return w.Bytes()
}
```