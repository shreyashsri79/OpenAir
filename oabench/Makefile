.PHONY: build test winkit androidkit clean

build:
	go build -o oabench .

test:
	go test ./...

# Cross-compile the Windows baseline binary for the D-22/D-23 runbook.
winkit:
	GOOS=windows GOARCH=amd64 go build -o winkit/oabench-amd64.exe .
	GOOS=windows GOARCH=arm64 go build -o winkit/oabench-arm64.exe .
	@ls -lh winkit/*.exe

# Cross-compile the on-device Android binary for the D-31 follow-up runbook.
androidkit:
	CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build -o androidkit/oabench-android-arm64 .
	@ls -lh androidkit/oabench-android-arm64

clean:
	rm -f oabench winkit/*.exe androidkit/oabench-android-*
