//go:build !unix

package main

import (
	"context"

	"github.com/eric642/e2b-go-sdk"
)

func watchResize(context.Context, *e2b.Sandbox, uint32, int) {}
