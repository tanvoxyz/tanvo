package memstore

import (
	"slices"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
)

// detachTax removes the tax id from every owner, as the cascading foreign
// keys of the store's tax join tables do.
func (st *state) detachTax(id core.TaxID) {
	detachTaxFrom(st.organization.billingEntities, func(b *organization.BillingEntity) *[]core.TaxID { return &b.TaxIDs }, id)
	detachTaxFrom(st.customer.customers, func(c *customer.Customer) *[]core.TaxID { return &c.TaxIDs }, id)
	detachTaxFrom(st.catalog.plans, func(p *catalog.Plan) *[]core.TaxID { return &p.TaxIDs }, id)
	detachTaxFrom(st.catalog.charges, func(c *catalog.Charge) *[]core.TaxID { return &c.TaxIDs }, id)
	detachTaxFrom(st.catalog.fixedCharges, func(f *catalog.FixedCharge) *[]core.TaxID { return &f.TaxIDs }, id)
	detachTaxFrom(st.catalog.addOns, func(a *catalog.AddOn) *[]core.TaxID { return &a.TaxIDs }, id)
	detachTaxFrom(st.catalogPart2.commitments, func(c *catalog.Commitment) *[]core.TaxID { return &c.TaxIDs }, id)
	detachTaxFrom(st.productCatalog.rateCards, func(c *catalog.RateCard) *[]core.TaxID { return &c.TaxIDs }, id)
}

// detachTaxFrom removes the tax id from the tax IDs (by ids) of the rows of
// t; the rows are replaced, never mutated in place.
func detachTaxFrom[K interface {
	comparable
	Compare(K) int
	String() string
}, V any](t *table[K, V], ids func(*V) *[]core.TaxID, id core.TaxID,
) {
	for k, v := range t.rows {
		if !slices.Contains(*ids(&v), id) {
			continue
		}
		c := t.clone(v)
		p := ids(&c)
		*p = slices.DeleteFunc(slices.Clone(*p), func(x core.TaxID) bool { return x == id })
		t.rows[k] = c
	}
}
