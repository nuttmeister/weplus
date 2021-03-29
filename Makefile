.PHONY: build

build:
	GOOS=linux GOARCH=amd64 go build -o handler
	zip handler.zip handler
	rm -rf handler
