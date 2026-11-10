// Command zoom-taker is the single Go binary replacing the Python pipeline:
//   zoom-taker                       serve (default)
//   zoom-taker serve                 web server + recorder page
//   zoom-taker meeting <name>        full meeting pipeline (filters + merge)
//   zoom-taker transcribe <file>     single-file transcription
//   zoom-taker download              download the GGML model into the cache
package main

import (
	"fmt"
	"os"
)

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}

	switch cmd {
	case "serve", "meeting", "transcribe", "download":
		// implemented in later commits; scaffold only builds for now
		fmt.Fprintf(os.Stderr, "%s: not implemented yet\n", cmd)
		os.Exit(2)
	default:
		fmt.Fprintf(os.Stderr, "usage: zoom-taker [serve|meeting|transcribe|download] [args]\n")
		os.Exit(2)
	}
}
