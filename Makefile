.PHONY: build build-setter

build:
	GOOS=linux GOARCH=amd64 go build -o handler
	zip handler.zip handler
	rm -rf handler

build-setter:
	cd setter && GOOS=windows GOARCH=amd64 go build && zip setter-win.zip setter.exe && rm setter.exe
	cd setter && GOOS=linux GOARCH=amd64 go build && zip setter-linux.zip setter && rm setter
	cd setter && GOOS=darwin GOARCH=amd64 go build && zip setter-macos-amd64.zip setter && rm setter
	cd setter && GOOS=darwin GOARCH=arm64 go build && zip setter-macos-arm64.zip setter && rm setter
