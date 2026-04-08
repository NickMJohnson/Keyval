package main

import (
	"flag"
	"log"
	"os"
	"strings"

	"github.com/yourusername/keyval/internal/raft"
	"github.com/yourusername/keyval/internal/server"
	"github.com/yourusername/keyval/internal/store"
)

func main() {
	id := flag.String("id", "", "unique node ID (e.g. node1)")
	addr := flag.String("addr", ":8080", "gRPC listen address")
	peers := flag.String("peers", "", "comma-separated gRPC addresses of other nodes")
	flag.Parse()

	if *id == "" {
		// fall back to hostname when running in Docker
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

	applyCh := make(chan raft.ApplyMsg, 256)
	r := raft.New(*id, peerList, applyCh)
	s := store.New(r, applyCh)
	srv := server.New(s, r, "")

	log.Printf("node %s listening on %s (peers: %v)", *id, *addr, peerList)
	if err := srv.Start(*addr); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
