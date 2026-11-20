package e2b

// Git wraps Commands to run git operations inside the sandbox (method bodies
// are in git_impl.go).
type Git struct {
	cmds *Commands
}
