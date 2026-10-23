package documents

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// texts flattens a layout into its printed texts, in reading order.
func (l *layout) texts() []string {
	out := []string{l.Title}
	for _, r := range l.Info {
		out = append(out, r[0], r[1])
	}
	for _, r := range l.Meta {
		out = append(out, r[0], r[1])
	}
	for _, p := range l.Parties {
		out = append(out, p.Title)
		out = append(out, p.Lines...)
	}
	out = append(out, l.Amount, l.AmountNote)
	for _, b := range l.Blocks {
		switch b.Kind {
		case blockHeading:
			out = append(out, b.Text)
		case blockTable:
			for _, r := range b.Rows {
				for _, c := range r.Cells {
					out = append(out, c.Text...)
					out = append(out, c.Sub...)
				}
			}
		case blockTotals:
			for _, r := range b.Totals {
				out = append(out, r.Label, r.Value)
			}
		case blockText:
			out = append(out, b.Lines...)
		case blockSections:
			for _, s := range b.Sections {
				out = append(out, s.Title)
				out = append(out, s.Lines...)
			}
		}
	}
	out = append(out, l.Footer.Number)
	return nonEmpty(out)
}

// snapshotIgnored are snapshot texts Tanvo does not print: lago's
// "Powered by" logo and the comma between zipcode and city, which Tanvo
// prints in the same line ("94105, San Francisco").
var snapshotIgnored = map[string]bool{"Powered by": true, ",": true}

// lagoSnapshot reads the visible texts of a lago-api HTML snapshot ported
// to testdata/lago (see the files' source lines).
func lagoSnapshot(t *testing.T, name string) (source string, want []string) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "lago", name+".txt"))
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		if src, ok := strings.CutPrefix(line, "# source: "); ok {
			source = src
			continue
		}
		if strings.HasPrefix(line, "#") || snapshotIgnored[line] {
			continue
		}
		want = append(want, line)
	}
	if err := s.Err(); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	return source, want
}

// checkSnapshot expects every text of the lago snapshot name, in order, in
// the layout's texts (each snapshot text within one layout text or
// spanning several: the snapshot splits some lines into runs). skip lists
// snapshot texts Tanvo deliberately prints differently, with the reason
// in the test case.
func checkSnapshot(t *testing.T, l *layout, name string, skip ...string) {
	t.Helper()
	source, want := lagoSnapshot(t, name)
	t.Logf("source: %s", source)
	skipped := map[string]int{}
	for _, s := range skip {
		skipped[s]++
	}
	got := strings.Join(l.texts(), "\n")
	pos := 0
	for i, w := range want {
		if skipped[w] > 0 {
			skipped[w]--
			continue
		}
		j := indexRun(got[pos:], w)
		if j < 0 {
			context := got[pos:min(len(got), pos+300)]
			if strings.Contains(got, w) {
				t.Fatalf("snapshot text %d %q: got = out of order, want = after position %d\nnext texts:\n%s", i, w, pos, context)
			}
			t.Fatalf("snapshot text %d %q: got = missing, want = present\nnext texts:\n%s", i, w, context)
		}
		pos += j + len(w)
	}
	for s, n := range skipped {
		if n > 0 {
			t.Errorf("skip %q: got = not in snapshot, want = a snapshot text", s)
		}
	}
}

// indexRun is the index of the first occurrence of run in s that starts
// and ends a line or a comma- or space-separated part of one; -1 if none.
func indexRun(s, run string) int {
	for off := 0; ; {
		j := strings.Index(s[off:], run)
		if j < 0 {
			return -1
		}
		j += off
		end := j + len(run)
		if (j == 0 || strings.ContainsRune("\n ,", rune(s[j-1]))) && (end == len(s) || strings.ContainsRune("\n ,", rune(s[end]))) {
			return j
		}
		off = j + 1
	}
}
