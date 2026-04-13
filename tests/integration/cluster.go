package integration

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	kvpb "github.com/yourusername/keyval/proto/kv"
	"github.com/yourusername/keyval/internal/raft"
	"github.com/yourusername/keyval/internal/server"
	"github.com/yourusername/keyval/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type testNode struct {
	id      string
	addr    string
	dataDir string
	raft    *raft.Raft
	store   *store.Store
	server  *server.Server
}

type testCluster struct {
	t      *testing.T
	nodes  map[string]*testNode
	killed map[string]bool
	nextID uint64
}

func newCluster(t *testing.T, n int) *testCluster {
	t.Helper()
	c := &testCluster{t: t, nodes: make(map[string]*testNode), killed: make(map[string]bool)}

	// allocate addresses first so each node knows all peers up front
	addrs := make(map[string]string, n)
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("node%d", i)
		addrs[id] = freeAddr(t)
	}

	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("node%d", i)
		peers := make([]string, 0, n-1)
		for j := 1; j <= n; j++ {
			if j != i {
				peers = append(peers, addrs[fmt.Sprintf("node%d", j)])
			}
		}
		node := startNode(t, id, addrs[id], peers, t.TempDir())
		c.nodes[id] = node
	}

	t.Cleanup(func() {
		for id, node := range c.nodes {
			if !c.killed[id] {
				node.server.Stop()
				node.raft.Stop()
			}
		}
	})
	return c
}

func startNode(t *testing.T, id, addr string, peers []string, dataDir string) *testNode {
	t.Helper()
	storage, err := raft.NewStorage(dataDir)
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}

	applyCh := make(chan raft.ApplyMsg, 256)
	r := raft.New(id, peers, applyCh, storage)
	s := store.New(r, applyCh)
	srv := server.New(s, r, "")

	go func() {
		if err := srv.Start(addr); err != nil {
			// server stopped — expected during tests
		}
	}()

	node := &testNode{id: id, addr: addr, dataDir: dataDir, raft: r, store: s, server: srv}
	return node
}

// kill stops a node's server and Raft engine (simulates a crash).
func (c *testCluster) kill(id string) {
	c.t.Helper()
	node, ok := c.nodes[id]
	if !ok {
		c.t.Fatalf("kill: unknown node %s", id)
	}
	node.server.Stop()
	node.raft.Stop()
	c.killed[id] = true
}

// restart brings a previously killed node back up using its persisted state.
func (c *testCluster) restart(id string) {
	c.t.Helper()
	node, ok := c.nodes[id]
	if !ok {
		c.t.Fatalf("restart: unknown node %s", id)
	}

	// collect peers from the cluster
	peers := make([]string, 0)
	for pid, pnode := range c.nodes {
		if pid != id {
			peers = append(peers, pnode.addr)
		}
	}

	newNode := startNode(c.t, id, node.addr, peers, node.dataDir)
	c.nodes[id] = newNode
	delete(c.killed, id)
}

// waitForLeader polls until any live node reports itself as leader or timeout.
func (c *testCluster) waitForLeader(timeout time.Duration) *testNode {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for id, node := range c.nodes {
			if !c.killed[id] && node.raft.IsLeader() {
				return node
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.t.Fatal("timed out waiting for leader")
	return nil
}

// leader returns whichever live node currently believes it is leader, or nil.
func (c *testCluster) leader() *testNode {
	for id, node := range c.nodes {
		if !c.killed[id] && node.raft.IsLeader() {
			return node
		}
	}
	return nil
}

// put sends a Put to the given node, following leader redirects once.
func (c *testCluster) put(targetAddr, key, value string) error {
	c.nextID++
	return c.putWithID(targetAddr, key, value, c.nextID)
}

func (c *testCluster) putWithID(targetAddr, key, value string, reqID uint64) error {
	conn, err := grpc.NewClient(targetAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()

	client := kvpb.NewKVServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	resp, err := client.Put(ctx, &kvpb.PutRequest{Key: key, Value: value, ClientId: "test", RequestId: reqID})
	if err != nil {
		return err
	}
	if !resp.Success && resp.LeaderAddr != "" {
		return c.putWithID(resp.LeaderAddr, key, value, reqID)
	}
	if !resp.Success {
		return fmt.Errorf("put failed, no leader addr returned")
	}
	return nil
}

// get reads a key directly from a node's local store (follower read).
func (c *testCluster) get(id, key string) (string, bool) {
	node, ok := c.nodes[id]
	if !ok {
		c.t.Fatalf("get: unknown node %s", id)
	}
	return node.store.Get(key)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freeAddr: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}
