.PHONY: all build clean run docker

all: build

build:
	@echo "Building WASM client..."
	cd client && GOOS=js GOARCH=wasm go build -o ../public/main.wasm main.go
	@echo "Building Server..."
	cd server && go build -o ../server-bin main.go

clean:
	rm -f public/main.wasm server-bin

run: build
	@echo "Running server..."
	cd server && PORT=8080 go run main.go

docker:
	docker-compose up --build
