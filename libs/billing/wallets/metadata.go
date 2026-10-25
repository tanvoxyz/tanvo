package wallets

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// DeleteMetadataKey removes key from the metadata of wallet id and returns
// the metadata left. Only the metadata changes: no webhook is sent, as lago
// Metadata::DeleteItemKeyService updates the metadata item alone. A wallet
// without metadata is metadata not found
// (app/controllers/concerns/wallet_metadata_actions.rb:37).
func (s *Service) DeleteMetadataKey(ctx context.Context, org core.OrganizationID, id core.WalletID, key string) (core.Metadata, error) {
	var out core.Metadata
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		w, err := lockWallet(ctx, r, org, id)
		if err != nil {
			return err
		}
		if len(w.Metadata) == 0 {
			return core.NotFound("metadata")
		}
		out = w.Metadata.Without(key)
		if len(out) == len(w.Metadata) {
			return nil
		}
		w.Metadata = out
		if err := r.Wallets.Update(ctx, w); err != nil {
			return fmt.Errorf("update wallet %s: %w", w.ID, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
