package main

import (
	"fmt"
	"os"

	"github.com/shreyashsri79/openair-cli/internal/receiver"
	"github.com/shreyashsri79/openair-cli/internal/sender"
)

const (
	ansiReset = "\x1b[0m"
	ansiRed   = "\x1b[31m"
)

func redf(format string, a ...any) string {
	return ansiRed + fmt.Sprintf(format, a...) + ansiReset
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "send":
		if len(os.Args) < 3 {
			fmt.Println(redf("Usage: openair send <file_path>"))
			os.Exit(1)
		}
		filePath := os.Args[2]
		if err := sender.RunSender(filePath); err != nil {
			fmt.Println(redf("Send failed: %v", err))
			os.Exit(1)
		}
	case "receive":
		if err := receiver.RunReceiver(); err != nil {
			fmt.Println(redf("Receiver failed: %v", err))
			os.Exit(1)
		}
	default:
		fmt.Println(redf("Unknown command: %s", command))
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("OpenAir CLI")
	fmt.Println("Usage:")
	fmt.Println("  openair send <file_path>    Send a file to an OpenAir receiver")
	fmt.Println("  openair receive             Start an OpenAir receiver to accept files")
}
