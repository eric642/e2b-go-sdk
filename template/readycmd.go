package template

import "fmt"

// ReadyCmd wraps a shell command evaluated by envd to decide when the
// sandbox has finished starting.
type ReadyCmd struct {
	cmd string
}

// Cmd returns the underlying shell snippet.
func (r ReadyCmd) Cmd() string { return r.cmd }

// WaitForPort checks whether the given TCP port is listening.
func WaitForPort(port int) ReadyCmd {
	return ReadyCmd{cmd: fmt.Sprintf("ss -tuln | grep :%d", port)}
}

// WaitForURL polls an HTTP endpoint until it returns the expected status code.
func WaitForURL(url string, statusCode int) ReadyCmd {
	if statusCode == 0 {
		statusCode = 200
	}
	return ReadyCmd{cmd: fmt.Sprintf("curl -s -o /dev/null -w \"%%{http_code}\" %s | grep -q \"%d\"", url, statusCode)}
}

// WaitForProcess checks whether a process with the given name is running.
func WaitForProcess(name string) ReadyCmd {
	return ReadyCmd{cmd: fmt.Sprintf("pgrep %s > /dev/null", name)}
}

// WaitForFile checks for the existence of a file.
func WaitForFile(path string) ReadyCmd {
	return ReadyCmd{cmd: fmt.Sprintf("[ -f %s ]", path)}
}

// WaitForTimeoutMs sleeps for the given number of milliseconds (rounded up
// to at least 1 second).
func WaitForTimeoutMs(ms int) ReadyCmd {
	seconds := ms / 1000
	if seconds < 1 {
		seconds = 1
	}
	return ReadyCmd{cmd: fmt.Sprintf("sleep %d", seconds)}
}
