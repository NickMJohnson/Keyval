package server

import (
	"context"
	"fmt"
	"net"

	kvpb "github.com/NickMJohnson/Keyval/proto/kv"
	raftpb "github.com/NickMJohnson/Keyval/proto/raft"

	"github.com/NickMJohnson/Keyval/internal/raft"
	"github.com/NickMJohnson/Keyval/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	kvpb.UnimplementedKVServiceServer
	raftpb.UnimplementedRaftServiceServer

	store      *store.Store
	raft       *raft.Raft
	leaderAddr string // hint returned to clients when this node isn't leader
	grpcServer *grpc.Server
}

func New(s *store.Store, r *raft.Raft, leaderAddr string) *Server {
	return &Server{store: s, raft: r, leaderAddr: leaderAddr}
}

func (s *Server) Start(addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	s.grpcServer = grpc.NewServer()
	kvpb.RegisterKVServiceServer(s.grpcServer, s)
	raftpb.RegisterRaftServiceServer(s.grpcServer, s)
	return s.grpcServer.Serve(lis)
}

func (s *Server) Stop() {
	if s.grpcServer != nil {
		s.grpcServer.GracefulStop()
	}
}

// --- KVService ---

func (s *Server) Get(_ context.Context, req *kvpb.GetRequest) (*kvpb.GetResponse, error) {
	val, found := s.store.Get(req.Key)
	return &kvpb.GetResponse{Value: val, Found: found}, nil
}

func (s *Server) Put(_ context.Context, req *kvpb.PutRequest) (*kvpb.PutResponse, error) {
	ok, _ := s.store.Put(req.Key, req.Value, req.ClientId, req.RequestId)
	if !ok {
		return &kvpb.PutResponse{Success: false, LeaderAddr: s.leaderAddr}, nil
	}
	return &kvpb.PutResponse{Success: true}, nil
}

func (s *Server) Delete(_ context.Context, req *kvpb.DeleteRequest) (*kvpb.DeleteResponse, error) {
	ok, _ := s.store.Delete(req.Key, req.ClientId, req.RequestId)
	if !ok {
		return &kvpb.DeleteResponse{Success: false, LeaderAddr: s.leaderAddr}, nil
	}
	return &kvpb.DeleteResponse{Success: true}, nil
}

// --- RaftService — delegates to the Raft engine ---

func (s *Server) RequestVote(ctx context.Context, args *raftpb.RequestVoteArgs) (*raftpb.RequestVoteReply, error) {
	reply, err := s.raft.RequestVote(ctx, args)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "RequestVote: %v", err)
	}
	return reply, nil
}

func (s *Server) AppendEntries(ctx context.Context, args *raftpb.AppendEntriesArgs) (*raftpb.AppendEntriesReply, error) {
	reply, err := s.raft.AppendEntries(ctx, args)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "AppendEntries: %v", err)
	}
	return reply, nil
}

func (s *Server) InstallSnapshot(ctx context.Context, args *raftpb.SnapshotArgs) (*raftpb.SnapshotReply, error) {
	reply, err := s.raft.InstallSnapshot(ctx, args)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "InstallSnapshot: %v", err)
	}
	return reply, nil
}
