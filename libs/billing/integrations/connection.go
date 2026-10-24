package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

var _ integration.ConnectionStore = (*Service)(nil)

// Connection returns the integration i as its adapter sees it, with its
// credentials opened.
func (s *Service) Connection(ctx context.Context, i *integration.Integration) (integration.Connection, error) {
	secrets, err := s.secrets(ctx, i)
	if err != nil {
		return integration.Connection{}, err
	}
	conn := integration.Connection{
		ID: i.ID, OrganizationID: i.OrganizationID, Code: i.Code, Provider: i.Provider,
		Settings: i.Settings,
	}
	if len(secrets) > 0 {
		if conn.Secrets, err = json.Marshal(secrets); err != nil {
			return integration.Connection{}, fmt.Errorf("integration %s secrets: %w", i.ID, err)
		}
	}
	return conn, nil
}

// UpdateConnection merges the update u of an adapter into the stored
// integration conn.ID: an OAuth token refreshed, a HubSpot object
// deployed, an Avalara company resolved. An empty value removes the key.
func (s *Service) UpdateConnection(ctx context.Context, conn integration.Connection, u integration.ConnectionUpdate) error {
	if u.Empty() {
		return nil
	}
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		i, err := r.Integrations.Get(ctx, conn.OrganizationID, conn.ID)
		if err != nil {
			return notFound(err)
		}
		settings, err := Settings(i)
		if err != nil {
			return err
		}
		secrets, err := s.secrets(ctx, i)
		if err != nil {
			return err
		}
		merge(settings, u.Settings)
		merge(secrets, u.Secrets)
		if err := s.store(ctx, i, settings, secrets); err != nil {
			return err
		}
		i.UpdatedAt = s.d.Now()
		if err := r.Integrations.Update(ctx, i); err != nil {
			return fmt.Errorf("update integration %s: %w", i.ID, err)
		}
		return nil
	})
}

// merge sets the values of src into dst, removing the keys of empty
// values.
func merge(dst, src map[string]string) {
	for k, v := range maps.All(src) {
		if v == "" {
			delete(dst, k)
		} else {
			dst[k] = v
		}
	}
}

// loadConnection loads the integration id and opens its connection.
func (s *Service) loadConnection(ctx context.Context, r port.Repos, org core.OrganizationID, id core.IntegrationID) (*integration.Integration, integration.Connection, error) {
	i, err := get(ctx, r, org, id)
	if err != nil {
		return nil, integration.Connection{}, err
	}
	conn, err := s.Connection(ctx, i)
	return i, conn, err
}
