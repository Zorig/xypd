# Build Stage for Server
FROM golang:1.23-alpine AS server-builder
WORKDIR /app
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o server-bin main.go

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

# Install wget for healthcheck
RUN apk add --no-cache wget

# Copy server binary
COPY --from=server-builder /app/server-bin ./server

# Copy static files
COPY public/ ./public/
# Copy compiled WASM
COPY --from=client-builder /app/main.wasm ./public/
COPY --from=client-builder /usr/local/go/misc/wasm/wasm_exec.js ./public/

# Expose port
EXPOSE 8080

# Environment variables
ENV PORT=8080

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

# Run server
CMD ["./server"]
