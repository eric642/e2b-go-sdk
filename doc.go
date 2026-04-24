// Package e2b is a Go client for the E2B sandbox platform.
//
// It ports the public surface of the Python (https://github.com/e2b-dev/E2B/tree/main/packages/python-sdk)
// and JavaScript (https://github.com/e2b-dev/E2B/tree/main/packages/js-sdk)
// SDKs to idiomatic Go: blocking APIs with context.Context, channels for
// streaming output, and typed errors.
//
// # Quick start
//
//	ctx := context.Background()
//	sbx, err := e2b.Create(ctx, e2b.CreateOptions{
//		Template: "base",
//		Timeout:  5 * time.Minute,
//	})
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer sbx.Kill(ctx)
//
//	handle, err := sbx.Commands.Run(ctx, "echo", e2b.RunOptions{Args: []string{"hello"}})
//	if err != nil {
//		log.Fatal(err)
//	}
//	result, _ := handle.Wait(ctx)
//	fmt.Println(result.Stdout) // hello
//
// # Authentication
//
// The SDK reads credentials from the environment by default:
//   - E2B_API_KEY: team API key (X-API-Key header)
//   - E2B_ACCESS_TOKEN: user access token (Authorization: Bearer)
//   - E2B_DOMAIN: defaults to e2b.app
//   - E2B_DEBUG: when "true", targets http://localhost:3000
//
// Pass an explicit Config to override.
//
// # Sub-packages
//
//   - template: fluent builder for sandbox templates (serialization only in v1)
//   - volume: client for persistent volumes
package e2b
