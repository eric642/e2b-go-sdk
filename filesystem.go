package e2b

// Filesystem exposes in-sandbox filesystem metadata, directory watching, and
// file transfer endpoints over envd's Connect-RPC Filesystem service and HTTP
// /files endpoints (implemented in filesystem_impl.go).
type Filesystem struct {
	sbx *Sandbox
}

// WatchEvent is an alias for FilesystemEvent emitted by Filesystem.Watch and
// Filesystem.WatchWithOptions.
type WatchEvent = FilesystemEvent
