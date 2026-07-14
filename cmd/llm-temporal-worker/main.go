package main

import (
	"context"
	"io"
	"os"
)

func main() {
	os.Exit(Execute(context.Background(), os.Args[1:], CommandOptions{
		Out:    os.Stdout,
		ErrOut: os.Stderr,
		RunWorker: func(context.Context, []byte, io.Writer) error {
			return errWorkerRuntimeUnavailable
		},
	}))
}
