package e2b_test

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/eric642/e2b-go-sdk"
)

func ExampleGetSignature() {
	sig, err := e2b.GetSignature(
		"/home/user/hello.txt",
		e2b.SignatureRead,
		"example-token",
		e2b.SignatureOptions{User: "user"},
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(sig.Value)
	// Output:
	// v1_1HtEcpQNp2wfdKa9NLF0h0XdOk6H9IiSXhcqCX6siSs
}

func ExampleNewClient() {
	ctx := context.Background()
	client, err := e2b.NewClient(e2b.Config{
		APIKey: "e2b_...",
	})
	if err != nil {
		log.Fatal(err)
	}

	sbx, err := client.Create(ctx, e2b.CreateOptions{
		Template: "base",
		Timeout:  5 * time.Minute,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = sbx.Kill(ctx) }()

	h, err := sbx.Commands.Run(ctx, "echo hello", e2b.RunOptions{})
	if err != nil {
		log.Fatal(err)
	}
	res, err := h.Wait(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(res.Stdout)
}
