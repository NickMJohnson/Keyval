package main

import (
	"flag"
	"log"
	"os"
	"strings"

	"github.com/NickMJohnson/Keyval/internal/raft"
	"github.com/NickMJohnson/Keyval/internal/server"
	"github.com/NickMJohnson/Keyval/internal/store"
)

func main() {
	id := flag.String("id", "", "unique node ID (e.g. node1)")
	addr := flag.String("addr", ":8080", "gRPC listen address")
	peers := flag.String("peers", "", "comma-separated gRPC addresses of other nodes")
	dataDir := flag.String("data-dir", "/var/lib/keyval", "directory for persistent state")
	flag.Parse()

	if *id == "" {
		host, _ := os.Hostname()
		*id = host
	}

	var peerList []string
	if *peers != "" {
		for _, p := range strings.Split(*peers, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				peerList = append(peerList, p)
			}
		}
	}

	storage, err := raft.NewStorage(*dataDir)
	if err != nil {
		log.Fatalf("init storage: %v", err)
	}

	applyCh := make(chan raft.ApplyMsg, 256)
	r := raft.New(*id, peerList, applyCh, storage)
	s := store.New(r, applyCh)
	srv := server.New(s, r, "")

	log.Printf("node %s listening on %s (peers: %v)", *id, *addr, peerList)
	if err := srv.Start(*addr); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
