.PHONY: build test demo
build:
	go build -o erc20-check ./cmd/erc20-check

test:
	go vet ./...
	go test -race ./...

demo:
	npm --prefix demo ci --ignore-scripts --no-audit --no-fund
	node demo/demo.mjs
