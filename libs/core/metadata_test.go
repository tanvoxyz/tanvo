package core_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
)

func items(kv ...string) core.Metadata {
	var m core.Metadata
	for i := 0; i+1 < len(kv); i += 2 {
		m = append(m, core.MetadataItem{Key: kv[i], Value: kv[i+1]})
	}
	return m
}

// TestItemMetadataLimits ports the value checks of Metadata::ItemMetadata.
func TestItemMetadataLimits(t *testing.T) {
	many := make([]string, 0, 102)
	for i := range 51 {
		many = append(many, "k"+strings.Repeat("x", i), "v")
	}
	tests := []struct {
		name  string
		m     core.Metadata
		field string
	}{
		// source: spec/models/metadata/item_metadata_spec.rb:58
		{name: "valid", m: items("foo", "bar")},
		// source: spec/models/metadata/item_metadata_spec.rb:31
		{name: "empty", m: nil},
		// source: spec/models/metadata/item_metadata_spec.rb:76
		{name: "more than 50 keys", m: items(many...), field: "items"},
		// source: spec/models/metadata/item_metadata_spec.rb:93
		{name: "key above 100 characters", m: items(strings.Repeat("k", 101), "v"), field: "items[0].key"},
		{name: "key of 100 characters", m: items(strings.Repeat("k", 100), "v")},
		// source: spec/models/metadata/item_metadata_spec.rb:120
		{name: "empty value", m: items("foo", "")},
		// source: spec/models/metadata/item_metadata_spec.rb:128
		{name: "value above 255 characters", m: items("foo", strings.Repeat("v", 256)), field: "items[0].value"},
		{name: "value of 255 characters", m: items("foo", strings.Repeat("v", 255))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.m.Validate(core.ItemMetadataLimits())
			var ve *core.ValidationError
			switch {
			case tt.field == "" && err != nil:
				t.Fatalf("Validate() got = %v, want = nil", err)
			case tt.field == "":
			case !errors.As(err, &ve) || !slices.ContainsFunc(ve.Errors, func(e core.FieldError) bool { return e.Field == tt.field }):
				t.Errorf("Validate() got = %v, want = an error on %s", err, tt.field)
			}
		})
	}
}

// TestMetadataMergeWithout ports the merges of Metadata::UpdateItemService
// and the key deletion of Metadata::DeleteItemKeyService.
func TestMetadataMergeWithout(t *testing.T) {
	existing := items("foo", "bar", "baz", "qux")
	tests := []struct {
		name string
		got  core.Metadata
		want core.Metadata
	}{
		// source: spec/services/metadata/update_item_service_spec.rb:138
		{name: "merge into none", got: core.Metadata(nil).Merge(items("foo", "bar", "baz", "qux")), want: items("foo", "bar", "baz", "qux")},
		// source: spec/services/metadata/update_item_service_spec.rb:156
		{name: "merge new keys", got: items("foo", "bar").Merge(items("baz", "qux")), want: items("foo", "bar", "baz", "qux")},
		// source: spec/services/metadata/update_item_service_spec.rb:196
		{name: "merge overwrites a key", got: existing.Merge(items("foo", "new")), want: items("foo", "new", "baz", "qux")},
		// source: spec/services/metadata/update_item_service_spec.rb:97
		{name: "merge nothing", got: existing.Merge(nil), want: existing},
		// source: spec/services/metadata/update_item_service_spec.rb:248
		{name: "merge an empty string", got: existing.Merge(items("foo", "")), want: items("foo", "", "baz", "qux")},
		// source: spec/services/metadata/update_item_service_spec.rb:277
		{name: "merge the same value", got: existing.Merge(items("foo", "bar")), want: existing},
		// source: spec/services/metadata/delete_item_key_service_spec.rb:16
		{name: "delete a key", got: existing.Without("foo"), want: items("baz", "qux")},
		// source: spec/services/metadata/delete_item_key_service_spec.rb:28
		{name: "delete a missing key", got: existing.Without("nope"), want: existing},
		// source: spec/services/metadata/delete_item_key_service_spec.rb:52
		{name: "delete the last key", got: items("foo", "bar").Without("foo"), want: core.Metadata{}},
		// source: spec/services/metadata/delete_item_key_service_spec.rb:76
		{name: "delete a key with an empty value", got: items("foo", "", "baz", "qux").Without("foo"), want: items("baz", "qux")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !slices.Equal(tt.got, tt.want) {
				t.Errorf("got = %v, want = %v", tt.got, tt.want)
			}
		})
	}
	if existing.Without("foo"); len(existing) != 2 || existing[0].Key != "foo" {
		t.Errorf("Without changed its receiver: got = %v", existing)
	}
}

// TestUpdateItemMetadata ports the table of Metadata::UpdateItemService:
// nil is no metadata item, a non-nil empty list an item without keys.
func TestUpdateItemMetadata(t *testing.T) {
	existing := items("old", "value")
	empty := core.Metadata{}
	pairs := items("foo", "bar", "baz", "qux")
	tests := []struct {
		name    string
		old     core.Metadata
		value   core.Metadata
		partial bool
		want    core.Metadata
	}{
		// source: spec/services/metadata/update_item_service_spec.rb:26
		{name: "nil partial without metadata", value: nil, partial: true, want: nil},
		// source: spec/services/metadata/update_item_service_spec.rb:41
		{name: "nil partial with metadata", old: existing, value: nil, partial: true, want: existing},
		// source: spec/services/metadata/update_item_service_spec.rb:54
		{name: "nil without metadata", value: nil, want: nil},
		// source: spec/services/metadata/update_item_service_spec.rb:68
		{name: "nil deletes metadata", old: existing, value: nil, want: nil},
		// source: spec/services/metadata/update_item_service_spec.rb:82
		{name: "empty partial without metadata", value: empty, partial: true, want: nil},
		// source: spec/services/metadata/update_item_service_spec.rb:97
		{name: "empty partial keeps metadata", old: existing, value: empty, partial: true, want: existing},
		// source: spec/services/metadata/update_item_service_spec.rb:110
		{name: "empty creates an empty item", value: empty, want: empty},
		// source: spec/services/metadata/update_item_service_spec.rb:125
		{name: "empty replaces with an empty item", old: existing, value: empty, want: empty},
		// source: spec/services/metadata/update_item_service_spec.rb:138
		{name: "partial creates", value: pairs, partial: true, want: pairs},
		// source: spec/services/metadata/update_item_service_spec.rb:156
		{name: "partial merges", old: existing, value: pairs, partial: true, want: items("old", "value", "foo", "bar", "baz", "qux")},
		// source: spec/services/metadata/update_item_service_spec.rb:169
		{name: "replace creates", value: items("foo", "bar"), want: items("foo", "bar")},
		// source: spec/services/metadata/update_item_service_spec.rb:181
		{name: "replace replaces", old: existing, value: items("foo", "bar"), want: items("foo", "bar")},
		{name: "partial into an empty item", old: empty, value: items("foo", "bar"), partial: true, want: items("foo", "bar")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := core.UpdateItemMetadata(tt.old, tt.value, tt.partial)
			if !core.SameItemMetadata(got, tt.want) {
				t.Errorf("got = %#v, want = %#v", got, tt.want)
			}
		})
	}
	// source: spec/services/metadata/delete_item_key_service_spec.rb:52
	if got := items("foo", "bar").Without("foo"); got == nil || len(got) != 0 {
		t.Errorf("deleting the last key: got = %#v, want = an empty item", got)
	}
	if core.SameItemMetadata(nil, empty) {
		t.Error("SameItemMetadata(nil, empty) = true, want = false")
	}
}
