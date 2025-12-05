# Build Stage for Server
FROM golang:1.23-alpine AS server-builder
WORKDIR /app
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN go build -o server-bin main.go

# Build Stage for WASM Client
FROM golang:1.23-alpine AS client-builder
WORKDIR /app
COPY client/go.mod ./
RUN go mod download
COPY client/ ./
RUN GOOS=js GOARCH=wasm go build -o main.wasm main.go

# Final Stage
FROM alpine:3.20
WORKDIR /app

# Copy server binary
COPY --from=server-builder /app/server-bin ./server

# Copy static files
COPY public/ ./public/
# Copy compiled WASM
COPY --from=client-builder /app/main.wasm ./public/

# Expose port
EXPOSE 8080

# Environment variables
ENV PORT=8080

# Run server
CMD ["./server"]
