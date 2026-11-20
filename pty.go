package e2b

// Pty exposes pseudo-terminal (PTY) creation, connection, resize, input, and
// signal endpoints over envd's Connect-RPC Process service (implemented in
// pty_impl.go).
type Pty struct {
	sbx *Sandbox
}
