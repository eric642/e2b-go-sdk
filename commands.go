package e2b

// Commands exposes in-sandbox command execution and process management
// endpoints over envd's Connect-RPC Process service (implemented in
// commands_impl.go).
type Commands struct {
	sbx *Sandbox
}
