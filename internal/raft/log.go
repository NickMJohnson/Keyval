package raft

import "fmt"

type LogEntry struct {
	Term    uint64
	Index   uint64
	Command []byte
}

type Log struct {
	entries []LogEntry
	// offset is the index of entries[0] — non-zero after snapshot
	offset uint64
}

func NewLog() *Log {
	// sentinel entry at index 0 so real entries start at 1
	return &Log{
		entries: []LogEntry{{Term: 0, Index: 0}},
		offset:  0,
	}
}

func (l *Log) LastIndex() uint64 {
	return l.offset + uint64(len(l.entries)) - 1
}

func (l *Log) LastTerm() uint64 {
	return l.entries[len(l.entries)-1].Term
}

func (l *Log) Entry(index uint64) (LogEntry, error) {
	if index < l.offset || index > l.LastIndex() {
		return LogEntry{}, fmt.Errorf("index %d out of range [%d, %d]", index, l.offset, l.LastIndex())
	}
	return l.entries[index-l.offset], nil
}

func (l *Log) Append(entry LogEntry) {
	l.entries = append(l.entries, entry)
}

// TruncateAfter removes all entries after (and including) index.
func (l *Log) TruncateAfter(index uint64) {
	if index <= l.offset {
		return
	}
	l.entries = l.entries[:index-l.offset]
}

// TermAt returns the term of the entry at index, or 0 if not found.
func (l *Log) TermAt(index uint64) uint64 {
	e, err := l.Entry(index)
	if err != nil {
		return 0
	}
	return e.Term
}

// Slice returns entries in range [start, end).
func (l *Log) Slice(start, end uint64) []LogEntry {
	if start < l.offset {
		start = l.offset
	}
	if end > l.LastIndex()+1 {
		end = l.LastIndex() + 1
	}
	return l.entries[start-l.offset : end-l.offset]
}

// CompactBefore discards all entries before (and including) index, recording
// the snapshot term so the log boundary is consistent.
func (l *Log) CompactBefore(index uint64, term uint64) {
	if index <= l.offset {
		return
	}
	if index > l.LastIndex() {
		index = l.LastIndex()
	}
	remaining := make([]LogEntry, 0, l.LastIndex()-index+1)
	// keep a sentinel at the compaction boundary
	remaining = append(remaining, LogEntry{Term: term, Index: index})
	remaining = append(remaining, l.entries[index-l.offset+1:]...)
	l.entries = remaining
	l.offset = index
}
