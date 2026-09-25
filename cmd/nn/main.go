package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/lamovs/nn/internal/output"
)

var version = "dev"

func main() {
	os.Exit(nnMain())
}

func nnMain() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	go func() {
		<-ctx.Done()
		stop()
	}()

	code := runText(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	if ctx.Err() != nil {

		reportInterrupted(os.Stderr)
		return output.ExitInterrupted
	}
	return code
}

func reportInterrupted(w io.Writer) {
	fmt.Fprint(w, "\nnn: interrupted\n")
}
