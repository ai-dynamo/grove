# Build stage
FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY src/ ./
RUN go mod tidy && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /restart-watcher .

# Runtime stage
FROM alpine:3.19
RUN apk --no-cache add ca-certificates
COPY --from=builder /restart-watcher /restart-watcher
ENTRYPOINT ["/restart-watcher"]
