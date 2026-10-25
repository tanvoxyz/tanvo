package plans

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// DeleteMetadataKey removes key from the metadata of plan id and returns
// the metadata left. Only the metadata changes: no webhook is sent, as lago
// Metadata::DeleteItemKeyService updates the metadata item alone. An
// override plan is plan not found, a plan without a metadata item is
// metadata not found, while emptied metadata answers an empty list
// (app/controllers/api/v1/plans/metadata_controller.rb:38).
func (s *Service) DeleteMetadataKey(ctx context.Context, org core.OrganizationID, id core.PlanID, key string) (core.Metadata, error) {
	var out core.Metadata
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := kept(ctx, r, org, id)
		if err != nil {
			return err
		}
		if p.IsChild() {
			return core.NotFound(Resource)
		}
		if p.Metadata == nil {
			return core.NotFound("metadata")
		}
		out = p.Metadata.Without(key)
		if len(out) == len(p.Metadata) {
			return nil
		}
		p.Metadata = out
		if err := r.Plans.Update(ctx, p); err != nil {
			return fmt.Errorf("update plan %s: %w", id, err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("delete metadata key of plan %s: %w", id, err)
	}
	return out, nil
}

// UpdateMetadata replaces the metadata of plan id with md, or with merge
// merges md into it key by key, as [Service.Update] does with only the
// metadata set, and returns the plan's metadata (lago
// Api::V1::Plans::MetadataController). A nil md without merge removes the
// metadata and an empty one leaves an empty item. Override plans are plan not found: the endpoints find the
// plans by code (app/controllers/api/v1/plans/base_controller.rb:12-14).
func (s *Service) UpdateMetadata(ctx context.Context, org core.OrganizationID, id core.PlanID, md core.Metadata, merge bool) (core.Metadata, error) {
	p, err := s.Get(ctx, org, id)
	if err != nil {
		return nil, err
	}
	if p.IsChild() || p.Deleted() {
		return nil, core.NotFound(Resource)
	}
	p, err = s.Update(ctx, org, id, UpdateInput{Metadata: core.Some(md), MergeMetadata: merge})
	if err != nil {
		return nil, err
	}
	return p.Metadata, nil
}
