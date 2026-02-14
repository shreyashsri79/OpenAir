package errorhandler

import (
	"fmt"
	"os"
)

func FatalRed(msg string, err error) {
	fmt.Fprintf(os.Stderr, "\033[31m%s:\033[0m %v\n", msg, err)
	os.Exit(1)
}
