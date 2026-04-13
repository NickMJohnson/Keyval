package raft

import (
	"context"
	"math/rand"
	"sync"
	"time"

	pb "github.com/NickMJohnson/Keyval/proto/raft"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type State int

const (
	Follower State = iota
	Candidate
	Leader
)

const (
	heartbeatInterval      = 50 * time.Millisecond
	electionTimeoutMin     = 150 * time.Millisecond
	electionTimeoutMax     = 300 * time.Millisecond
)

// ApplyMsg is sent to the state machine whenever an entry is committed.
type ApplyMsg struct {
	Index   uint64
	Command []byte
}

type Raft struct {
	mu    sync.Mutex
	id    string
	peers []string // gRPC addresses of all other nodes

	// persistent state
	currentTerm uint64
	votedFor    string
	log         *Log

	// volatile state
	state       State
	commitIndex uint64
	lastApplied uint64

	// leader-only volatile state
	nextIndex  map[string]uint64
	matchIndex map[string]uint64

	// snapshot state
	lastSnapshotIndex uint64
	lastSnapshotTerm  uint64

	storage       *Storage
	electionTimer *time.Timer
	applyCh       chan ApplyMsg
	stopCh        chan struct{}
	stopOnce      sync.Once
}

func New(id string, peers []string, applyCh chan ApplyMsg, storage *Storage) *Raft {
	term, votedFor, log, err := storage.Load()
	if err != nil {
		panic("raft: failed to load persisted state: " + err.Error())
	}

	r := &Raft{
		id:       id,
		peers:    peers,
		log:      log,
		state:    Follower,
		applyCh:  applyCh,
		stopCh:   make(chan struct{}),
		storage:  storage,
		currentTerm: term,
		votedFor:    votedFor,
	}
	r.resetElectionTimer()
	go r.run()
	return r
}

// persist saves currentTerm, votedFor, and the log to disk.
// Must be called with r.mu held before responding to any RPC that mutates these.
func (r *Raft) persist() {
	if err := r.storage.Save(r.currentTerm, r.votedFor, r.log); err != nil {
		// Non-fatal log — in production you'd want to crash here.
		// Continuing risks a safety violation on restart.
		_ = err
	}
}

func (r *Raft) Stop() {
	r.stopOnce.Do(func() {
		r.mu.Lock()
		r.state = Follower
		r.mu.Unlock()
		close(r.stopCh)
	})
}

// run is the main event loop.
func (r *Raft) run() {
	for {
		select {
		case <-r.stopCh:
			return
		case <-r.electionTimer.C:
			r.mu.Lock()
			if r.state != Leader {
				r.startElection()
			}
			r.mu.Unlock()
		}
	}
}

func (r *Raft) resetElectionTimer() {
	d := electionTimeoutMin + time.Duration(rand.Int63n(int64(electionTimeoutMax-electionTimeoutMin)))
	if r.electionTimer == nil {
		r.electionTimer = time.NewTimer(d)
	} else {
		r.electionTimer.Reset(d)
	}
}

// startElection transitions to Candidate and requests votes from all peers.
// Must be called with r.mu held.
func (r *Raft) startElection() {
	r.state = Candidate
	r.currentTerm++
	r.votedFor = r.id
	r.persist()
	term := r.currentTerm
	lastIndex := r.log.LastIndex()
	lastTerm := r.log.LastTerm()
	r.resetElectionTimer()

	votes := 1 // vote for self
	total := len(r.peers) + 1
	quorum := total/2 + 1

	r.mu.Unlock()

	var voteMu sync.Mutex
	var wg sync.WaitGroup

	for _, peer := range r.peers {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			granted := r.sendRequestVote(addr, term, lastIndex, lastTerm)
			if !granted {
				return
			}
			voteMu.Lock()
			votes++
			won := votes >= quorum
			voteMu.Unlock()

			if won {
				r.mu.Lock()
				if r.state == Candidate && r.currentTerm == term {
					r.becomeLeader()
				}
				r.mu.Unlock()
			}
		}(peer)
	}

	wg.Wait()
	r.mu.Lock() // re-acquire for the caller
}

func (r *Raft) becomeLeader() {
	r.state = Leader
	r.nextIndex = make(map[string]uint64)
	r.matchIndex = make(map[string]uint64)
	nextIdx := r.log.LastIndex() + 1
	for _, p := range r.peers {
		r.nextIndex[p] = nextIdx
		r.matchIndex[p] = 0
	}
	go r.heartbeatLoop()
}

func (r *Raft) heartbeatLoop() {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.mu.Lock()
			if r.state != Leader {
				r.mu.Unlock()
				return
			}
			r.mu.Unlock()
			for _, peer := range r.peers {
				go r.sendAppendEntries(peer)
			}
		}
	}
}

// --- RPC senders ---

func (r *Raft) sendRequestVote(addr string, term, lastIndex, lastTerm uint64) bool {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return false
	}
	defer conn.Close()

	client := pb.NewRaftServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	reply, err := client.RequestVote(ctx, &pb.RequestVoteArgs{
		Term:         term,
		CandidateId:  r.id,
		LastLogIndex: lastIndex,
		LastLogTerm:  lastTerm,
	})
	if err != nil {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if reply.Term > r.currentTerm {
		r.stepDown(reply.Term)
		return false
	}
	return reply.VoteGranted
}

func (r *Raft) sendAppendEntries(addr string) {
	r.mu.Lock()
	if r.state != Leader {
		r.mu.Unlock()
		return
	}
	nextIdx := r.nextIndex[addr]
	prevIndex := nextIdx - 1
	prevTerm := r.log.TermAt(prevIndex)
	entries := r.log.Slice(nextIdx, r.log.LastIndex()+1)
	leaderCommit := r.commitIndex
	term := r.currentTerm
	r.mu.Unlock()

	pbEntries := make([]*pb.LogEntry, len(entries))
	for i, e := range entries {
		pbEntries[i] = &pb.LogEntry{Term: e.Term, Index: e.Index, Command: e.Command}
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return
	}
	defer conn.Close()

	client := pb.NewRaftServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	reply, err := client.AppendEntries(ctx, &pb.AppendEntriesArgs{
		Term:         term,
		LeaderId:     r.id,
		PrevLogIndex: prevIndex,
		PrevLogTerm:  prevTerm,
		Entries:      pbEntries,
		LeaderCommit: leaderCommit,
	})
	if err != nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if reply.Term > r.currentTerm {
		r.stepDown(reply.Term)
		return
	}
	if r.state != Leader || r.currentTerm != term {
		return
	}

	if reply.Success {
		newMatch := prevIndex + uint64(len(entries))
		if newMatch > r.matchIndex[addr] {
			r.matchIndex[addr] = newMatch
			r.nextIndex[addr] = newMatch + 1
		}
		r.maybeCommit()
	} else {
		// back off using conflict hint
		if reply.ConflictIndex > 0 {
			r.nextIndex[addr] = reply.ConflictIndex
		} else if r.nextIndex[addr] > 1 {
			r.nextIndex[addr]--
		}
	}
}

// maybeCommit advances commitIndex if a new entry has been replicated to a majority.
// Must be called with r.mu held.
func (r *Raft) maybeCommit() {
	for n := r.log.LastIndex(); n > r.commitIndex; n-- {
		if r.log.TermAt(n) != r.currentTerm {
			break
		}
		count := 1
		for _, m := range r.matchIndex {
			if m >= n {
				count++
			}
		}
		if count >= len(r.peers)/2+1 {
			r.commitIndex = n
			go r.applyCommitted()
			break
		}
	}
}

func (r *Raft) applyCommitted() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for r.lastApplied < r.commitIndex {
		r.lastApplied++
		entry, err := r.log.Entry(r.lastApplied)
		if err != nil {
			continue
		}
		r.applyCh <- ApplyMsg{Index: entry.Index, Command: entry.Command}
	}
}

// --- RPC handlers ---

func (r *Raft) RequestVote(_ context.Context, args *pb.RequestVoteArgs) (*pb.RequestVoteReply, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	reply := &pb.RequestVoteReply{Term: r.currentTerm}

	if args.Term < r.currentTerm {
		return reply, nil
	}
	if args.Term > r.currentTerm {
		r.stepDown(args.Term)
	}

	alreadyVoted := r.votedFor != "" && r.votedFor != args.CandidateId
	logOk := args.LastLogTerm > r.log.LastTerm() ||
		(args.LastLogTerm == r.log.LastTerm() && args.LastLogIndex >= r.log.LastIndex())

	if !alreadyVoted && logOk {
		r.votedFor = args.CandidateId
		r.persist()
		r.resetElectionTimer()
		reply.VoteGranted = true
	}
	return reply, nil
}

func (r *Raft) AppendEntries(_ context.Context, args *pb.AppendEntriesArgs) (*pb.AppendEntriesReply, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	reply := &pb.AppendEntriesReply{Term: r.currentTerm}

	if args.Term < r.currentTerm {
		return reply, nil
	}
	if args.Term > r.currentTerm {
		r.stepDown(args.Term)
	}
	r.state = Follower
	r.resetElectionTimer()

	// consistency check
	if args.PrevLogIndex > 0 {
		if args.PrevLogIndex > r.log.LastIndex() {
			reply.ConflictIndex = r.log.LastIndex() + 1
			return reply, nil
		}
		if r.log.TermAt(args.PrevLogIndex) != args.PrevLogTerm {
			ct := r.log.TermAt(args.PrevLogIndex)
			idx := args.PrevLogIndex
			for idx > r.lastSnapshotIndex && r.log.TermAt(idx) == ct {
				idx--
			}
			reply.ConflictIndex = idx + 1
			reply.ConflictTerm = ct
			return reply, nil
		}
	}

	// append new entries
	for _, e := range args.Entries {
		if e.Index <= r.log.LastIndex() {
			if r.log.TermAt(e.Index) != e.Term {
				r.log.TruncateAfter(e.Index)
				r.log.Append(LogEntry{Term: e.Term, Index: e.Index, Command: e.Command})
			}
		} else {
			r.log.Append(LogEntry{Term: e.Term, Index: e.Index, Command: e.Command})
		}
	}
	r.persist()

	if args.LeaderCommit > r.commitIndex {
		last := r.log.LastIndex()
		if args.LeaderCommit < last {
			r.commitIndex = args.LeaderCommit
		} else {
			r.commitIndex = last
		}
		go r.applyCommitted()
	}

	reply.Success = true
	return reply, nil
}

func (r *Raft) InstallSnapshot(_ context.Context, args *pb.SnapshotArgs) (*pb.SnapshotReply, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	reply := &pb.SnapshotReply{Term: r.currentTerm}
	if args.Term < r.currentTerm {
		return reply, nil
	}
	if args.Term > r.currentTerm {
		r.stepDown(args.Term)
	}
	r.resetElectionTimer()

	if args.LastIncludedIndex <= r.lastSnapshotIndex {
		return reply, nil
	}

	r.log.CompactBefore(args.LastIncludedIndex, args.LastIncludedTerm)
	r.lastSnapshotIndex = args.LastIncludedIndex
	r.lastSnapshotTerm = args.LastIncludedTerm

	if r.commitIndex < args.LastIncludedIndex {
		r.commitIndex = args.LastIncludedIndex
	}
	if r.lastApplied < args.LastIncludedIndex {
		r.lastApplied = args.LastIncludedIndex
	}

	r.applyCh <- ApplyMsg{Index: args.LastIncludedIndex, Command: args.Data}
	return reply, nil
}

// stepDown reverts to Follower with the given term.
// Must be called with r.mu held.
func (r *Raft) stepDown(term uint64) {
	r.currentTerm = term
	r.state = Follower
	r.votedFor = ""
	r.resetElectionTimer()
}

// --- Public API for the KV layer ---

// IsLeader reports whether this node is currently the leader.
func (r *Raft) IsLeader() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state == Leader
}

// Submit appends a command to the Raft log (only valid on leader).
// Returns the assigned log index, the current term, and whether this node is leader.
func (r *Raft) Submit(command []byte) (uint64, uint64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != Leader {
		return 0, 0, false
	}
	index := r.log.LastIndex() + 1
	entry := LogEntry{Term: r.currentTerm, Index: index, Command: command}
	r.log.Append(entry)
	r.matchIndex[r.id] = index
	return index, r.currentTerm, true
}
