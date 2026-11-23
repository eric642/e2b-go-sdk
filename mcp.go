package e2b

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

const (
	// DefaultMCPTemplate is the default sandbox template used when
	// CreateOptions.Mcp is non-nil and CreateOptions.Template is unset.
	DefaultMCPTemplate = "mcp-gateway"

	// MCPPort is the in-sandbox port the MCP gateway listens on.
	MCPPort = 50005

	mcpTokenPath = "/etc/mcp-gateway/.token"
)

// GetMcpURL returns the sandbox's MCP gateway URL (https://<host>/mcp, or
// http://<host>/mcp in Debug mode). It does not verify that an MCP gateway is
// running; create the sandbox with CreateOptions.Mcp to start one.
func (s *Sandbox) GetMcpURL() string {
	scheme := "https"
	if s.cfg.Debug {
		scheme = "http"
	}
	return scheme + "://" + s.GetHost(MCPPort) + "/mcp"
}

// GetMcpToken returns the MCP gateway access token for this sandbox. When the
// sandbox was created in-process with CreateOptions.Mcp, the cached token is
// returned without extra I/O; on a reconnected sandbox it reads
// /etc/mcp-gateway/.token as root (matching the reference JS/Python SDKs) and
// caches the result. Returns *FileNotFoundError if no MCP gateway is configured.
//
// Endpoint: GET /files?path=/etc/mcp-gateway/.token&username=root (envd, when uncached)
// Envd: >= v0.1.0
func (s *Sandbox) GetMcpToken(ctx context.Context) (string, error) {
	s.mu.RLock()
	cached := s.mcpToken
	s.mu.RUnlock()
	if cached != "" {
		return cached, nil
	}

	data, err := s.Files.Read(ctx, mcpTokenPath, FsOptions{User: "root"})
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(data))
	s.mu.Lock()
	if s.mcpToken == "" {
		s.mcpToken = tok
	}
	s.mu.Unlock()
	return tok, nil
}

func (c *Client) bootstrapMCP(ctx context.Context, sbx *Sandbox, mcpCfg map[string]any) error {
	token, err := newUUIDv4()
	if err != nil {
		c.killBestEffort(ctx, sbx.ID)
		return newSandboxError("generate MCP gateway token", err)
	}
	if c.cfg.Debug {
		sbx.mu.Lock()
		sbx.mcpToken = token
		sbx.mu.Unlock()
		return nil
	}

	cfgJSON, err := json.Marshal(mcpCfg)
	if err != nil {
		c.killBestEffort(ctx, sbx.ID)
		return &InvalidArgumentError{Message: "marshal MCP config: " + err.Error()}
	}

	cmdLine := "mcp-gateway --config " + mcpShellQuote(string(cfgJSON))
	_, runErr := sbx.Commands.Run(ctx, "/bin/bash", RunOptions{
		Args: []string{"-l", "-c", cmdLine},
		User: "root",
		Envs: map[string]string{
			"GATEWAY_ACCESS_TOKEN": token,
		},
	})
	if runErr != nil {
		c.killBestEffort(ctx, sbx.ID)
		if exitErr, ok := errors.AsType[*CommandExitError](runErr); ok {
			return &SandboxError{
				Message: "failed to start MCP gateway: " + strings.TrimSpace(exitErr.Result.Stderr),
				Cause:   runErr,
			}
		}
		return runErr
	}

	sbx.mu.Lock()
	sbx.mcpToken = token
	sbx.mu.Unlock()
	return nil
}

func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out[:]), nil
}

func mcpShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
