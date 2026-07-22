package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/sky-valley/gitrdone/internal/grdclient"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "submit" {
		fmt.Fprintln(stderr, "usage: grd submit")
		return 2
	}
	client := grdclient.Client{Stdout: stdout}
	if err := client.Submit(ctx, "."); err != nil {
		fmt.Fprintf(stderr, "grd submit: %v\n", err)
		return 1
	}
	return 0
}
