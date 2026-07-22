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
	if len(args) != 1 || (args[0] != "submit" && args[0] != "status" && args[0] != "sync") {
		fmt.Fprintln(stderr, "usage: grd <submit|status|sync>")
		return 2
	}
	client := grdclient.Client{Stdout: stdout}
	var err error
	if args[0] == "submit" {
		err = client.Submit(ctx, ".")
	} else if args[0] == "status" {
		err = client.Status(ctx, ".")
	} else {
		err = client.Sync(ctx, ".")
	}
	if err != nil {
		fmt.Fprintf(stderr, "grd %s: %v\n", args[0], err)
		return 1
	}
	return 0
}
