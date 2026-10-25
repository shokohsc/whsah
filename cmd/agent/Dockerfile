# Agent image (runs inside a gaming VM)
FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/agent ./cmd/agent

FROM alpine:3.20
WORKDIR /app
COPY --from=builder /bin/agent /bin/agent
ENTRYPOINT ["/bin/agent"]
