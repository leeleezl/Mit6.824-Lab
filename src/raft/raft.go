package raft

//
// this is an outline of the API that raft must expose to
// the service (or tester). see comments below for
// each of these functions for more details.
//
// rf = Make(...)
//   create a new Raft server.
// rf.Start(command interface{}) (index, term, isleader)
//   start agreement on a new log entry
// rf.GetState() (term, isLeader)
//   ask a Raft for its current term, and whether it thinks it is leader
// ApplyMsg
//   each time a new entry is committed to the log, each Raft peer
//   should send an ApplyMsg to the service (or tester)
//   in the same server.
//

import (
	"bytes"
	"sync"
	"sync/atomic"
	"time"

	"6.824/labgob"
	"6.824/labrpc"
)

//
// A Go object implementing a single Raft peer.
//
type Raft struct {
	mu        sync.RWMutex        // Lock to protect shared access to this peer's state
	peers     []*labrpc.ClientEnd // RPC end points of all peers
	persister *Persister          // Object to hold this peer's persisted state
	me        int                 // this peer's index into peers[]
	dead      int32               // set by Kill()

	applyCh        chan ApplyMsg
	applyCond      *sync.Cond   // 用于在提交新条目后唤醒应用协程
	replicatorCond []*sync.Cond // 用于向复制程序协程发出信号，以批处理复制项
	state          NodeState

	currentTerm int
	votedFor    int
	logs        []Entry

	commitIndex int
	lastApplied int
	nextIndex   []int
	matchIndex  []int //跟踪其他节点在日志中与自己达成一致的最高索引 通过比较自己的matchIndex和其他节点的matchIndex，领导者可以确定哪些日志条目已经在多数节点上复制成功

	electionTimer  *time.Timer
	heartbeatTimer *time.Timer
}

// return currentTerm and whether this server
// believes it is the leader.
func (rf *Raft) GetState() (int, bool) {
	rf.mu.RLock()
	defer rf.mu.RUnlock()
	return rf.currentTerm, rf.state == StateLeader
}

func (rf *Raft) GetRaftStateSize() int {
	rf.mu.RLock()
	defer rf.mu.RUnlock()
	return rf.persister.RaftStateSize()
}

//
// save Raft's persistent state to stable storage,
// where it can later be retrieved after a crash and restart.
// see paper's Figure 2 for a description of what should be persistent.
//
func (rf *Raft) persist() {
	rf.persister.SaveRaftState(rf.encodeState())
}

//
// restore previously persisted state.
//
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
		DPrintf("{Node %v} restores persisted state failed", rf.me)
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

//
// A service wants to switch to snapshot.  Only do so if Raft hasn't
// have more recent info since it communicate the snapshot on applyCh.
//
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

// the service says it has created a snapshot that has
// all info up to and including index. this means the
// service no longer needs the log through (and including)
// that index. Raft should now trim its log as much as possible.

//做快照
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

func (rf *Raft) RequestVote(request *RequestVoteRequest, response *RequestVoteResponse) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	defer rf.persist()
	defer DPrintf("{Node %v}'s state is {state %v,term %v,commitIndex %v,lastApplied %v,firstLog %v,lastLog %v} before processing requestVoteRequest %v and reply requestVoteResponse %v", rf.me, rf.state, rf.currentTerm, rf.commitIndex, rf.lastApplied, rf.getFirstLog(), rf.getLastLog(), request, response)

	if request.Term < rf.currentTerm || (request.Term == rf.currentTerm && rf.votedFor != -1 && rf.votedFor != request.CandidateId) {
		response.Term, response.VoteGranted = rf.currentTerm, false
		return
	}
	if request.Term > rf.currentTerm {
		rf.ChangeState(StateFollower)
		rf.currentTerm, rf.votedFor = request.Term, -1
	}
	if !rf.isLogUpToDate(request.LastLogTerm, request.LastLogIndex) {
		response.Term, response.VoteGranted = rf.currentTerm, false
		return
	}
	rf.votedFor = request.CandidateId
	rf.electionTimer.Reset(RandomizedElectionTimeout())
	response.Term, response.VoteGranted = rf.currentTerm, true
}

func (rf *Raft) AppendEntries(request *AppendEntriesRequest, response *AppendEntriesResponse) {

	//2A
	//follower 会接到这个消息并处理
	rf.mu.Lock()
	defer rf.mu.Unlock()
	defer rf.persist()
	// leader的term比自己小
	if request.Term < rf.currentTerm {
		response.Term, response.Success = rf.currentTerm, false
		return
	}
	//发现更大的任期（刚刚变为leader的candidate发送的心跳）
	if request.Term > rf.currentTerm {
		rf.currentTerm, rf.votedFor = request.Term, -1
	}
	// 刚刚变为leader的candidate发送的心跳，此时的节点可能是另一个candidate状态
	rf.ChangeState(StateFollower)
	rf.electionTimer.Reset(RandomizedElectionTimeout())

	//2B
	if request.PrevLogIndex < rf.getFirstLog().Index {
		response.Term, response.Success = 0, false
		DPrintf("{Node %v} receives unexpected AppendEntriesRequest %v from {Node %v} because prevLogIndex %v < firstLogIndex %v", rf.me, request, request.LeaderId, request.PrevLogIndex, rf.getFirstLog().Index)
		return
	}

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
}

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

//
// example code to send a RPC to a server.
// server is the index of the target server in rf.peers[].
// expects RPC arguments in args.
// fills in *reply with RPC reply, so caller should
// pass &reply.
// the types of the args and reply passed to Call() must be
// the same as the types of the arguments declared in the
// handler function (including whether they are pointers).
//
// The labrpc package simulates a lossy network, in which servers
// may be unreachable, and in which requests and replies may be lost.
// Call() sends a request and waits for a reply. If a reply arrives
// within a timeout interval, Call() returns true; otherwise
// Call() returns false. Thus Call() may not return for a while.
// A false return can be caused by a dead server, a live server that
// can't be reached, a lost request, or a lost reply.
//
// Call() is guaranteed to return (perhaps after a delay) *except* if the
// handler function on the server side does not return.  Thus there
// is no need to implement your own timeouts around Call().
//
// look at the comments in ../labrpc/labrpc.go for more details.
//
// if you're having trouble getting RPC to work, check that you've
// capitalized all field names in structs passed over RPC, and
// that the caller passes the address of the reply struct with &, not
// the struct itself.
//
func (rf *Raft) sendRequestVote(server int, request *RequestVoteRequest, response *RequestVoteResponse) bool {
	return rf.peers[server].Call("Raft.RequestVote", request, response)
}

func (rf *Raft) sendAppendEntries(server int, request *AppendEntriesRequest, response *AppendEntriesResponse) bool {
	return rf.peers[server].Call("Raft.AppendEntries", request, response)
}

func (rf *Raft) sendInstallSnapshot(server int, request *InstallSnapshotRequest, response *InstallSnapshotResponse) bool {
	return rf.peers[server].Call("Raft.InstallSnapshot", request, response)
}

func (rf *Raft) StartElection() {
	request := rf.genRequestVoteRequest()
	DPrintf("{Node %v} starts election with RequestVoteRequest %v", rf.me, request)
	// use Closure
	grantedVotes := 1
	rf.votedFor = rf.me
	rf.persist()
	for peer := range rf.peers {
		if peer == rf.me {
			continue
		}
		go func(peer int) {
			response := new(RequestVoteResponse)
			if rf.sendRequestVote(peer, request, response) {
				rf.mu.Lock()
				defer rf.mu.Unlock()
				if rf.currentTerm == request.Term && rf.state == StateCandidate {
					if response.VoteGranted {
						grantedVotes += 1
						if grantedVotes > len(rf.peers)/2 {
							DPrintf("{Node %v} receives majority votes in term %v", rf.me, rf.currentTerm)
							rf.ChangeState(StateLeader)
							rf.BroadcastHeartbeat(true)
						}
					} else if response.Term > rf.currentTerm {
						DPrintf("{Node %v} finds a new leader {Node %v} with term %v and steps down in term %v", rf.me, peer, response.Term, rf.currentTerm)
						rf.ChangeState(StateFollower)
						rf.currentTerm, rf.votedFor = response.Term, -1
						rf.persist()
					}
				}
			}
		}(peer)
	}
}

func (rf *Raft) BroadcastHeartbeat(isHeartBeat bool) {
	for peer := range rf.peers {
		if peer == rf.me {
			continue
		}
		if isHeartBeat {
			// need sending at once to maintain leadership
			go rf.replicateOneRound(peer)
		} else {
			// just signal replicator goroutine to send entries in batch
			rf.replicatorCond[peer].Signal()
		}
	}
}

func (rf *Raft) replicateOneRound(peer int) {
	rf.mu.RLock()
	if rf.state != StateLeader {
		rf.mu.RUnlock()
		return
	}
	prevLogIndex := rf.nextIndex[peer] - 1
	//此时leader已经丢弃了快照之前的log，所以leader的firstLogIndex要大于peer的prelogIndex
	if prevLogIndex < rf.getFirstLog().Index {
		//快照
		request := rf.genInstallSnapshotRequest()
		rf.mu.RUnlock()
		response := new(InstallSnapshotResponse)
		if rf.sendInstallSnapshot(peer, request, response) {
			rf.mu.Lock()
			rf.handleInstallSnapshotResponse(peer, request, response)
			rf.mu.Unlock()
		}
	} else {
		request := rf.genAppendEntriesRequest(prevLogIndex)
		rf.mu.RUnlock()
		response := new(AppendEntriesResponse)
		if rf.sendAppendEntries(peer, request, response) {
			rf.mu.Lock()
			rf.handleAppendEntriesResponse(peer, request, response)
			rf.mu.Unlock()
		}
	}
}

func (rf *Raft) genRequestVoteRequest() *RequestVoteRequest {
	lastLog := rf.getLastLog()
	return &RequestVoteRequest{
		Term:         rf.currentTerm,
		CandidateId:  rf.me,
		LastLogIndex: lastLog.Index,
		LastLogTerm:  lastLog.Term,
	}
}

func (rf *Raft) genAppendEntriesRequest(prevLogIndex int) *AppendEntriesRequest {
	firstIndex := rf.getFirstLog().Index
	entries := make([]Entry, len(rf.logs[prevLogIndex+1-firstIndex:]))
	copy(entries, rf.logs[prevLogIndex+1-firstIndex:])
	return &AppendEntriesRequest{
		Term:         rf.currentTerm,
		LeaderId:     rf.me,
		PrevLogIndex: prevLogIndex,
		PrevLogTerm:  rf.logs[prevLogIndex-firstIndex].Term,
		Entries:      entries,
		LeaderCommit: rf.commitIndex,
	}
}

func (rf *Raft) genInstallSnapshotRequest() *InstallSnapshotRequest {
	firstLog := rf.getFirstLog()
	return &InstallSnapshotRequest{
		Term:              rf.currentTerm,
		LeaderId:          rf.me,
		LastIncludedIndex: firstLog.Index,
		LastIncludedTerm:  firstLog.Term,
		Data:              rf.persister.ReadSnapshot(),
	}
}

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

func (rf *Raft) handleInstallSnapshotResponse(peer int, request *InstallSnapshotRequest, response *InstallSnapshotResponse) {
	if rf.state == StateLeader && rf.currentTerm == request.Term {
		if response.Term > rf.currentTerm {
			rf.ChangeState(StateFollower)
			rf.electionTimer.Reset(RandomizedElectionTimeout())
		} else {
			rf.matchIndex[peer], rf.nextIndex[peer] = request.LastIncludedIndex, request.LastIncludedIndex+1
		}
	}
}

func (rf *Raft) ChangeState(state NodeState) {
	if rf.state == state {
		return
	}
	DPrintf("{Node %d} changes state from %s to %s in term %d", rf.me, rf.state, state, rf.currentTerm)
	rf.state = state
	switch state {
	case StateFollower:
		rf.heartbeatTimer.Stop()
		rf.electionTimer.Reset(RandomizedElectionTimeout())
	case StateCandidate:
	case StateLeader:
		lastLog := rf.getLastLog()
		for i := 0; i < len(rf.peers); i++ {
			rf.matchIndex[i], rf.nextIndex[i] = 0, lastLog.Index+1
		}
		rf.electionTimer.Stop()
		rf.heartbeatTimer.Reset(StableHeartbeatTimeout())
	}
}

// used to compute and advance commitIndex by matchIndex[]
func (rf *Raft) advanceCommitIndexForLeader() {
	n := len(rf.matchIndex)
	srt := make([]int, n)
	copy(srt, rf.matchIndex)
	insertionSort(srt)
	//超过半数
	newCommitIndex := srt[n-(n/2+1)]
	if newCommitIndex > rf.commitIndex {
		// only advance commitIndex for current term's log
		if rf.matchLog(rf.currentTerm, newCommitIndex) {
			DPrintf("{Node %d} advance commitIndex from %d to %d with matchIndex %v in term %d", rf.me, rf.commitIndex, newCommitIndex, rf.matchIndex, rf.currentTerm)
			rf.commitIndex = newCommitIndex
			rf.applyCond.Signal()
		} else {
			DPrintf("{Node %d} can not advance commitIndex from %d because the term of newCommitIndex %d is not equal to currentTerm %d", rf.me, rf.commitIndex, newCommitIndex, rf.currentTerm)
		}
	}
}

// used to advance commitIndex by leaderCommit
func (rf *Raft) advanceCommitIndexForFollower(leaderCommit int) {
	newCommitIndex := Min(leaderCommit, rf.getLastLog().Index)
	if newCommitIndex > rf.commitIndex {
		DPrintf("{Node %d} advance commitIndex from %d to %d with leaderCommit %d in term %d", rf.me, rf.commitIndex, newCommitIndex, leaderCommit, rf.currentTerm)
		rf.commitIndex = newCommitIndex
		rf.applyCond.Signal()
	}
}

func (rf *Raft) getLastLog() Entry {
	return rf.logs[len(rf.logs)-1]
}

func (rf *Raft) getFirstLog() Entry {
	return rf.logs[0]
}

// used by RequestVote Handler to judge which log is newer
func (rf *Raft) isLogUpToDate(term, index int) bool {
	lastLog := rf.getLastLog()
	return term > lastLog.Term || (term == lastLog.Term && index >= lastLog.Index)
}

// used by AppendEntries Handler to judge whether log is matched
func (rf *Raft) matchLog(term, index int) bool {
	return index <= rf.getLastLog().Index && rf.logs[index-rf.getFirstLog().Index].Term == term
}

// used by Start function to append a new Entry to logs
func (rf *Raft) appendNewEntry(command interface{}) Entry {
	lastLog := rf.getLastLog()
	newLog := Entry{lastLog.Index + 1, rf.currentTerm, command}
	rf.logs = append(rf.logs, newLog)
	rf.matchIndex[rf.me], rf.nextIndex[rf.me] = newLog.Index, newLog.Index+1
	rf.persist()
	return newLog
}

// used by replicator goroutine to judge whether a peer needs replicating
func (rf *Raft) needReplicating(peer int) bool {
	rf.mu.RLock()
	defer rf.mu.RUnlock()
	return rf.state == StateLeader && rf.matchIndex[peer] < rf.getLastLog().Index
}

// used by upper layer to detect whether there are any logs in current term
// func (rf *Raft) HasLogInCurrentTerm() bool {
// 	rf.mu.RLock()
// 	defer rf.mu.RUnlock()
// 	return rf.getLastLog().Term == rf.currentTerm
// }

//
// the service using Raft (e.g. a k/v server) wants to start
// agreement on the next command to be appended to Raft's log. if this
// server isn't the leader, returns false. otherwise start the
// agreement and return immediately. there is no guarantee that this
// command will ever be committed to the Raft log, since the leader
// may fail or lose an election. even if the Raft instance has been killed,
// this function should return gracefully.
//
// the first return value is the index that the command will appear at
// if it's ever committed. the second return value is the current
// term. the third return value is true if this server believes it is
// the leader.
//
func (rf *Raft) Start(command interface{}) (int, int, bool) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if rf.state != StateLeader {
		return -1, -1, false
	}
	newLog := rf.appendNewEntry(command)
	DPrintf("{Node %v} receives a new command[%v] to replicate in term %v", rf.me, newLog, rf.currentTerm)
	rf.BroadcastHeartbeat(false)
	return newLog.Index, newLog.Term, true
}

//
// the tester doesn't halt goroutines created by Raft after each test,
// but it does call the Kill() method. your code can use killed() to
// check whether Kill() has been called. the use of atomic avoids the
// need for a lock.
//
// the issue is that long-running goroutines use memory and may chew
// up CPU time, perhaps causing later tests to fail and generating
// confusing debug output. any goroutine with a long-running loop
// should call killed() to check whether it should stop.
//
func (rf *Raft) Kill() {
	atomic.StoreInt32(&rf.dead, 1)
}

func (rf *Raft) killed() bool {
	return atomic.LoadInt32(&rf.dead) == 1
}

func (rf *Raft) Me() int {
	return rf.me
}

func (rf *Raft) ticker() {
	for rf.killed() == false {
		select {
		case <-rf.electionTimer.C:
			rf.mu.Lock()
			rf.ChangeState(StateCandidate)
			rf.currentTerm += 1
			rf.StartElection()
			rf.electionTimer.Reset(RandomizedElectionTimeout())
			rf.mu.Unlock()
		case <-rf.heartbeatTimer.C:
			rf.mu.Lock()
			if rf.state == StateLeader {
				rf.BroadcastHeartbeat(true)
				rf.heartbeatTimer.Reset(StableHeartbeatTimeout())
			}
			rf.mu.Unlock()
		}
	}
}

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
			// 通知上层 KVserver
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

//
// the service or tester wants to create a Raft server. the ports
// of all the Raft servers (including this one) are in peers[]. this
// server's port is peers[me]. all the servers' peers[] arrays
// have the same order. persister is a place for this server to
// save its persistent state, and also initially holds the most
// recent saved state, if any. applyCh is a channel on which the
// tester or service expects Raft to send ApplyMsg messages.
// Make() must return quickly, so it should start goroutines
// for any long-running work.
//
func Make(peers []*labrpc.ClientEnd, me int,
	persister *Persister, applyCh chan ApplyMsg) *Raft {
	rf := &Raft{
		peers:          peers,
		persister:      persister,
		me:             me,
		dead:           0,
		applyCh:        applyCh,
		replicatorCond: make([]*sync.Cond, len(peers)),
		state:          StateFollower,
		currentTerm:    0,
		votedFor:       -1,
		logs:           make([]Entry, 1),
		nextIndex:      make([]int, len(peers)),
		matchIndex:     make([]int, len(peers)),
		heartbeatTimer: time.NewTimer(StableHeartbeatTimeout()),
		electionTimer:  time.NewTimer(RandomizedElectionTimeout()),
	}

	rf.readPersist(persister.ReadRaftState())
	rf.applyCond = sync.NewCond(&rf.mu)
	lastLog := rf.getLastLog()
	for i := 0; i < len(peers); i++ {
		rf.matchIndex[i], rf.nextIndex[i] = 0, lastLog.Index+1
		if i != rf.me {
			rf.replicatorCond[i] = sync.NewCond(&sync.Mutex{})
			// start replicator goroutine to replicate entries in batch
			go rf.replicator(i)
		}
	}
	go rf.ticker()
	go rf.applier()

	return rf
}
