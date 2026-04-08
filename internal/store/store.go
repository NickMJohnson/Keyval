package store

import (
	"encoding/json"
	"sync"

	"github.com/yourusername/keyval/internal/raft"
)

type OpType string

const (
	OpPut    OpType = "put"
	OpDelete OpType = "delete"
)

type Command struct {
	Op       OpType `json:"op"`
	Key      string `json:"key"`
	Value    string `json:"value,omitempty"`
	ClientID string `json:"client_id"`
	ReqID    uint64 `json:"req_id"`
}

type Store struct {
	mu   sync.RWMutex
	data map[string]string

	// dedup table: last applied request per client
	lastReq map[string]uint64

	raft *raft.Raft
}

func New(r *raft.Raft, applyCh chan raft.ApplyMsg) *Store {
	s := &Store{
		data:    make(map[string]string),
		lastReq: make(map[string]uint64),
		raft:    r,
	}
	go s.apply(applyCh)
	return s
}

// apply reads committed entries from Raft and executes them.
func (s *Store) apply(applyCh chan raft.ApplyMsg) {
	for msg := range applyCh {
		var cmd Command
		if err := json.Unmarshal(msg.Command, &cmd); err != nil {
			continue
		}

		s.mu.Lock()
		// skip duplicate requests
		if last, ok := s.lastReq[cmd.ClientID]; ok && last >= cmd.ReqID {
			s.mu.Unlock()
			continue
		}
		s.lastReq[cmd.ClientID] = cmd.ReqID

		switch cmd.Op {
		case OpPut:
			s.data[cmd.Key] = cmd.Value
		case OpDelete:
			delete(s.data, cmd.Key)
		}
		s.mu.Unlock()
	}
}

// Get returns the value for key. Served locally (follower reads).
func (s *Store) Get(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[key]
	return v, ok
}

// Put proposes a put command to Raft. Returns false if not leader.
func (s *Store) Put(key, value, clientID string, reqID uint64) (bool, string) {
	return s.propose(Command{Op: OpPut, Key: key, Value: value, ClientID: clientID, ReqID: reqID})
}

// Delete proposes a delete command to Raft. Returns false if not leader.
func (s *Store) Delete(key, clientID string, reqID uint64) (bool, string) {
	return s.propose(Command{Op: OpDelete, Key: key, ClientID: clientID, ReqID: reqID})
}

func (s *Store) propose(cmd Command) (bool, string) {
	b, err := json.Marshal(cmd)
	if err != nil {
		return false, ""
	}
	_, _, isLeader := s.raft.Submit(b)
	return isLeader, ""
}
