package template

import (
	"fmt"
	"strings"
)

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	for i := range len(s) {
		c := s[i]
		if (c < 'a' || c > 'z') &&
			(c < 'A' || c > 'Z') &&
			(c < '0' || c > '9') &&
			strings.IndexByte("_@%+=:,./-", c) < 0 {
			return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
		}
	}
	return s
}

// ReadyCmd wraps a shell command evaluated during template build to decide
// when the sandbox has finished starting.
type ReadyCmd struct {
	cmd string
}

// NewReadyCmd constructs a ReadyCmd from a custom shell snippet. Pass an empty
// string (or ReadyCmd{}) to opt out of a readiness check.
func NewReadyCmd(cmd string) ReadyCmd {
	return ReadyCmd{cmd: cmd}
}

// Cmd returns the underlying shell snippet.
func (r ReadyCmd) Cmd() string { return r.cmd }

// WaitForPort checks whether the given TCP port is listening using ss's exact
// source-port filter (sport = :<port>) so prefix ports (e.g. 80 vs 8080) never
// collide.
func WaitForPort(port int) ReadyCmd {
	return ReadyCmd{cmd: fmt.Sprintf(`[ -n "$(ss -Htuln sport = :%d)" ]`, port)}
}

// WaitForURL polls an HTTP endpoint until it returns the expected status code
// (default 200 when statusCode == 0).
func WaitForURL(url string, statusCode int) ReadyCmd {
	if statusCode == 0 {
		statusCode = 200
	}
	return ReadyCmd{cmd: fmt.Sprintf(`curl -s -o /dev/null -w "%%{http_code}" %s | grep -q "%d"`, shellQuote(url), statusCode)}
}

// WaitForProcess checks whether a process with the given name is running.
func WaitForProcess(name string) ReadyCmd {
	return ReadyCmd{cmd: fmt.Sprintf("pgrep %s > /dev/null", shellQuote(name))}
}

// WaitForFile checks for the existence of a regular file.
func WaitForFile(path string) ReadyCmd {
	return ReadyCmd{cmd: fmt.Sprintf("[ -f %s ]", shellQuote(path))}
}

// WaitForTimeoutMs sleeps for the given number of milliseconds (clamped to at
// least 1 second).
func WaitForTimeoutMs(ms int) ReadyCmd {
	seconds := max(ms/1000, 1)
	return ReadyCmd{cmd: fmt.Sprintf("sleep %d", seconds)}
}
