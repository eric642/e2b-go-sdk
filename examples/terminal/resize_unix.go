//go:build unix

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"github.com/eric642/e2b-go-sdk"
)

func watchResize(ctx context.Context, sbx *e2b.Sandbox, pid uint32, fd int) {
	if !term.IsTerminal(fd) {
		return
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	defer signal.Stop(ch)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			cols, rows, err := term.GetSize(fd)
			if err != nil {
				continue
			}
			_ = sbx.Pty.Resize(ctx, pid, uint32(cols), uint32(rows))
		}
	}
}
