package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestCases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x_spec.rb")
	src := `RSpec.describe Foo do
  describe "#call" do
    context "when a" do
      it "does x" do
      end

      it_behaves_like "a thing"
    end

    it { is_expected.to be_ok }
  end
end
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := cases(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []specCase{
		{line: 4, name: `when a > "does x"`, blocks: []int{1, 2, 3, 4}},
		{line: 7, name: `when a > it_behaves_like a thing`, blocks: []int{1, 2, 3, 7}},
		{line: 10, name: `it { is_expected.to be_ok }`, blocks: []int{1, 2, 10}},
	}
	if len(got) != len(want) {
		t.Fatalf("cases: got = %+v, want = %+v", got, want)
	}
	for i := range want {
		if got[i].line != want[i].line || got[i].name != want[i].name || !slices.Equal(got[i].blocks, want[i].blocks) {
			t.Errorf("case %d: got = %+v, want = %+v", i, got[i], want[i])
		}
	}
}

func TestCitations(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a_test.go"), []byte(`// source: spec/services/x_spec.rb:4, 9-10
t.Log("source: spec/services/y_spec.rb:7")`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := citations(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"services/x_spec.rb:4", "services/x_spec.rb:9", "services/x_spec.rb:10", "services/y_spec.rb:7"} {
		if !got[k] {
			t.Errorf("%s: got = not cited, want = cited", k)
		}
	}
	if len(got) != 4 {
		t.Errorf("citations: got = %v, want = 4", got)
	}
}

func TestSkipReason(t *testing.T) {
	skipped := map[string]string{"a_spec.rb": "file", "a_spec.rb:3": "block", "a_spec.rb:4": "case"}
	tests := []struct {
		name   string
		blocks []int
		want   string
	}{
		{"own line", []int{1, 3, 4}, "case"},
		{"enclosing block", []int{1, 3, 5}, "block"},
		{"file", []int{1, 7}, "file"},
	}
	for _, tt := range tests {
		if got, ok := skipReason(skipped, "a_spec.rb", tt.blocks); !ok || got != tt.want {
			t.Errorf("%s: got = %q, want = %q", tt.name, got, tt.want)
		}
	}
	if got, ok := skipReason(map[string]string{}, "a_spec.rb", []int{1}); ok {
		t.Errorf("none: got = %q, want = not skipped", got)
	}
}
