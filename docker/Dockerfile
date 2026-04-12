FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o /bin/server ./cmd/server

FROM alpine:3.19
COPY --from=builder /bin/server /bin/server
ENTRYPOINT ["/bin/server"]
