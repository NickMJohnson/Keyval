.PHONY: proto build run test

proto:
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/kv.proto proto/raft.proto

build:
	go build -o bin/server ./cmd/server

test:
	go test ./...

run:
	docker-compose up --build
