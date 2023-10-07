## Raft 简介
Raft 是一种为了管理复制日志的一致性算法，它拥有与 Paxos 相同的功能和性能，但是算法结构与 Paxos 不同，Raft 将一致性算法分成了几个关键的模块，例如领导人选举，日志复制和安全性，在Lab2A 中我们实现的是领导人选举。
## 领导人选举
Raft 通过选举一个领导人，领导人拥管理日志复制的全部责任来实现一致性。领导人从客户端接受日志条目，然后把日志条目复制到其他服务器上，并且在保证安全性的情况下告诉其他服务器将日志条目应用到各自的状态机上。一个领导人可以发生宕机，当发生宕机时，一个新的领导人就会被选举出来。
根据 Raft 协议，一个应用 Raft 协议的集群在刚启动时，所有节点的状态都是 Follower。由于没有 Leader，Followers 无法与 Leader 保持心跳（Heart Beat），因此，Followers 会认为 Leader 已经下线，进而转为 Candidate 状态。然后，**Candidate 将向集群中其它节点请求投票，同意自己升级为 Leader。如果 Candidate 收到超过半数节点的投票（N/2 + 1），它将获胜成为 Leader。**
## Raft 结构体
raft 所需要的结构体在 raft 论文的图2中均有体现
![](raft.png)
一定要看懂其中每个字段的意思
```go
type Raft struct {
mu sync.RWMutex // Lock to protect shared access to this peer's state
peers []*labrpc.ClientEnd // RPC end points of all peers
persister *Persister // Object to hold this peer's persisted state
me int // this peer's index into peers[]
dead int32 // set by Kill()

applyCh chan ApplyMsg
applyCond *sync.Cond
replicatorCond []*sync.Cond
state NodeState    //节点状态
  
currentTerm int    //节点当前所处的任期
votedFor int     //在本次任期内 将选票投给了哪个节点
logs []Entry     //日志
commitIndex int   //知道的已经提交的最新log
nextInedx []int   //对应 应该下一次发送给各个节点的日志的index
lastApplied int   //节点已经作用在状态机上的日志的index
matchIndex []int  

electionTimer *time.Timer
heartbeatTimer *time.Timer
}
```
## 选举逻辑实现
### 第一阶段：所有的节点都是Follower
一个 Raft 集群刚启动时，所有的节点状态都是 Follower，初始 Term 为0。同时启动选举定时器，选举定时器的超时时间在150到350毫秒内（避免同时发生选举）。
```go
electionTimer: time.NewTimer(RandomizeElectionTimeout()) // 选举定时器随机化，防止各个节点同时发生选举
```
### 第二阶段：Follower 转为 Candidate 并发起投票
没有 Leader，Followers 无法与 Leader 保持心跳，节点启动后，在一个选举定时器周期内，没有收到心跳和投票请求，则转换自身角色为 Candidate，且 Term 自增，并向集群中的所有节点发送投票并重置选举定时器。
在 `ticker()`方法中，我们要做的是一直等待选举定时器和心跳定时器超时的通知，并在超时时做出相应的动作。
```go 
func (rf *Raft) ticker() {
	for rf.killed() == false {
		select {
		case <-rf.electionTimer.C:
			//开始选举
			rf.mu.Lock()
			rf.ChangeState(StateCandidate)
			rf.currentTerm += 1
			rf.StartElection()
			rf.electionTimer.Reset(RandomizeElectionTimeout())
			rf.mu.Unlock()
		case <-rf.heartbeatTimer.C:
			//心跳
			if rf.state == StateLeader {
				rf.BroadcastHeartbeat(true)
				rf.heartbeatTimer.Reset(StableHeartbeatTimeout())
			}
		}
	}	
}
```
- 选举定时器超时，转变角色，将term自增进入下一个任期，并开始选举
```go
//选举定时器超时
case <-rf.electionTimer.C:
			//开始选举
			rf.mu.Lock()
			rf.ChangeState(StateCandidate) //转变为Candidate
			rf.currentTerm += 1 //Term自增
			rf.StartElection() //开始选举
			rf.electionTimer.Reset(RandomizeElectionTimeout()) //重置选举定时器
			rf.mu.Unlock()
```
- 构建`RequestVoteArgs`，本节点将选票投给自己，随后启动`len(peers) - 1`个协程并发 RPC 请求。
```go
func (rf *Raft) StartElection() {
	//构建RPC request
	lastLog := rf.lastLog()
	args := &RequestVoteArgs{
		Term:         rf.currentTerm,
		CandidateId:  rf.me,
		LastLogIndex: lastLog.Index,
		LastLogTerm:  lastLog.Term,
	}
	//本节点将选票投给自己
	rf.votedFor = rf.me
	grantedVotes := 1
	rf.persist()
	//将请求投票的请求发给所有节点
	for peer := range rf.peers {
		if peer == rf.me {
			continue
		}
		//每份请求开启一个协程
		go func(peer int) {
			reply := &RequestVoteReply{}
			if rf.sendRequestVote(peer, args, reply) {
				rf.mu.Lock()
				defer rf.mu.Unlock()
				if rf.currentTerm == args.Term && rf.state == StateCandidate {
					//在这处理 rpc 的reply，在此先不做处理
				}
			}
		}(peer)
	}
}
```
### 第三阶段：投票策略
节点收到投票请求后，会根据以下情况决定是否接受投票请求（每个 follower 刚成为 Candidate 的时候会将票投给自己）
> 请求节点的 Term 大于自己的 Term，且自己尚未投票给其它节点，则接受请求，把票投给它；
> 请求节点的 Term 小于自己的 Term，且自己尚未投票，则拒绝请求，将票投给自己。
- 变为 Candidate 时将选票投给自己
```go
    rf.votedFor = rf.me
	grantedVotes := 1
```
- 节点投票的策略：
```go
func (rf *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) {
	//请求节点的任期小于自己的任期 || 请求节点的任期等于自己的任期并且自己已经投过票了（没有投给它）
	if args.Term < rf.currentTerm ||
		(args.Term == rf.currentTerm && rf.votedFor != -1 && rf.votedFor != args.CandidateId) {
		reply.VoteGranted = false
		reply.Term = rf.currentTerm
		return
	}
	//请求节点的term大于自己的term
	if args.Term > rf.currentTerm {
		rf.ChangeState(StateFollower)
		rf.currentTerm = args.Term
		rf.votedFor = -1
	}
	rf.votedFor = args.CandidateId
	rf.electionTimer.Reset(RandomizeElectionTimeout())
	reply.Term = rf.currentTerm
	reply.VoteGranted = true
}
```
### 第四阶段：收到超过半数的选票Candidate 成为 Leader
一轮选举过后，正常情况下，会有一个 Candidate 收到超过半数节点（N/2 + 1）的投票，它将胜出并升级为 Leader。然后定时发送心跳给其它的节点，其它节点会转为 Follower 并与 Leader 保持同步，到此，本轮选举结束。
- 正常情况收到（N/2 + 1）的投票
```go
if reply.VoteGranted {
	//总选票超过一半竞选成功
	grantedVotes++
	if grantedVotes > len(rf.peers)/2 {
		DPrintf("{Node %v} receives majority votes in term %v", rf.me, rf.currentTerm)
		rf.ChangeState(StateLeader)
		//开启心跳
		rf.BroadcastHeartbeat(true)
	}
} else if reply.Term > rf.currentTerm {
	//放弃
	DPrintf("{Node %v} found a new leader {Node %v} with term %v and steps down in term %v", rf.me, peer, reply.Term, rf.currentTerm)
	rf.ChangeState(StateFollower)
	rf.currentTerm = reply.Term
	rf.votedFor = -1
	rf.persist()
}
```
## 心跳机制实现
**lab2A 只考虑心跳，不考虑 log 的同步**
Raft 集群在正常运行中是不会触发选举的，选举只会发生在集群初次启动或者其它节点无法收到Leader 心跳的情况下。初次启动比较好理解，因为raft节点在启动时，默认都是将自己设置为Follower。收不到 Leader 心跳有两种情况，一种是原来的 Leader 机器 Crash 了，还有一种是发生网络分区，Follower 跟 Leader 之间的网络断了，Follower 以为 Leader 宕机了。
我们在lab2A中日志同步和心跳机制复用一个方法，但是在lab2A中不考虑日志同步的情况，因此 Leader 广播的消息中日志部分为空则代表是心跳消息。我们的广播周期为100ms。当Leader的心跳计时器超时时，`tricker()`会接到通知并发送一次心跳
```go
func (rf *Raft) BroadcastHeartbeat(isHeartBeat bool) {
	for peer := range rf.peers {
		if peer == rf.me {
			continue
		}
		//发送心跳
		if isHeartBeat {
			args := &AppendEntriesArgs{
				Term:     rf.currentTerm,
				LeaderId: rf.me,
			}
			go func(peer int) {
				reply := &AppendEntriesReply{}
				if rf.sendAppendEntries(peer, args, reply) {
					rf.mu.Lock()
					defer rf.mu.Unlock()
					if rf.currentTerm != args.Term {
						return
					}
					if reply.Term > args.Term {
						rf.currentTerm = reply.Term
						rf.ChangeState(StateFollower)
						rf.votedFor = -1
					}
				}
			}(peer)
		}
	}
}
```
raft 论文要求每次接到 RPC 请求是，都要检查请求节点的Term和自己的Term的大小
```go 
func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
	//2A只心跳  //Follower 会接到这个消息并处理
	rf.mu.Lock()
	defer rf.mu.Unlock()
	//每次接到rpc都会检查Term的大小
	if args.Term < rf.currentTerm {
		reply.Success = false
		reply.Term = rf.currentTerm
		return
	}

	//发现更大的任期
	if args.Term > rf.currentTerm {
		rf.currentTerm = args.Term
		rf.votedFor = -1 //增加了任期，这个任期的投票就还没有投
	}
	//继续向下走
	rf.ChangeState(StateFollower) 
	//接到选票重置选票超时时间
	rf.electionTimer.Reset(RandomizeElectionTimeout())
}
```
## 总结
1. 每次请求和响应时，都要先判断，如果 term > currentTerm，要转换角色为 Follower
2. 每个 term 只能 voteFor 其他节点一次
3. candidates 请求投票时间是随机的，注意随机性
4. 得到大多数选票后立即结束等待剩余RPC
5. 成为 Leader 后要尽快进行心跳，否则其他节点又将变成 Candidate