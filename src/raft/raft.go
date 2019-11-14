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
	"encoding/gob"
	"labrpc"
	"math/rand"
	"sync"
	"time"
)

const (
	LEADER    = 0
	CANDIDATE = 1
	FOLLOWER  = 2
	CHAN_SIZE = 50
)

// as each Raft peer becomes aware that successive log entries are
// committed, the peer should send an ApplyMsg to the service (or
// tester) on the same server, via the applyCh passed to Make().
//
type ApplyMsg struct {
	Index       int
	Command     interface{}
	UseSnapshot bool   // ignore for lab2; only used in lab3
	Snapshot    []byte // ignore for lab2; only used in lab3
}

type LogEntry struct {
	Index   int
	Term    int
	Command interface{}
}

// A Go object implementing a single Raft peer.

type Raft struct {
	mu        sync.Mutex
	peers     []*labrpc.ClientEnd
	persister *Persister
	me        int // index into peers[]

	// Persistent state on all servers
	currentTerm int
	votedFor    int
	log         []LogEntry

	// Volatile state on all servers
	commitIndex int
	lastApplied int

	// Volatile state on leaders
	nextIndex  []int
	matchIndex []int

	// other
	state       int
	voteCount   int
	commitCh    chan bool
	heartbeatCh chan bool
	grantVoteCh chan bool
	leaderCh    chan bool
	applyCh     chan ApplyMsg
}

// return currentTerm and whether this server
// believes it is the leader.
func (rf *Raft) GetState() (int, bool) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	return rf.currentTerm, rf.state == LEADER
}

//
// save Raft's persistent state to stable storage,
// where it can later be retrieved after a crash and restart.
// see paper's Figure 2 for a description of what should be persistent.
//
func (rf *Raft) persist() {
	w := new(bytes.Buffer)
	e := gob.NewEncoder(w)
	e.Encode(rf.currentTerm)
	e.Encode(rf.votedFor)
	e.Encode(rf.log)
	data := w.Bytes()
	rf.persister.SaveRaftState(data)
}

//
// restore previously persisted state.
//
func (rf *Raft) readPersist(data []byte) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	r := bytes.NewBuffer(data)
	d := gob.NewDecoder(r)
	d.Decode(&rf.currentTerm)
	d.Decode(&rf.votedFor)
	d.Decode(&rf.log)
}

//
// example RequestVote RPC arguments structure.
//
type RequestVoteArgs struct {
	Term         int
	CandidateId  int
	LastLogIndex int
	LastLogTerm  int
}

//
// example RequestVote RPC reply structure.
//
type RequestVoteReply struct {
	Term        int
	VoteGranted bool
}

type AppendEntriesArgs struct {
	Term         int
	LeaderId     int
	PrevLogTerm  int
	PrevLogIndex int
	Entries      []LogEntry
	LeaderCommit int
}

type AppendEntriesReply struct {
	Term      int
	Success   bool
	NextIndex int
}

//
// example RequestVote RPC handler.
//
func (rf *Raft) RequestVote(args RequestVoteArgs, reply *RequestVoteReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	defer rf.persist()

	reply.VoteGranted = false
	// 1. Reply false if term < currentTerm (§5.1)
	if args.Term < rf.currentTerm {
		return
	}
	rf.currentTerm = args.Term
	rf.state = FOLLOWER
	rf.votedFor = -1
	index := rf.log[len(rf.log)-1].Index
	term := rf.log[len(rf.log)-1].Term
	reply.Term = rf.currentTerm
	// 2. If votedFor is null or candidateId, and candidate’s log is at
	// least as up-to-date as receiver’s log, grant vote (§5.2, §5.4)
	// If the logs have last entries with different terms, then
	// the log with the later term is more up-to-date. If the logs
	// end with the same term, then whichever log is longer is
	// more up-to-date.
	if rf.votedFor == -1 || rf.votedFor == args.CandidateId {
		if (args.LastLogTerm > term) || (args.LastLogTerm == term && args.LastLogIndex >= index) {
			reply.VoteGranted = true
			rf.votedFor = args.CandidateId
			rf.grantVoteCh <- true
		}
	}

}

//
// example code to send a RequestVote RPC to a server.
// server is the index of the target server in rf.peers[].
// expects RPC arguments in args.
// fills in *reply with RPC reply, so caller should
// pass &reply.
// the types of the args and reply passed to Call() must be
// the same as the types of the arguments declared in the
// handler function (including whether they are pointers).
//
// returns true if labrpc says the RPC was delivered.
//
// if you're having trouble getting RPC to work, check that you've
// capitalized all field names in structs passed over RPC, and
// that the caller passes the address of the reply struct with &, not
// the struct itself.
//
func (rf *Raft) sendRequestVote(server int, args RequestVoteArgs, reply *RequestVoteReply) bool {
	ok := rf.peers[server].Call("Raft.RequestVote", args, reply)
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if ok {
		if args.Term != rf.currentTerm || rf.state != CANDIDATE {
			return ok
		}
		if reply.Term > rf.currentTerm {
			rf.state = FOLLOWER
			rf.votedFor = -1
			rf.currentTerm = reply.Term
			rf.persist()
		}
		if reply.VoteGranted {
			rf.voteCount++
			if rf.state == CANDIDATE && rf.voteCount > len(rf.peers)/2 {
				rf.state = LEADER
				rf.leaderCh <- true
			}
		}
	}
	return ok
}

func (rf *Raft) AppendEntries(args AppendEntriesArgs, reply *AppendEntriesReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	defer rf.persist()
	lastIndex := rf.log[len(rf.log)-1].Index
	//Reply false if term < currentTerm
	if args.Term < rf.currentTerm {
		reply.Term = rf.currentTerm
		reply.NextIndex = lastIndex + 1
		return
	}
	rf.heartbeatCh <- true
	//If RPC request or response contains term T > currentTerm: set currentTerm = T, convert to follower
	if args.Term > rf.currentTerm {
		rf.state = FOLLOWER
		rf.votedFor = -1
		rf.currentTerm = args.Term

	}
	reply.Term = args.Term

	if args.PrevLogIndex > lastIndex {
		reply.NextIndex = lastIndex + 1
		return
	}

	prevIndex := rf.log[0].Index

	if args.PrevLogIndex > prevIndex {
		term := rf.log[args.PrevLogIndex-prevIndex].Term
		if args.PrevLogTerm != term {
			for i := args.PrevLogIndex - 1; i >= prevIndex; i-- {
				if rf.log[i-prevIndex].Term != term {
					reply.NextIndex = i + 1
					break
				}
			}
			return
		}
	}
	if args.PrevLogIndex >= prevIndex {
		//Append any new entries not already in the log
		rf.log = rf.log[:args.PrevLogIndex+1-prevIndex]
		rf.log = append(rf.log, args.Entries...)
		reply.Success = true
		reply.NextIndex = lastIndex + 1
	}
	//If leaderCommit > commitIndex, set commitIndex =min(leaderCommit, index of last new entry)
	if args.LeaderCommit > rf.commitIndex {
		if args.LeaderCommit > lastIndex {
			rf.commitIndex = lastIndex
		} else {
			rf.commitIndex = args.LeaderCommit
		}
		rf.commitCh <- true
	}
	return
}

func (rf *Raft) sendAppendEntries(server int, args AppendEntriesArgs, reply *AppendEntriesReply) bool {
	ok := rf.peers[server].Call("Raft.AppendEntries", args, reply)
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if ok {
		if rf.state != LEADER || args.Term != rf.currentTerm {
			return ok
		}
		if reply.Term > rf.currentTerm {
			rf.currentTerm = reply.Term
			rf.state = FOLLOWER
			rf.votedFor = -1
			rf.persist()
			return ok
		}
		if reply.Success {
			if len(args.Entries) > 0 {
				rf.nextIndex[server] = args.Entries[len(args.Entries)-1].Index + 1
				rf.matchIndex[server] = rf.nextIndex[server] - 1
			}
		} else {
			rf.nextIndex[server] = reply.NextIndex
		}
	}
	return ok
}

//
// the service using Raft (e.g. a k/v server) wants to start
// agreement on the next command to be appended to Raft's log. if this
// server isn't the leader, returns false. otherwise start the
// agreement and return immediately. there is no guarantee that this
// command will ever be committed to the Raft log, since the leader
// may fail or lose an election.
//
// the first return value is the index that the command will appear at
// if it's ever committed. the second return value is the current
// term. the third return value is true if this server believes it is
// the leader.
//
func (rf *Raft) Start(command interface{}) (int, int, bool) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	index := -1
	term, isLeader := rf.GetState()
	if isLeader {
		index = rf.log[len(rf.log)-1].Index + 1
		// append new entry from client
		rf.log = append(rf.log, LogEntry{Term: term, Command: command, Index: index})
		rf.persist()
	}
	return index, term, isLeader
}

//
// the tester calls Kill() when a Raft instance won't
// be needed again. you are not required to do anything
// in Kill(), but it might be convenient to (for example)
// turn off debug output from this instance.
//
func (rf *Raft) Kill() {
	// Your code here, if desired.
}

func (rf *Raft) leaderAppendEntries() {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	N := rf.commitIndex
	last := rf.log[len(rf.log)-1].Index
	prevIndex := rf.log[0].Index
	// If there exists an N such that N > commitIndex, a majority of matchIndex[i] ≥ N,
	// and log[N].term == currentTerm: set commitIndex = N
	for i := rf.commitIndex + 1; i <= last; i++ {
		num := 1
		for j := range rf.peers {
			if j != rf.me && rf.matchIndex[j] >= i && rf.log[i-prevIndex].Term == rf.currentTerm {
				num++
			}
		}
		if 2*num > len(rf.peers) {
			N = i
		}
	}
	if N != rf.commitIndex {
		rf.commitIndex = N
		rf.commitCh <- true
	}
	for i := range rf.peers {
		if i != rf.me && rf.state == LEADER {
			if rf.nextIndex[i] > prevIndex {
				var args AppendEntriesArgs
				args.Term = rf.currentTerm
				args.LeaderId = rf.me
				args.PrevLogIndex = rf.nextIndex[i] - 1
				args.PrevLogTerm = rf.log[args.PrevLogIndex-prevIndex].Term
				args.Entries = make([]LogEntry, len(rf.log[args.PrevLogIndex+1-prevIndex:]))
				copy(args.Entries, rf.log[args.PrevLogIndex+1-prevIndex:])
				args.LeaderCommit = rf.commitIndex
				go func(i int, args AppendEntriesArgs) {
					var reply AppendEntriesReply
					rf.sendAppendEntries(i, args, &reply)
				}(i, args)
			}
		}
	}
}

func (rf *Raft) candidateRequestVote() {
	var args RequestVoteArgs
	rf.mu.Lock()
	args.Term = rf.currentTerm
	args.CandidateId = rf.me
	args.LastLogTerm = rf.log[len(rf.log)-1].Term
	args.LastLogIndex = rf.log[len(rf.log)-1].Index
	rf.mu.Unlock()

	for i := range rf.peers {
		if i != rf.me && rf.state == CANDIDATE {
			go func(i int) {
				var reply RequestVoteReply
				rf.sendRequestVote(i, args, &reply)
			}(i)
		}
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
	rf := &Raft{}
	rf.peers = peers
	rf.persister = persister
	rf.me = me

	// Your initialization code here.
	rf.votedFor = -1
	rf.state = FOLLOWER
	rf.currentTerm = 0
	rf.log = append(rf.log, LogEntry{Term: 0})
	rf.commitCh = make(chan bool, CHAN_SIZE)
	rf.heartbeatCh = make(chan bool, CHAN_SIZE)
	rf.grantVoteCh = make(chan bool, CHAN_SIZE)
	rf.leaderCh = make(chan bool, CHAN_SIZE)
	rf.applyCh = applyCh

	// initialize from state persisted before a crash
	rf.readPersist(persister.ReadRaftState())

	go func() {
		for {
			switch rf.state {
			case LEADER:
				rf.leaderAppendEntries()
				time.Sleep(50 * time.Millisecond)
			case CANDIDATE:
				func() {
					rf.mu.Lock()
					defer rf.mu.Unlock()
					defer rf.persist()
					rf.currentTerm++
					rf.votedFor = rf.me
					rf.voteCount = 1
				}()

				go rf.candidateRequestVote()
				select {
				case <-rf.heartbeatCh:
					rf.state = FOLLOWER
				case <-rf.leaderCh:
					func() {
						rf.mu.Lock()
						defer rf.mu.Unlock()
						rf.state = LEADER
						rf.nextIndex = make([]int, len(rf.peers))
						rf.matchIndex = make([]int, len(rf.peers))
						for i := range rf.peers {
							rf.nextIndex[i] = rf.log[len(rf.log)-1].Index + 1
							rf.matchIndex[i] = 0
						}
					}()
				case <-time.After(time.Duration(rand.Int()%237+477) * time.Millisecond):
				}
			case FOLLOWER:
				select {
				case <-rf.heartbeatCh:
				case <-rf.grantVoteCh:
				case <-time.After(time.Duration(rand.Int()%317+550) * time.Millisecond):
					rf.state = CANDIDATE
				}
			}
		}
	}()

	go func() {
		for {
			select {
			case <-rf.commitCh:
				func() {
					rf.mu.Lock()
					defer rf.mu.Unlock()
					commitIndex := rf.commitIndex
					prevIndex := rf.log[0].Index
					for i := rf.lastApplied + 1; i <= commitIndex; i++ {
						msg := ApplyMsg{Index: i, Command: rf.log[i-prevIndex].Command}
						applyCh <- msg
						rf.lastApplied = i
					}
				}()

			}
		}
	}()
	return rf
}
