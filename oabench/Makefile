.PHONY: build test winkit clean

build:
	go build -o oabench .

test:
	go test ./...

# Cross-compile the Windows baseline binary for the D-22/D-23 runbook.
winkit:
	GOOS=windows GOARCH=amd64 go build -o winkit/oabench-amd64.exe .
	GOOS=windows GOARCH=arm64 go build -o winkit/oabench-arm64.exe .
	@ls -lh winkit/*.exe

clean:
	rm -f oabench winkit/*.exe
