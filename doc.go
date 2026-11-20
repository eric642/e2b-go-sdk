// Package e2b is a Go client SDK for the E2B sandbox platform.
//
// It tracks the official Python (https://github.com/e2b-dev/E2B/tree/main/packages/python-sdk)
// and JavaScript (https://github.com/e2b-dev/E2B/tree/main/packages/js-sdk)
// SDKs with idiomatic Go APIs: context-aware calls, typed errors implementing
// [Error], and coexisting control-plane methods for both E2B Cloud and
// self-hosted E2B clusters.
//
// Pinned upstream baselines:
//   - E2B SDK OpenAPI/Protobuf spec pin: e2b@2.64.0
//   - Self-hosted E2B runtime: 2026.30 (compatible across runtime <= 2026.30, tagged 2025.31..2026.30 + pre-2025.31)
//   - In-sandbox envd daemon: envd-v0.6.13 (v0.1.0..v0.6.13 via SemVer gates)
//
// # Quick start
//
//	ctx := context.Background()
//	client, err := e2b.NewClient(e2b.Config{}) // reads E2B_API_KEY from env
//	if err != nil {
//		log.Fatal(err)
//	}
//	sbx, err := client.Create(ctx, e2b.CreateOptions{
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
// # Authentication and configuration
//
// A zero [Config] falls back to environment variables:
//   - E2B_API_KEY: team API key (X-API-Key header; wins if E2B_ACCESS_TOKEN is also set)
//   - E2B_ACCESS_TOKEN: user access token (Authorization: Bearer)
//   - E2B_DOMAIN: base domain (defaults to e2b.app)
//   - E2B_API_URL: control-plane URL override (defaults to https://api.<domain>)
//   - E2B_SANDBOX_URL: envd URL override (e.g. http://localhost:3002 for E2B Embed)
//   - E2B_VOLUME_API_URL: volume-content URL override (defaults to https://api.<volumeDomain>)
//   - E2B_HTTP_VERSION: HTTP protocol version ("1.1" or "2")
//   - E2B_DEBUG: when "true", targets localhost debug ports (3000 API, 8080 volume, 49983 envd)
//
// # Unified client
//
// Build one [Client] via [NewClient] and reuse it across calls. It resolves
// [Config] once and separates control-plane HTTP traffic (retries + request
// timeout) from long-lived envd data-plane streams and file transfers.
// Package-level helpers (Create, CreateV1, Connect, ConnectV1, Resume, Kill,
// List, ListAll) remain available for short scripts.
//
// # Multi-generation server selection
//
// Control-plane methods map 1-to-1 to server endpoints with zero hidden
// fallback probes:
//   - E2B Cloud: Create (POST /v2/sandboxes), Connect (POST /v2/sandboxes/{id}/connect),
//     Pause, List / ListAll (GET /v2/sandboxes), GetLogs (GET /v2/sandboxes/{id}/logs),
//     Fork, UpdateNetwork, Snapshots, Secrets, template.Client.Build (/v3/templates),
//     template.Client.ListV2 (/v2/templates), template.Client.SetPublic (/v2/templates/{id}),
//     and volume.NewClient.
//   - Self-Hosted Modern (runtime 2025.44..2026.30): CreateV1 (POST /sandboxes),
//     ConnectV1 (POST /sandboxes/{id}/connect), Pause / Resume, List / ListAll,
//     GetLogs (runtime >= 2026.07) or GetLogsV1, template.Client.Build (/v3/templates),
//     template.Client.ListV2 (runtime >= 2026.26) or ListV1, template.Client.SetPublic (runtime >= 2026.05),
//     volume.NewClient (runtime >= 2026.07), Secrets (runtime 2026.30).
//   - Self-Hosted Legacy (runtime < 2025.44): CreateV1, Pause / Resume (Client.Resume,
//     Sandbox.Resume, ResumeV1 via POST /sandboxes/{id}/resume), RefreshTimeout,
//     List (runtime >= 2025.31) or ListV1 (runtime < 2025.31), GetLogsV1,
//     template.Client.BuildV2 (/v2/templates), template.Client.ListV1, and template.Client.SetPublicV1.
//
// Soft-deprecated OpenAPI routes (CreateV1, ConnectV1, Resume, RefreshTimeout,
// ListV1, GetLogsV1, template.Client.ListV1, template.Client.SetPublicV1) remain
// actively served on both E2B Cloud and self-hosted runtime <= 2026.30; only
// POST /v2/templates (template.Client.Build*V2) is retired (410 Gone) on E2B
// Cloud while remaining active on self-hosted runtime <= 2026.30.
//
// High-level composite features (Sandbox.IsRunning, GetInfo empty-state
// normalization, MCP gateway bootstrap, template S3/Azure layer uploads and
// build-log fallback, volume BYOC domain routing, and envd SemVer capability
// gates in versions.go) adapt automatically across versions.
//
// # Sub-packages
//
//   - github.com/eric642/e2b-go-sdk/template: fluent template Builder, ReadyCmd helpers, v3 and v2 build pipelines, and template/tag management
//   - github.com/eric642/e2b-go-sdk/volume: persistent volume lifecycle client and streaming belt volume-content API
package e2b
