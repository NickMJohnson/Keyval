.PHONY: proto build run test

PROTOC_GEN_GO=$(HOME)/go/bin/protoc-gen-go
PROTOC_GEN_GO_GRPC=$(HOME)/go/bin/protoc-gen-go-grpc

proto:
	protoc --plugin=protoc-gen-go=$(PROTOC_GEN_GO) \
		--plugin=protoc-gen-go-grpc=$(PROTOC_GEN_GO_GRPC) \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/kv/kv.proto proto/raft/raft.proto

build:
	go build -o bin/server ./cmd/server

test:
	go test ./...

test-integration:
	go test -v -timeout 30s ./tests/integration/...

run:
	docker-compose up --build
