// filter_spec trims the upstream E2B OpenAPI specs down to the surface the Go
// SDK generates clients for. It mirrors the decorators in upstream
// E2B/redocly.yaml (which the JS/Python SDK pipelines run through `redocly
// bundle`) so our oapi-codegen inputs stay equivalent without a Node.js
// toolchain:
//
//   - every mode: drop any node marked `x-not-implemented: true` (operations,
//     schemas, properties, ...);
//   - mode "internal" (envd.yaml, openapi-volumecontent.yml): drop operations
//     marked `x-internal: true` (orchestrator-only control endpoints such as
//     envd's /init, whose `x-go-type: SecureToken` does not compile outside
//     envd);
//   - mode "api" (openapi.yml): keep only operations whose tags intersect the
//     SDK tag set (plus GET /health), dropping admin/console endpoints;
//
// after which path items left without operations are removed and
// unreferenced components are pruned (redocly's remove-unused-components).
//
// The filter is deliberately dependency-free (no YAML library in go.mod): it
// works on the block-structured layout upstream uses (2-space indentation,
// paths at indent 2, operations at indent 4, operation fields at indent 6,
// components at indent 2/4) and removes whole line blocks, so every kept line
// stays byte-identical to upstream. It fails loudly if the layout changes.
//
// Usage (invoked by scripts/sync-spec.sh):
//
//	go run -mod=readonly ./scripts/filter_spec.go <api|internal> <file.yml> [<file.yml>...]
package main

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
)

// sdkTags mirrors the python-sdk filter-in list in E2B/redocly.yaml.
var sdkTags = []string{"sandboxes", "snapshots", "templates", "tags", "volumes", "secrets"}

var httpMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

var refRE = regexp.MustCompile(`#/components/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)`)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/filter_spec.go <api|internal> <file>...")
		os.Exit(2)
	}
	mode := os.Args[1]
	if mode != "api" && mode != "internal" {
		fmt.Fprintf(os.Stderr, "filter_spec: unknown mode %q\n", mode)
		os.Exit(2)
	}
	for _, path := range os.Args[2:] {
		if err := filterFile(mode, path); err != nil {
			fmt.Fprintf(os.Stderr, "filter_spec: %s: %v\n", path, err)
			os.Exit(1)
		}
	}
}

func filterFile(mode, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, removedOps, pruned, err := filterText(mode, string(raw))
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "==> filtered %s (mode=%s): removed %d operations, pruned %d components\n", path, mode, removedOps, pruned)
	return nil
}

func filterText(mode, raw string) (string, int, int, error) {
	if strings.Contains(raw, "\t") {
		return "", 0, 0, fmt.Errorf("tab characters are not supported")
	}
	text := strings.ReplaceAll(raw, "\r\n", "\n")
	trailingNL := strings.HasSuffix(text, "\n")
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")

	lines = stripNotImplemented(lines)
	lines, removedOps, err := filterPaths(mode, lines)
	if err != nil {
		return "", 0, 0, err
	}
	lines, pruned := pruneComponents(lines)
	lines = collapseBlankLines(lines)

	out := strings.Join(lines, "\n")
	if trailingNL {
		out += "\n"
	}
	return out, removedOps, pruned, nil
}

func indent(line string) int {
	trimmed := strings.TrimLeft(line, " ")
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return -1
	}
	return len(line) - len(trimmed)
}

func key(line string) string {
	t := strings.TrimSpace(line)
	if strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "#") {
		return ""
	}
	i := keyEnd(t)
	if i <= 0 {
		return ""
	}
	return strings.Trim(t[:i], `"'`)
}

func keyEnd(t string) int {
	if t == "" {
		return -1
	}
	if q := t[0]; q == '"' || q == '\'' {
		j := strings.IndexByte(t[1:], q)
		if j < 0 {
			return -1
		}
		end := j + 2
		if end < len(t) && t[end] == ':' {
			return end
		}
		return -1
	}
	for i := 0; i < len(t); i++ {
		if t[i] == ':' && (i+1 == len(t) || t[i+1] == ' ') {
			return i
		}
	}
	return -1
}

func value(line string) string {
	t := strings.TrimSpace(line)
	i := keyEnd(t)
	if i < 0 {
		return ""
	}
	v := strings.TrimSpace(t[i+1:])
	if j := strings.Index(v, " #"); j >= 0 {
		v = strings.TrimSpace(v[:j])
	}
	return v
}

func isSeqItem(line string) bool {
	t := strings.TrimSpace(line)
	return t == "-" || strings.HasPrefix(t, "- ")
}

// isBlockScalarHeader reports whether a YAML scalar value (with any trailing
// comment stripped) introduces a literal ('|') or folded ('>') block scalar,
// e.g. "|", "|-", ">+", "|2", ">-2".
func isBlockScalarHeader(v string) bool {
	if v == "" || (v[0] != '|' && v[0] != '>') {
		return false
	}
	rest := v[1:]
	var seenChomp, seenIndent bool
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case '-', '+':
			if seenChomp {
				return false
			}
			seenChomp = true
		case '1', '2', '3', '4', '5', '6', '7', '8', '9':
			if seenIndent {
				return false
			}
			seenIndent = true
		default:
			return false
		}
	}
	return true
}

func startsBlockScalar(line string) (int, bool) {
	ind := indent(line)
	if ind < 0 {
		return 0, false
	}
	if isBlockScalarHeader(value(line)) {
		return ind, true
	}
	t := strings.TrimSpace(line)
	if strings.HasPrefix(t, "- ") {
		rem := strings.TrimSpace(t[2:])
		if j := strings.Index(rem, " #"); j >= 0 {
			rem = strings.TrimSpace(rem[:j])
		}
		if isBlockScalarHeader(rem) {
			return ind, true
		}
	}
	return 0, false
}

// blockScalarLines marks every line that is body content inside a YAML literal
// ('|') or folded ('>') block scalar so structural passes ignore prose.
func blockScalarLines(lines []string) []bool {
	inScalar := make([]bool, len(lines))
	active := false
	parentInd := -1
	for i, l := range lines {
		if active {
			trimmed := strings.TrimLeft(l, " ")
			if trimmed == "" || len(l)-len(trimmed) > parentInd {
				inScalar[i] = true
				continue
			}
			active = false
		}
		if pInd, ok := startsBlockScalar(l); ok {
			active = true
			parentInd = pInd
		}
	}
	return inScalar
}

func blockEnd(lines []string, start int) int {
	base := indent(lines[start])
	// YAML allows sequence items under a mapping key to share the key's indent
	// (e.g. "tags:\n- templates").
	allowSameIndentSeq := !isSeqItem(lines[start]) && key(lines[start]) != "" && value(lines[start]) == ""
	inScalar := false
	scalarParentInd := -1
	if pInd, ok := startsBlockScalar(lines[start]); ok {
		inScalar = true
		scalarParentInd = pInd
	}
	end := start + 1
	for i := start + 1; i < len(lines); i++ {
		if inScalar {
			trimmed := strings.TrimLeft(lines[i], " ")
			if trimmed == "" {
				continue
			}
			if len(lines[i])-len(trimmed) > scalarParentInd {
				end = i + 1
				continue
			}
			inScalar = false
		}
		ind := indent(lines[i])
		if ind == -1 {
			continue
		}
		if ind < base {
			break
		}
		if ind == base && (!allowSameIndentSeq || !isSeqItem(lines[i])) {
			break
		}
		end = i + 1
		if pInd, ok := startsBlockScalar(lines[i]); ok {
			inScalar = true
			scalarParentInd = pInd
		}
	}
	return end
}

func section(lines []string, name string) (start, end int, ok bool) {
	inScalar := false
	scalarParentInd := -1
	for i, l := range lines {
		if inScalar {
			trimmed := strings.TrimLeft(l, " ")
			if trimmed == "" || len(l)-len(trimmed) > scalarParentInd {
				continue
			}
			inScalar = false
		}
		if indent(l) == 0 && key(l) == name {
			return i + 1, blockEnd(lines, i), true
		}
		if pInd, ok := startsBlockScalar(l); ok {
			inScalar = true
			scalarParentInd = pInd
		}
	}
	return 0, 0, false
}

func children(lines []string, start, end, ind int) []int {
	var out []int
	inScalar := false
	scalarParentInd := -1
	for i := start; i < end; i++ {
		if inScalar {
			trimmed := strings.TrimLeft(lines[i], " ")
			if trimmed == "" || len(lines[i])-len(trimmed) > scalarParentInd {
				continue
			}
			inScalar = false
		}
		if indent(lines[i]) == ind {
			out = append(out, i)
		}
		if pInd, ok := startsBlockScalar(lines[i]); ok {
			inScalar = true
			scalarParentInd = pInd
		}
	}
	return out
}

func isTrue(v string) bool { return v == "true" }

func collapseBlankLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.TrimSpace(l) == "" && len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
			continue
		}
		out = append(out, l)
	}
	return out
}

func stripNotImplemented(lines []string) []string {
	for {
		inScalar := blockScalarLines(lines)
		hit := -1
		for i, l := range lines {
			if inScalar[i] {
				continue
			}
			if key(l) == "x-not-implemented" && isTrue(value(l)) {
				hit = i
				break
			}
		}
		if hit < 0 {
			return lines
		}
		ind := indent(lines[hit])
		parent := -1
		for i := hit - 1; i >= 0; i-- {
			if inScalar[i] {
				continue
			}
			if p := indent(lines[i]); p != -1 && p < ind {
				parent = i
				break
			}
		}
		if parent < 0 {
			lines = slices.Delete(lines, hit, hit+1)
			continue
		}
		wasPathOp := false
		if indent(lines[parent]) == 4 && slices.Contains(httpMethods, key(lines[parent])) {
			if pStart, pEnd, ok := section(lines, "paths"); ok && parent >= pStart && parent < pEnd {
				wasPathOp = true
			}
		}
		lines = slices.Delete(lines, parent, blockEnd(lines, parent))
		if wasPathOp {
			lines = pruneEmptyPathItem(lines, parent)
		}
	}
}

// pruneEmptyPathItem removes the enclosing indent-2 path item if stripping an
// operation left it with no remaining HTTP methods.
func pruneEmptyPathItem(lines []string, deletedAt int) []string {
	pStart, pEnd, ok := section(lines, "paths")
	if !ok {
		return lines
	}
	inScalar := blockScalarLines(lines)
	pathIdx := -1
	for i := min(deletedAt, len(lines)) - 1; i >= pStart; i-- {
		if inScalar[i] {
			continue
		}
		if indent(lines[i]) == 2 {
			pathIdx = i
			break
		}
	}
	if pathIdx < 0 || pathIdx >= pEnd || !strings.HasPrefix(key(lines[pathIdx]), "/") {
		return lines
	}
	itemEnd := blockEnd(lines, pathIdx)
	for _, m := range children(lines, pathIdx+1, itemEnd, 4) {
		if slices.Contains(httpMethods, key(lines[m])) {
			return lines
		}
	}
	return slices.Delete(lines, pathIdx, itemEnd)
}

func filterPaths(mode string, lines []string) ([]string, int, error) {
	start, end, ok := section(lines, "paths")
	if !ok {
		return nil, 0, fmt.Errorf("no top-level paths section")
	}
	type span struct{ from, to int }
	var drop []span
	removed := 0
	for _, p := range children(lines, start, end, 2) {
		pathKey := key(lines[p])
		if !strings.HasPrefix(pathKey, "/") {
			return nil, 0, fmt.Errorf("line %d: unexpected layout under paths (want 2-space indented path keys): %q", p+1, lines[p])
		}
		pEnd := blockEnd(lines, p)
		methods, kept := 0, 0
		var opDrops []span
		for _, m := range children(lines, p+1, pEnd, 4) {
			method := key(lines[m])
			if !slices.Contains(httpMethods, method) {
				continue
			}
			methods++
			mEnd := blockEnd(lines, m)
			if keepOperation(mode, pathKey, method, lines, m+1, mEnd) {
				kept++
				continue
			}
			removed++
			opDrops = append(opDrops, span{m, mEnd})
		}
		if methods == 0 {
			return nil, 0, fmt.Errorf("line %d: path %s has no operations at indent 4", p+1, pathKey)
		}
		if kept == 0 {
			drop = append(drop, span{p, pEnd})
		} else {
			drop = append(drop, opDrops...)
		}
	}
	for _, d := range slices.Backward(drop) {
		lines = slices.Delete(lines, d.from, d.to)
	}
	return lines, removed, nil
}

func keepOperation(mode, path, method string, lines []string, start, end int) bool {
	fields := children(lines, start, end, 6)
	switch mode {
	case "internal":
		for _, f := range fields {
			if key(lines[f]) == "x-internal" && isTrue(value(lines[f])) {
				return false
			}
		}
		return true
	case "api":
		if path == "/health" && method == "get" {
			return true
		}
		for _, f := range fields {
			if key(lines[f]) != "tags" {
				continue
			}
			for _, t := range operationTags(lines, f, end) {
				if slices.Contains(sdkTags, t) {
					return true
				}
			}
		}
		return false
	}
	return true
}

func operationTags(lines []string, at, end int) []string {
	var tags []string
	if v := value(lines[at]); v != "" {
		v = strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")
		for t := range strings.SplitSeq(v, ",") {
			if t = strings.Trim(strings.TrimSpace(t), `"'`); t != "" {
				tags = append(tags, t)
			}
		}
		return tags
	}
	for i := at + 1; i < blockEnd(lines, at) && i < end; i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "- ") {
			tags = append(tags, strings.Trim(strings.TrimSpace(t[2:]), `"'`))
		}
	}
	return tags
}

func pruneComponents(lines []string) ([]string, int) {
	start, end, ok := section(lines, "components")
	if !ok {
		return lines, 0
	}
	type span struct{ from, to int }
	blocks := map[string]span{}
	type group struct {
		kind     string
		from, to int
		names    []string
	}
	var groups []group
	for _, g := range children(lines, start, end, 2) {
		gEnd := blockEnd(lines, g)
		gr := group{kind: key(lines[g]), from: g, to: gEnd}
		for _, n := range children(lines, g+1, gEnd, 4) {
			name := key(lines[n])
			blocks[gr.kind+"/"+name] = span{n, blockEnd(lines, n)}
			gr.names = append(gr.names, name)
		}
		groups = append(groups, gr)
	}

	reachable := map[string]bool{}
	var queue []string
	scan := func(from, to int) {
		for i := from; i < to; i++ {
			for _, m := range refRE.FindAllStringSubmatch(lines[i], -1) {
				id := m[1] + "/" + m[2]
				if !reachable[id] {
					reachable[id] = true
					queue = append(queue, id)
				}
			}
		}
	}
	scan(0, start-1)
	scan(end, len(lines))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if b, ok := blocks[id]; ok {
			scan(b.from, b.to)
		}
	}

	var drop []span
	pruned := 0
	for _, g := range groups {
		if g.kind == "securitySchemes" {
			continue
		}
		var groupDrops []span
		keptNames := 0
		for _, n := range g.names {
			if reachable[g.kind+"/"+n] {
				keptNames++
				continue
			}
			groupDrops = append(groupDrops, blocks[g.kind+"/"+n])
			pruned++
		}
		if keptNames == 0 && len(g.names) > 0 {
			drop = append(drop, span{g.from, g.to})
		} else {
			drop = append(drop, groupDrops...)
		}
	}
	slices.SortFunc(drop, func(a, b span) int { return a.from - b.from })
	for _, d := range slices.Backward(drop) {
		lines = slices.Delete(lines, d.from, d.to)
	}
	return lines, pruned
}
