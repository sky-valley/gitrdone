package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/sky-valley/gitrdone/internal/grdclient"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		writeUsage(stderr)
		return 2
	}
	client := grdclient.Client{Stdout: stdout}
	var err error
	switch args[0] {
	case "submit":
		if len(args) != 1 {
			writeUsage(stderr)
			return 2
		}
		err = client.Submit(ctx, ".")
	case "status":
		if len(args) != 1 {
			writeUsage(stderr)
			return 2
		}
		err = client.Status(ctx, ".")
	case "sync":
		if len(args) != 1 {
			writeUsage(stderr)
			return 2
		}
		err = client.Sync(ctx, ".")
	case "reviews":
		if len(args) != 1 {
			writeUsage(stderr)
			return 2
		}
		err = client.Reviews(ctx, ".")
	case "approve", "request-changes":
		handle, rationale, parseErr := parseReviewResponseArgs(args[1:])
		if parseErr != nil {
			writeUsage(stderr)
			return 2
		}
		decision := "approved"
		if args[0] == "request-changes" {
			decision = "changes_requested"
		}
		err = client.RespondReview(ctx, ".", handle, decision, rationale)
	default:
		writeUsage(stderr)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "grd %s: %v\n", args[0], err)
		return 1
	}
	return 0
}

func parseReviewResponseArgs(args []string) (string, string, error) {
	if len(args) != 3 || args[1] != "-m" {
		return "", "", errors.New("review response requires a handle and -m rationale")
	}
	handle := strings.TrimSpace(args[0])
	rationale := strings.TrimSpace(args[2])
	if handle == "" || rationale == "" {
		return "", "", errors.New("review response requires a handle and rationale")
	}
	return handle, rationale, nil
}

func writeUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: grd <submit|status|sync|reviews>")
	fmt.Fprintln(w, "       grd approve <review> -m <rationale>")
	fmt.Fprintln(w, "       grd request-changes <review> -m <rationale>")
}
