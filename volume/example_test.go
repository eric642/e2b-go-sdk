package volume_test

import (
	"context"
	"fmt"
	"log"

	"github.com/eric642/e2b-go-sdk"
	"github.com/eric642/e2b-go-sdk/volume"
)

func ExampleClient_ConnectWithToken() {
	client, err := volume.NewClient(e2b.Config{
		APIKey: "example-key",
		Domain: "e2b.app",
	})
	if err != nil {
		log.Fatal(err)
	}

	vol, err := client.ConnectWithToken("vol-123", "example-jwt")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(vol.ID, vol.Token() != "")
	// Output:
	// vol-123 true
}

func ExampleNewClient() {
	ctx := context.Background()
	client, err := volume.NewClient(e2b.Config{
		APIKey: "e2b_...",
	})
	if err != nil {
		log.Fatal(err)
	}

	vol, err := client.Create(ctx, "my-dataset")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = vol.Delete(ctx) }()

	if err := vol.WriteFile(ctx, "/hello.txt", []byte("hello volume")); err != nil {
		log.Fatal(err)
	}
	content, err := vol.ReadFileString(ctx, "/hello.txt")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(content)
}
