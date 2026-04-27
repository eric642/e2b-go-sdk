package template

import (
	"bufio"
	"os"
	"strings"
)

// FromDockerfileContent parses inline Dockerfile text and applies the
// instructions to this builder. Supported instructions: FROM, RUN, COPY
// (two-token form), WORKDIR, USER, ENV. Others are silently ignored.
func (b *Builder) FromDockerfileContent(content string) *Builder {
	return parseDockerfile(b, content)
}

// FromDockerfileFile reads a file from disk and parses it with
// FromDockerfileContent.
func (b *Builder) FromDockerfileFile(path string) *Builder {
	data, err := os.ReadFile(path)
	if err != nil {
		if b.err == nil {
			b.err = err
		}
		return b
	}
	return parseDockerfile(b, string(data))
}

func parseDockerfile(b *Builder, src string) *Builder {
	scanner := bufio.NewScanner(strings.NewReader(src))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		head, rest := splitInstruction(line)
		switch strings.ToUpper(head) {
		case "FROM":
			b.baseImage = rest
		case "RUN":
			b.RunCmd(rest)
		case "COPY":
			parts := strings.Fields(rest)
			if len(parts) >= 2 {
				b.Copy(parts[0], parts[1])
			}
		case "WORKDIR":
			b.SetWorkdir(rest)
		case "USER":
			b.SetUser(rest)
		case "ENV":
			envs := parseEnvLine(rest)
			if len(envs) > 0 {
				b.SetEnvs(envs)
			}
		default:
			// TODO: parser ignores unsupported instructions (LABEL,
			// HEALTHCHECK, ENTRYPOINT, CMD, ARG, etc.). Extend as needed.
		}
	}
	return b
}

func splitInstruction(line string) (head, rest string) {
	space := strings.IndexAny(line, " \t")
	if space < 0 {
		return line, ""
	}
	return line[:space], strings.TrimSpace(line[space+1:])
}

// parseEnvLine splits an "ENV" value into key/value pairs. Only the
// "ENV KEY=VALUE [KEY=VALUE ...]" form is supported (not the legacy
// "ENV KEY VALUE" form, which is ambiguous with multiple keys).
func parseEnvLine(rest string) map[string]string {
	envs := map[string]string{}
	for _, tok := range strings.Fields(rest) {
		eq := strings.IndexByte(tok, '=')
		if eq <= 0 {
			continue
		}
		envs[tok[:eq]] = strings.Trim(tok[eq+1:], `"`)
	}
	return envs
}
