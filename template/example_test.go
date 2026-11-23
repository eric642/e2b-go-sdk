package template_test

import (
	"context"
	"fmt"
	"log"

	"github.com/eric642/e2b-go-sdk"
	"github.com/eric642/e2b-go-sdk/template"
)

func ExampleBuilder_ToDockerfile() {
	b := template.New().
		FromImage("ubuntu:22.04").
		SetWorkdir("/app").
		RunCmd("echo hello")

	dockerfile, err := b.ToDockerfile()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(dockerfile)
	// Output:
	// FROM ubuntu:22.04
	// WORKDIR /app
	// RUN echo hello
}

func ExampleNewClient() {
	ctx := context.Background()
	client, err := template.NewClient(e2b.Config{
		APIKey: "e2b_...",
	})
	if err != nil {
		log.Fatal(err)
	}

	b := template.New().
		FromImage("python:3.12-slim").
		PipInstall([]string{"requests"})

	info, err := client.Build(ctx, b, template.BuildOptions{
		Name: "my-python-template",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(info.TemplateID)
}
