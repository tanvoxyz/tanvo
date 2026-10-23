package validators

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// structuralFile is a testdata file of ported structural validator specs.
type structuralFile struct {
	Source string           `json:"source"`
	Family string           `json:"family"`
	Base   json.RawMessage  `json:"base"`
	Cases  []structuralCase `json:"cases"`
}

type structuralCase struct {
	Line   int       `json:"line"`
	Name   string    `json:"name"`
	Scope  string    `json:"scope"`
	Patch  []patchOp `json:"patch"`
	Errors []string  `json:"errors"`
}

// patchOp edits the base billing items: set the value at a JSON pointer
// ("" is the root), delete a member, or append a deep copy of an element.
type patchOp struct {
	Set    *string         `json:"set"`
	Value  json.RawMessage `json:"value"`
	Delete *string         `json:"delete"`
	Copy   *string         `json:"copy"`
	To     string          `json:"to"`
}

func TestStructural(t *testing.T) {
	for _, name := range []string{"one_off_structural.json", "subscription_creation_structural.json"} {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		var f structuralFile
		if err := json.Unmarshal(data, &f); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, c := range f.Cases {
			t.Run(f.Family+"/"+strconv.Itoa(c.Line)+"/"+c.Name, func(t *testing.T) {
				t.Logf("source: %s:%d", f.Source, c.Line)
				root, err := decodeValue(f.Base)
				if err != nil {
					t.Fatal(err)
				}
				for _, op := range c.Patch {
					root = applyPatch(t, root, op)
				}
				var raw []byte
				if root != nil {
					raw = encodeValue(root)
				}
				items, err := billingItems(raw)
				if err != nil {
					t.Fatal(err)
				}
				scope := ScopeUpdate
				if c.Scope == "approve" {
					scope = ScopeApprove
				}
				got := fieldErrors(t, validateStructure(f.Family, scope, items))
				if !slices.Equal(got, c.Errors) {
					t.Errorf("errors: got = %q, want = %q", got, c.Errors)
				}
			})
		}
	}
}

// fieldErrors renders a validation error as "field: code" lines.
func fieldErrors(t *testing.T, err error) []string {
	t.Helper()
	out := []string{}
	if err == nil {
		return out
	}
	var ve *core.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error: got = %v, want = *core.ValidationError", err)
	}
	for _, e := range ve.Errors {
		out = append(out, e.Field+": "+e.Code)
	}
	return out
}

func pointerTokens(p string) []string {
	if p == "" {
		return nil
	}
	toks := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, tok := range toks {
		toks[i] = strings.NewReplacer("~1", "/", "~0", "~").Replace(tok)
	}
	return toks
}

// resolve returns the parent of the value at pointer p and its last token.
func resolve(t *testing.T, root *value, p string) (*value, string) {
	t.Helper()
	toks := pointerTokens(p)
	cur := root
	for _, tok := range toks[:len(toks)-1] {
		cur = child(t, cur, tok)
	}
	return cur, toks[len(toks)-1]
}

func child(t *testing.T, v *value, tok string) *value {
	t.Helper()
	switch v.kind {
	case kindObject:
		if c, ok := v.vals[tok]; ok {
			return c
		}
	case kindArray:
		if i, err := strconv.Atoi(tok); err == nil && i >= 0 && i < len(v.arr) {
			return v.arr[i]
		}
	}
	t.Fatalf("patch: no member %q", tok)
	return nil
}

func applyPatch(t *testing.T, root *value, op patchOp) *value {
	t.Helper()
	switch {
	case op.Set != nil:
		var nv *value
		if string(op.Value) != "null" || *op.Set != "" {
			var err error
			if nv, err = decodeValue(op.Value); err != nil {
				t.Fatal(err)
			}
		}
		if *op.Set == "" {
			return nv
		}
		parent, tok := resolve(t, root, *op.Set)
		setChild(t, parent, tok, nv)
	case op.Delete != nil:
		parent, tok := resolve(t, root, *op.Delete)
		if parent.kind != kindObject {
			t.Fatalf("patch: delete %q from a non-object", *op.Delete)
		}
		delete(parent.vals, tok)
		parent.keys = slices.DeleteFunc(parent.keys, func(k string) bool { return k == tok })
	case op.Copy != nil:
		src, tok := resolve(t, root, *op.Copy)
		dup, err := decodeValue(encodeValue(child(t, src, tok)))
		if err != nil {
			t.Fatal(err)
		}
		parent, to := resolve(t, root, op.To)
		setChild(t, parent, to, dup)
	default:
		t.Fatal("patch: empty operation")
	}
	return root
}

func setChild(t *testing.T, parent *value, tok string, nv *value) {
	t.Helper()
	switch parent.kind {
	case kindObject:
		parent.set(tok, nv)
	case kindArray:
		i, err := strconv.Atoi(tok)
		switch {
		case err != nil || i < 0 || i > len(parent.arr):
			t.Fatalf("patch: bad index %q", tok)
		case i == len(parent.arr):
			parent.arr = append(parent.arr, nv)
		default:
			parent.arr[i] = nv
		}
	default:
		t.Fatalf("patch: set %q in a scalar", tok)
	}
}

// encodeValue encodes v keeping the order of object keys.
func encodeValue(v *value) []byte {
	var sb strings.Builder
	var enc func(*value)
	enc = func(v *value) {
		switch v.kind {
		case kindNull:
			sb.WriteString("null")
		case kindBool:
			sb.WriteString(strconv.FormatBool(v.b))
		case kindNumber:
			sb.WriteString(v.s)
		case kindString:
			b, _ := json.Marshal(v.s)
			sb.Write(b)
		case kindArray:
			sb.WriteByte('[')
			for i, e := range v.arr {
				if i > 0 {
					sb.WriteByte(',')
				}
				enc(e)
			}
			sb.WriteByte(']')
		case kindObject:
			sb.WriteByte('{')
			for i, k := range v.keys {
				if i > 0 {
					sb.WriteByte(',')
				}
				b, _ := json.Marshal(k)
				sb.Write(b)
				sb.WriteByte(':')
				enc(v.vals[k])
			}
			sb.WriteByte('}')
		}
	}
	enc(v)
	return []byte(sb.String())
}
