package template

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	e2b "github.com/eric642/e2b-go-sdk"
)

const maxDockerfileLineBytes = 1 << 20 // 1 MiB

// FromDockerfileContent parses inline Dockerfile text and applies the
// instructions to this builder. Supported instructions: FROM (single-stage,
// optional --platform and AS alias), RUN, COPY (including --chown, --chmod,
// and multiple sources), WORKDIR, USER, ENV (KEY=VAL and legacy KEY VAL).
// Multi-line '\' continuations are joined per Dockerfile rules. Unsupported
// metadata instructions are silently ignored; multi-stage FROM or COPY --from
// record an *e2b.InvalidArgumentError on the builder.
func (b *Builder) FromDockerfileContent(content string) *Builder {
	return parseDockerfile(b, content)
}

// FromDockerfileFile reads a file from disk and parses it with
// FromDockerfileContent.
func (b *Builder) FromDockerfileFile(path string) *Builder {
	data, err := os.ReadFile(path)
	if err != nil {
		b.setErr(&BuildError{Op: "dockerfile", Err: err})
		return b
	}
	return parseDockerfile(b, string(data))
}

func parseDockerfile(b *Builder, src string) *Builder {
	lines, err := readDockerfileLogicalLines(src)
	if err != nil {
		b.setErr(&BuildError{Op: "dockerfile", Err: err})
		return b
	}

	fromCount := 0
	for _, line := range lines {
		head, rest := splitInstruction(line)
		switch strings.ToUpper(head) {
		case "FROM":
			fromCount++
			if fromCount > 1 {
				b.setErr(&e2b.InvalidArgumentError{
					Message: "multi-stage Dockerfiles are not supported",
				})
				return b
			}
			image, err := parseFromLine(rest)
			if err != nil {
				b.setErr(err)
				return b
			}
			b.baseImage = image
			b.baseTemplate = ""
			b.registryConfig = nil
			if b.forceNextLayer {
				b.force = true
			}
		case "RUN":
			b.RunCmd(rest)
		case "COPY":
			if err := applyCopyLine(b, rest); err != nil {
				b.setErr(err)
				return b
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
		}
	}
	return b
}

// readDockerfileLogicalLines joins '\' line continuations, skipping blank and
// comment lines both outside and inside continuations per Dockerfile rules.
func readDockerfileLogicalLines(src string) ([]string, error) {
	scanner := bufio.NewScanner(strings.NewReader(src))
	scanner.Buffer(make([]byte, 0, 64<<10), maxDockerfileLineBytes)

	var (
		lines          []string
		logical        strings.Builder
		inContinuation bool
	)
	for scanner.Scan() {
		raw := scanner.Text()
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		rtrimmed := strings.TrimRight(raw, " \t")
		continues := strings.HasSuffix(rtrimmed, "\\")
		part := rtrimmed
		if continues {
			part = rtrimmed[:len(rtrimmed)-1]
		}
		if !inContinuation {
			part = strings.TrimLeft(part, " \t")
		} else if logical.Len() > 0 {
			last := logical.String()[logical.Len()-1]
			if last == ' ' || last == '\t' {
				part = strings.TrimLeft(part, " \t")
			}
		}
		logical.WriteString(part)
		if continues {
			inContinuation = true
			continue
		}
		if line := strings.TrimSpace(logical.String()); line != "" {
			lines = append(lines, line)
		}
		logical.Reset()
		inContinuation = false
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if inContinuation {
		if line := strings.TrimSpace(logical.String()); line != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

func parseFromLine(rest string) (string, error) {
	tokens := strings.Fields(rest)
	i := 0
	for i < len(tokens) && strings.HasPrefix(tokens[i], "--") {
		flag := tokens[i]
		name, val, hasEq := strings.Cut(flag[2:], "=")
		switch name {
		case "platform":
			if !hasEq || val == "" {
				return "", &e2b.InvalidArgumentError{
					Message: fmt.Sprintf("invalid FROM flag %q: missing value", flag),
				}
			}
		default:
			return "", &e2b.InvalidArgumentError{
				Message: fmt.Sprintf("unsupported FROM flag %q", flag),
			}
		}
		i++
	}
	args := tokens[i:]
	if len(args) == 1 {
		return args[0], nil
	}
	if len(args) == 3 && strings.EqualFold(args[1], "AS") {
		return args[0], nil
	}
	return "", &e2b.InvalidArgumentError{
		Message: fmt.Sprintf("invalid FROM instruction %q: expected <image> [AS <stage>]", rest),
	}
}

func applyCopyLine(b *Builder, rest string) error {
	tokens := splitDockerfileTokens(rest)
	var opts []CopyOption
	i := 0
	for i < len(tokens) && strings.HasPrefix(tokens[i], "--") {
		flag := tokens[i]
		name, val, hasEq := strings.Cut(flag[2:], "=")
		switch name {
		case "from":
			return &e2b.InvalidArgumentError{
				Message: "COPY --from is not supported (multi-stage builds are not supported)",
			}
		case "chown":
			if !hasEq || val == "" {
				return &e2b.InvalidArgumentError{
					Message: fmt.Sprintf("invalid COPY flag %q: missing value", flag),
				}
			}
			opts = append(opts, WithCopyUser(val))
		case "chmod":
			if !hasEq || !isValidChmodOctal(val) {
				return &e2b.InvalidArgumentError{
					Message: fmt.Sprintf("invalid COPY --chmod value %q: expected 3-4 octal digits", val),
				}
			}
			mode, err := strconv.ParseUint(val, 8, 32)
			if err != nil {
				return &e2b.InvalidArgumentError{
					Message: fmt.Sprintf("invalid COPY --chmod value %q: %v", val, err),
				}
			}
			opts = append(opts, WithCopyMode(os.FileMode(mode)))
		case "link", "parents", "exclude":
			// Accepted Docker buildkit metadata flags; no-op in E2B templates.
		default:
			return &e2b.InvalidArgumentError{
				Message: fmt.Sprintf("unsupported COPY flag %q", flag),
			}
		}
		i++
	}
	args := tokens[i:]
	if len(args) < 2 {
		return &e2b.InvalidArgumentError{
			Message: fmt.Sprintf("COPY requires at least two arguments: %q", rest),
		}
	}
	dest := args[len(args)-1]
	for _, src := range args[:len(args)-1] {
		b.Copy(src, dest, opts...)
	}
	return nil
}

func isValidChmodOctal(s string) bool {
	if len(s) < 3 || len(s) > 4 {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '7' {
			return false
		}
	}
	return true
}

func splitInstruction(line string) (head, rest string) {
	space := strings.IndexAny(line, " \t")
	if space < 0 {
		return line, ""
	}
	return line[:space], strings.TrimSpace(line[space+1:])
}

// splitDockerfileTokens splits whitespace-separated tokens while keeping
// single- or double-quoted spans intact and unquoting them.
func splitDockerfileTokens(s string) []string {
	var (
		out []string
		cur strings.Builder
	)
	inToken := false
	i := 0
	for i < len(s) {
		ch := s[i]
		switch {
		case ch == ' ' || ch == '\t':
			if inToken {
				out = append(out, cur.String())
				cur.Reset()
				inToken = false
			}
			i++
		case ch == '"' || ch == '\'':
			inToken = true
			quote := ch
			i++
			for i < len(s) && s[i] != quote {
				if quote == '"' && s[i] == '\\' && i+1 < len(s) {
					i++
				}
				cur.WriteByte(s[i])
				i++
			}
			if i < len(s) && s[i] == quote {
				i++
			}
		case ch == '\\' && i+1 < len(s):
			inToken = true
			i++
			cur.WriteByte(s[i])
			i++
		default:
			inToken = true
			cur.WriteByte(ch)
			i++
		}
	}
	if inToken {
		out = append(out, cur.String())
	}
	return out
}

// parseEnvLine parses both modern ENV KEY="a b" FOO=bar (multiple pairs with
// quoted or escaped values) and legacy ENV KEY VAL (single pair where the rest
// of the line is the value).
func parseEnvLine(rest string) map[string]string {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return nil
	}
	// Detect whether the first key is followed by '=' before whitespace.
	eqIdx := -1
	for i := 0; i < len(rest); i++ {
		if rest[i] == '=' {
			eqIdx = i
			break
		}
		if rest[i] == ' ' || rest[i] == '\t' {
			break
		}
	}
	if eqIdx <= 0 {
		// Legacy form: ENV KEY value...
		key, val := splitInstruction(rest)
		if key == "" {
			return nil
		}
		return map[string]string{key: unquoteEnvValue(val)}
	}

	envs := map[string]string{}
	i := 0
	for i < len(rest) {
		for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t') {
			i++
		}
		if i >= len(rest) {
			break
		}
		keyStart := i
		for i < len(rest) && rest[i] != '=' && rest[i] != ' ' && rest[i] != '\t' {
			i++
		}
		key := rest[keyStart:i]
		if i >= len(rest) || rest[i] != '=' || key == "" {
			// Skip malformed token without '='.
			for i < len(rest) && rest[i] != ' ' && rest[i] != '\t' {
				i++
			}
			continue
		}
		i++ // skip '='
		var val strings.Builder
		for i < len(rest) && rest[i] != ' ' && rest[i] != '\t' {
			switch rest[i] {
			case '"':
				i++
				for i < len(rest) && rest[i] != '"' {
					if rest[i] == '\\' && i+1 < len(rest) {
						i++
					}
					val.WriteByte(rest[i])
					i++
				}
				if i < len(rest) && rest[i] == '"' {
					i++
				}
			case '\'':
				i++
				for i < len(rest) && rest[i] != '\'' {
					val.WriteByte(rest[i])
					i++
				}
				if i < len(rest) && rest[i] == '\'' {
					i++
				}
			case '\\':
				if i+1 < len(rest) {
					i++
				}
				val.WriteByte(rest[i])
				i++
			default:
				val.WriteByte(rest[i])
				i++
			}
		}
		envs[key] = val.String()
	}
	return envs
}

func unquoteEnvValue(val string) string {
	if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
		quote := val[0]
		inner := val[1 : len(val)-1]
		if quote == '\'' {
			return inner
		}
		var sb strings.Builder
		for i := 0; i < len(inner); i++ {
			if inner[i] == '\\' && i+1 < len(inner) {
				i++
			}
			sb.WriteByte(inner[i])
		}
		return sb.String()
	}
	return val
}
