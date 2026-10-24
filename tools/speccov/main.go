package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// specs is a module's testdata/specs.json.
type specs struct {
	Title   string            `json:"title"`
	Files   []string          `json:"files"`
	Reasons map[string]string `json:"reasons"`
	Notes   []string          `json:"notes,omitempty"`
	Skipped map[string]string `json:"skipped"`
}

// specCase is an example of a spec file.
type specCase struct {
	line int
	// name is the enclosing contexts and the example's description.
	name string
	// blocks are the lines of the example and its enclosing blocks.
	blocks []int
}

var (
	blockRe   = regexp.MustCompile(`^(\s*)(describe|context|it|specify|it_behaves_like|include_examples|RSpec\.describe)\b\s*(.*)$`)
	exampleRe = regexp.MustCompile(`^(it|specify|it_behaves_like|include_examples)$`)
	refRe     = regexp.MustCompile(`spec/([\w/]+_spec\.rb):(\d+(?:\s*[-,]\s*\d+)*)`)
)

func main() {
	lago := flag.String("lago", filepath.Join(os.Getenv("HOME"), "Code/frameworks/lago/api"), "upstream repository")
	write := flag.Bool("write", false, "rewrite SKIPPED.md")
	flag.Parse()
	if err := run(*lago, *write, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "speccov:", err)
		os.Exit(1)
	}
}

func run(lago string, write bool, modules []string) error {
	if len(modules) == 0 {
		err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules"):
				return filepath.SkipDir
			case !d.IsDir() && d.Name() == "specs.json" && filepath.Base(filepath.Dir(path)) == "testdata":
				modules = append(modules, filepath.Dir(filepath.Dir(path)))
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	refs, err := citations(".")
	if err != nil {
		return err
	}
	var failed bool
	for _, m := range modules {
		ok, err := check(lago, m, refs, write)
		if err != nil {
			return fmt.Errorf("%s: %w", m, err)
		}
		failed = failed || !ok
	}
	if failed {
		return errors.New("uncovered or inconsistent cases")
	}
	return nil
}

// citations returns the spec lines cited by the tests, the testdata files
// and the shared conformance suites (porttest packages) of the repository
// under root.
func citations(root string) (map[string]bool, error) {
	refs := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "gen", ".nx", "target":
				return filepath.SkipDir
			}
			return nil
		}
		slash := filepath.ToSlash(path)
		test := strings.HasSuffix(path, "_test.go") || strings.Contains(slash, "/testdata/") ||
			strings.Contains(slash, "/porttest/") && strings.HasSuffix(path, ".go")
		if !test || d.Name() == "specs.json" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range refRe.FindAllStringSubmatch(string(b), -1) {
			for part := range strings.FieldsFuncSeq(m[2], func(r rune) bool { return r == ',' || r == ' ' }) {
				lo, hi, rng := strings.Cut(part, "-")
				a, _ := strconv.Atoi(strings.TrimSpace(lo))
				z := a
				if rng {
					z, _ = strconv.Atoi(strings.TrimSpace(hi))
				}
				for l := a; l <= z; l++ {
					refs[m[1]+":"+strconv.Itoa(l)] = true
				}
			}
		}
		return nil
	})
	return refs, err
}

// cases parses the examples of a spec file by indentation.
func cases(path string) ([]specCase, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	type open struct {
		indent int
		line   int
		name   string
		ctx    bool
	}
	var (
		stack []open
		out   []specCase
		n     int
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		n++
		m := blockRe.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		indent := len(m[1])
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		desc, quoted := describe(m[3])
		if exampleRe.MatchString(m[2]) {
			c := specCase{line: n}
			var names []string
			for _, o := range stack {
				c.blocks = append(c.blocks, o.line)
				if o.ctx && o.name != "" {
					names = append(names, o.name)
				}
			}
			c.blocks = append(c.blocks, n)
			switch {
			case desc == "":
				desc = m[2]
			case quoted && (m[2] == "it" || m[2] == "specify"):
				desc = `"` + desc + `"`
			default:
				desc = m[2] + " " + desc
			}
			c.name = strings.Join(append(names, desc), " > ")
			out = append(out, c)
			continue
		}
		stack = append(stack, open{indent: indent, line: n, name: desc, ctx: m[2] == "context"})
	}
	return out, sc.Err()
}

// describe returns the description of a block's opening line and whether
// it is a string.
func describe(rest string) (string, bool) {
	rest = strings.TrimSpace(rest)
	rest = strings.TrimPrefix(rest, "(")
	if rest == "" || rest[0] != '"' && rest[0] != '\'' {
		if i := strings.IndexAny(rest, " ,{("); i > 0 {
			return escape(strings.TrimSuffix(rest[:i], ")")), false
		}
		return escape(strings.TrimSuffix(strings.TrimSuffix(rest, " do"), ")")), false
	}
	q := rest[0]
	if i := strings.IndexByte(rest[1:], q); i >= 0 {
		return escape(rest[1 : i+1]), true
	}
	return escape(rest), true
}

// skipReason returns the reason of the innermost skipped entry that
// covers a case: its own line, an enclosing block's line, or its file.
func skipReason(skipped map[string]string, file string, blocks []int) (string, bool) {
	for _, l := range slices.Backward(blocks) {
		if r, ok := skipped[file+":"+strconv.Itoa(l)]; ok {
			return r, true
		}
	}
	r, ok := skipped[file]
	return r, ok
}

// escape makes s safe in a Markdown table cell.
func escape(s string) string { return strings.ReplaceAll(s, "|", "\\|") }

func check(lago, module string, refs map[string]bool, write bool) (bool, error) {
	b, err := os.ReadFile(filepath.Join(module, "testdata", "specs.json"))
	if err != nil {
		return false, err
	}
	var s specs
	if err := json.Unmarshal(b, &s); err != nil {
		return false, fmt.Errorf("specs.json: %w", err)
	}
	ok := true
	report := func(format string, args ...any) {
		ok = false
		fmt.Printf("%s: "+format+"\n", append([]any{module}, args...)...)
	}
	type row struct {
		c      specCase
		reason string
	}
	byFile := map[string][]row{}
	counts := map[string]int{}
	seen := map[string]bool{}
	ported, skippedN, total := 0, 0, 0
	for _, file := range s.Files {
		cs, err := cases(filepath.Join(lago, "spec", file))
		if err != nil {
			return false, err
		}
		for _, c := range cs {
			total++
			key := file + ":" + strconv.Itoa(c.line)
			seen[key] = true
			for _, l := range c.blocks {
				seen[file+":"+strconv.Itoa(l)] = true
			}
			cited := slices.ContainsFunc(c.blocks, func(l int) bool { return refs[file+":"+strconv.Itoa(l)] })
			_, exact := s.Skipped[key]
			reason, skipped := skipReason(s.Skipped, file, c.blocks)
			switch {
			case cited && exact:
				report("%s is ported and skipped (%s)", key, reason)
			case cited:
				ported++
			case !skipped:
				report("%s %s is neither ported nor skipped", key, c.name)
			case s.Reasons[reason] == "":
				report("%s: unknown reason %q", key, reason)
			default:
				byFile[file] = append(byFile[file], row{c, reason})
				counts[reason]++
				skippedN++
			}
		}
	}
	for _, file := range s.Files {
		seen[file] = true
	}
	for _, key := range slices.Sorted(maps.Keys(s.Skipped)) {
		if !seen[key] {
			report("skipped %s is not a case, a block or an owned file", key)
		}
	}
	fmt.Printf("%s: %d cases, %d ported, %d skipped, %d uncovered\n", module, total, ported, skippedN, total-ported-skippedN)
	if !write {
		return ok, nil
	}
	var w strings.Builder
	fmt.Fprintf(&w, "# Unported spec cases\n\nSpec cases of %s that are not ported, each with its reason. Paths are relative to the upstream `spec/` directory. Ported cases carry a `source:` reference in the tests; together the two lists cover every case of the files in `testdata/specs.json` (checked by `go run ./tools/speccov`).\n\n", s.Title)
	fmt.Fprintf(&w, "Cases: %d. Ported: %d. Unported: %d.\n\n## Reasons\n\n", total, ported, total-ported)
	reasons := slices.SortedFunc(maps.Keys(s.Reasons), func(a, b string) int {
		if d := counts[b] - counts[a]; d != 0 {
			return d
		}
		return strings.Compare(a, b)
	})
	for _, r := range reasons {
		if counts[r] > 0 {
			fmt.Fprintf(&w, "- **%s** (%d): %s\n", r, counts[r], s.Reasons[r])
		}
	}
	for _, n := range s.Notes {
		fmt.Fprintf(&w, "\n%s\n", n)
	}
	w.WriteString("\n## Cases\n")
	for _, file := range s.Files {
		rows := byFile[file]
		if len(rows) == 0 {
			continue
		}
		fmt.Fprintf(&w, "\n### %s\n\n| Line | Case | Reason |\n| ---: | :--- | :--- |\n", file)
		for _, r := range rows {
			fmt.Fprintf(&w, "| %d | %s | %s |\n", r.c.line, r.c.name, r.reason)
		}
	}
	return ok, os.WriteFile(filepath.Join(module, "SKIPPED.md"), []byte(w.String()), 0o644)
}
