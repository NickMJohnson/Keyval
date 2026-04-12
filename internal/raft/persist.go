package raft

import (
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"
)

// persistState holds the fields that must survive a restart.
type persistState struct {
	CurrentTerm uint64
	VotedFor    string
	Entries     []LogEntry
	Offset      uint64
}

type Storage struct {
	dir string
}

func NewStorage(dir string) (*Storage, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create storage dir: %w", err)
	}
	return &Storage{dir: dir}, nil
}

func (s *Storage) statePath() string {
	return filepath.Join(s.dir, "state.gob")
}

func (s *Storage) snapshotPath() string {
	return filepath.Join(s.dir, "snapshot.bin")
}

// Save writes Raft's persistent state atomically (write to tmp, then rename).
func (s *Storage) Save(term uint64, votedFor string, log *Log) error {
	ps := persistState{
		CurrentTerm: term,
		VotedFor:    votedFor,
		Entries:     log.entries,
		Offset:      log.offset,
	}

	tmp := s.statePath() + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create tmp state file: %w", err)
	}

	if err := gob.NewEncoder(f).Encode(ps); err != nil {
		f.Close()
		return fmt.Errorf("encode state: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("fsync state: %w", err)
	}
	f.Close()

	return os.Rename(tmp, s.statePath())
}

// Load reads persisted state. Returns zero values if no state exists yet.
func (s *Storage) Load() (term uint64, votedFor string, log *Log, err error) {
	log = NewLog()

	f, err := os.Open(s.statePath())
	if os.IsNotExist(err) {
		return 0, "", log, nil
	}
	if err != nil {
		return 0, "", log, fmt.Errorf("open state file: %w", err)
	}
	defer f.Close()

	var ps persistState
	if err := gob.NewDecoder(f).Decode(&ps); err != nil {
		return 0, "", log, fmt.Errorf("decode state: %w", err)
	}

	log.entries = ps.Entries
	log.offset = ps.Offset
	return ps.CurrentTerm, ps.VotedFor, log, nil
}

// SaveSnapshot writes snapshot bytes to disk atomically.
func (s *Storage) SaveSnapshot(data []byte) error {
	tmp := s.snapshotPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}
	return os.Rename(tmp, s.snapshotPath())
}

// LoadSnapshot reads the latest snapshot, returning nil if none exists.
func (s *Storage) LoadSnapshot() ([]byte, error) {
	data, err := os.ReadFile(s.snapshotPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	return data, err
}
