# Worker image (Go job-queue worker: config generation, metadata retrieval, library scan)
FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/worker ./cmd/worker

FROM alpine:3.20
WORKDIR /app
COPY --from=builder /bin/worker /bin/worker
ENTRYPOINT ["/bin/worker"]
