package integrations

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// itemMapper resolves the external items of the fees of a document for
// one integration and billing entity (lago
// Integrations::Aggregator::BasePayload), caching the charges and fixed
// charges it loads. It is bound to one transaction.
type itemMapper struct {
	r           port.Repos
	org         core.OrganizationID
	be          core.BillingEntityID
	mappings    []*integration.Mapping
	collections []*integration.CollectionMapping
	charges     map[core.ChargeID]*catalog.Charge
	fixed       map[core.FixedChargeID]*catalog.FixedCharge
}

// newItemMapper loads the mappings of the integration id.
func newItemMapper(ctx context.Context, r port.Repos, org core.OrganizationID, id core.IntegrationID, be core.BillingEntityID) (*itemMapper, error) {
	ms, err := r.IntegrationMappings.ListByIntegration(ctx, org, id)
	if err != nil {
		return nil, fmt.Errorf("load mappings of integration %s: %w", id, err)
	}
	cs, err := r.IntegrationCollectionMappings.ListByType(ctx, org, id, "")
	if err != nil {
		return nil, fmt.Errorf("load collection mappings of integration %s: %w", id, err)
	}
	return &itemMapper{
		r: r, org: org, be: be, mappings: ms, collections: cs,
		charges: map[core.ChargeID]*catalog.Charge{}, fixed: map[core.FixedChargeID]*catalog.FixedCharge{},
	}, nil
}

// fee returns the external item of the fee f, or nil when nothing maps
// it: a charge fee maps as its billable metric, an add-on or fixed-charge
// fee as its add-on, a commitment fee as the minimum commitment, a
// subscription fee as the subscription fee; credit and product fees map
// to nothing (lago taxes/base_payload.rb:8-20 and
// accounting payloads, base_payload.rb:21-51).
func (m *itemMapper) fee(ctx context.Context, f *invoice.Fee) (*integration.ExternalItem, error) {
	switch {
	case f.Type == core.FeeTypeCharge:
		ch, err := m.charge(ctx, f.ChargeID)
		if err != nil {
			return nil, err
		}
		return m.object(integration.MappableTypeBillableMetric, ch.BillableMetricID.UUID()), nil
	case !f.AddOnID.IsZero() && f.Type != core.FeeTypeFixedCharge:
		return m.object(integration.MappableTypeAddOn, f.AddOnID.UUID()), nil
	case f.Type == core.FeeTypeFixedCharge:
		a, err := m.fixedChargeAddOn(ctx, f)
		if err != nil {
			return nil, err
		}
		return m.object(integration.MappableTypeAddOn, a.UUID()), nil
	case f.Type == core.FeeTypeCommitment:
		return m.collection(integration.CollectionMappingTypeMinimumCommitment), nil
	case f.Type == core.FeeTypeSubscription:
		return m.collection(integration.CollectionMappingTypeSubscriptionFee), nil
	default:
		return nil, nil
	}
}

// object resolves the mapping of an add-on or billable metric.
func (m *itemMapper) object(t integration.MappableType, id uuid.UUID) *integration.ExternalItem {
	it, err := integration.Resolve(integration.ResolveInput{
		Mappings: m.mappings, CollectionMappings: m.collections,
		MappableType: t, MappableID: id, BillingEntityID: m.be,
	})
	if err != nil {
		return nil
	}
	return &it
}

// collection resolves the collection mapping of type t.
func (m *itemMapper) collection(t integration.CollectionMappingType) *integration.ExternalItem {
	c := integration.LookupCollection(m.collections, m.be, t)
	if c == nil {
		return nil
	}
	it := c.Item()
	return &it
}

// itemID is lago Fee#item_id: the billable metric of a charge fee, the
// add-on of an add-on or fixed-charge fee, the wallet transaction of a
// credit fee, the product of a product fee, else the subscription
// (app/models/fee.rb:109-117).
func (m *itemMapper) itemID(ctx context.Context, f *invoice.Fee) (string, error) {
	switch f.Type {
	case core.FeeTypeCharge:
		ch, err := m.charge(ctx, f.ChargeID)
		if err != nil {
			return "", err
		}
		return ch.BillableMetricID.String(), nil
	case core.FeeTypeAddOn:
		return f.AddOnID.String(), nil
	case core.FeeTypeCredit:
		return f.WalletTransactionID.String(), nil
	case core.FeeTypeFixedCharge:
		a, err := m.fixedChargeAddOn(ctx, f)
		if err != nil {
			return "", err
		}
		return a.String(), nil
	case core.FeeTypeProduct:
		return f.ProductID.String(), nil
	default:
		return f.SubscriptionID.String(), nil
	}
}

func (m *itemMapper) charge(ctx context.Context, id core.ChargeID) (*catalog.Charge, error) {
	if ch, ok := m.charges[id]; ok {
		return ch, nil
	}
	ch, err := m.r.Charges.Get(ctx, m.org, id)
	if err != nil {
		return nil, fmt.Errorf("load charge %s: %w", id, err)
	}
	m.charges[id] = ch
	return ch, nil
}

// fixedChargeAddOn returns the add-on of a fixed-charge fee.
func (m *itemMapper) fixedChargeAddOn(ctx context.Context, f *invoice.Fee) (core.AddOnID, error) {
	if !f.AddOnID.IsZero() {
		return f.AddOnID, nil
	}
	fc, ok := m.fixed[f.FixedChargeID]
	if !ok {
		var err error
		if fc, err = m.r.FixedCharges.Get(ctx, m.org, f.FixedChargeID); err != nil {
			return core.AddOnID{}, fmt.Errorf("load fixed charge %s: %w", f.FixedChargeID, err)
		}
		m.fixed[f.FixedChargeID] = fc
	}
	return fc.AddOnID, nil
}
