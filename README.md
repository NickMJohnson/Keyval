# Keyval

A fault-tolerant distributed key-value store implementing the Raft consensus algorithm in Go, with gRPC for communication and Docker for deployment.

## Overview

Keyval runs as a cluster of nodes that agree on every write using Raft before acknowledging it to the client. The cluster remains available and consistent as long as a majority of nodes are running. A 3-node cluster tolerates 1 failure; a 5-node cluster tolerates 2.

**Stack:** Go · gRPC · Protocol Buffers · Docker

## Features

- Leader election with randomized timeouts
- Log replication across all nodes
- Automatic leader failover
- Follower catch-up after restart
- Persistent state — nodes survive restarts without losing data
- Client request deduplication — safe to retry writes
- Leader redirect — any node accepts requests and tells you where the leader is

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                      Client                         │
└───────────────────────┬─────────────────────────────┘
                        │ gRPC (KVService)
            ┌───────────▼───────────┐
            │        Node 1         │  ← Leader
            │   gRPC Server         │
            │   Raft Engine         │
            │   KV State Machine    │
            └──────┬──────────┬─────┘
       AppendEntries│          │AppendEntries
            ┌───────▼──┐  ┌───▼──────┐
            │  Node 2  │  │  Node 3  │  ← Followers
            └──────────┘  └──────────┘
```

Each node exposes a single gRPC port that handles both client requests (`KVService`) and internal Raft RPCs (`RaftService`).

## Project Structure

```
keyval/
├── cmd/server/         # Main entrypoint
├── internal/
│   ├── raft/           # Raft consensus engine (election, replication, snapshots, persistence)
│   ├── store/          # KV state machine (applied on top of Raft log)
│   └── server/         # gRPC server (KVService + RaftService)
├── proto/
│   ├── kv/             # KVService proto + generated Go code
│   └── raft/           # RaftService proto + generated Go code
├── tests/integration/  # Integration tests with fault injection
├── docker/Dockerfile
└── docker-compose.yml
```

## Prerequisites

- [Go 1.22+](https://golang.org/dl/)
- [Docker](https://docs.docker.com/get-docker/) and Docker Compose
- [protoc](https://grpc.io/docs/protoc-installation/) + plugins (only needed to regenerate proto files)
- [grpcurl](https://github.com/fullstorydev/grpcurl) (for manual testing)

```bash
brew install go protobuf grpcurl
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```

## Getting Started

**Clone and install dependencies:**

```bash
git clone https://github.com/NickMJohnson/Keyval.git
cd Keyval
go mod tidy
```

**Start a 3-node cluster:**

```bash
docker-compose up --build
```

Nodes are available on ports `8081`, `8082`, `8083`. One will elect itself leader within ~300ms — watch the logs to see which one.

**Build the binary locally:**

```bash
make build
./bin/server -id node1 -addr :8080 -peers localhost:8081,localhost:8082 -data-dir /tmp/node1
```

## Usage

All operations use gRPC. Pass `-proto proto/kv/kv.proto` to grpcurl so it knows the schema.

### Put

```bash
grpcurl -plaintext -proto proto/kv/kv.proto \
  -d '{"key":"hello","value":"world","client_id":"me","request_id":1}' \
  localhost:8081 kv.KVService/Put
```

```json
{ "success": true }
```

### Get

```bash
grpcurl -plaintext -proto proto/kv/kv.proto \
  -d '{"key":"hello"}' \
  localhost:8081 kv.KVService/Get
```

```json
{ "value": "world", "found": true }
```

### Delete

```bash
grpcurl -plaintext -proto proto/kv/kv.proto \
  -d '{"key":"hello","client_id":"me","request_id":2}' \
  localhost:8081 kv.KVService/Delete
```

```json
{ "success": true }
```

### Leader redirect

If you send a write to a follower, it returns the leader address instead of failing silently:

```json
{ "success": false, "leaderAddr": "172.18.0.3:8080" }
```

Retry the request against that address.

## API Notes

| Field | Description |
|---|---|
| `client_id` | Identifies the client. Any string — use a UUID per process in production. |
| `request_id` | Monotonically increasing per client. Increment for each new write. The store deduplicates on `(client_id, request_id)` — reusing an ID silently drops the write. |
| `found` | `false` on Get if the key does not exist. |

## Testing

**Run integration tests:**

```bash
make test-integration
```

Or:

```bash
go test -v -timeout 30s -count=1 ./tests/integration/...
```

**Tests cover:**

| Test | What it verifies |
|---|---|
| `TestLeaderElection` | A leader is elected within 3s of cluster startup |
| `TestLeaderFailover` | Killing the leader causes a different node to take over |
| `TestFollowerCatchup` | A restarted follower receives and applies missed log entries |
| `TestNoQuorum` | No node holds leadership when a majority are down |

**Test fault tolerance manually:**

```bash
# kill a node
docker-compose stop node1

# cluster keeps working on the remaining two
grpcurl -plaintext -proto proto/kv/kv.proto \
  -d '{"key":"hello"}' localhost:8082 kv.KVService/Get

# bring it back — catches up automatically
docker-compose start node1
```

## How Raft Works (briefly)

Raft solves distributed consensus in three parts:

1. **Leader election** — if followers stop hearing from the leader, they hold an election. Randomized timeouts (150–300ms) prevent split votes. A candidate needs a majority to win.

2. **Log replication** — the leader appends writes to its log and replicates them to followers via `AppendEntries`. An entry is committed once a majority acknowledge it, then applied to the KV state machine.

3. **Safety** — a node can only win an election if its log is at least as up-to-date as any majority member's. This prevents a stale node from overwriting committed entries after a restart.

Persistent state (`currentTerm`, `votedFor`, log entries) is written to disk atomically before responding to any RPC, so a node that crashes and restarts never violates safety guarantees.

## Regenerating Proto Files

```bash
make proto
```

Requires `protoc`, `protoc-gen-go`, and `protoc-gen-go-grpc` to be installed.

## Makefile Targets

| Target | Description |
|---|---|
| `make proto` | Regenerate Go code from `.proto` files |
| `make build` | Build the server binary to `bin/server` |
| `make test` | Run all tests |
| `make test-integration` | Run integration tests with verbose output |
| `make run` | Start the cluster with docker-compose |
