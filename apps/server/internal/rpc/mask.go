package rpc

import (
	"slices"

	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// UpdatePaths returns the fields an update writes (AIP-134): the
// update_mask paths, each one of allowed ("*" is all of them), or, without
// a mask, the allowed fields that set reports present in the request's
// resource. An unknown path fails with an InvalidArgument violation on
// update_mask.
func UpdatePaths(mask *fieldmaskpb.FieldMask, allowed []string, set func(path string) bool) ([]string, error) {
	if len(mask.GetPaths()) == 0 {
		var out []string
		for _, f := range allowed {
			if set(f) {
				out = append(out, f)
			}
		}
		return out, nil
	}
	if slices.Contains(mask.GetPaths(), "*") {
		return allowed, nil
	}
	for _, path := range mask.GetPaths() {
		if !slices.Contains(allowed, path) {
			return nil, InvalidArgument("invalid update_mask", []FieldViolation{{
				Field: "update_mask", Reason: core.CodeInvalid, Description: "unknown field " + path,
			}})
		}
	}
	return mask.GetPaths(), nil
}
