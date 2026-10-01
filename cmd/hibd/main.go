// Command hibd is Hibernaut's host daemon: the commands hib runs over SSH, and that systemd and
// idle-deallocate run (spec D1).
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/solev/hibernaut/internal/hibd"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	a := &hibd.App{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv}
	os.Exit(a.Run(ctx, os.Args[1:]))
}
