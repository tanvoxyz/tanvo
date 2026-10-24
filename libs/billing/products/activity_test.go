package products_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/products"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
)

// newLoggedFixture returns a fixture whose later changes record activity
// logs.
func newLoggedFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.h.LogActivities(auditlog.Actor{Source: auditlog.ActivitySourceAPI})
	return f
}

func logged(t auditlog.ActivityType, res auditlog.ResourceType, id uuid.UUID, changes ...string) billingtest.ActivityWant {
	return billingtest.ActivityWant{Type: t, ResourceType: res, ResourceID: id, Changes: changes}
}

// source: spec/services/product_categories/create_service_spec.rb:31,
// update_service_spec.rb:51, destroy_service_spec.rb:28-29,
// spec/services/products/create_service_spec.rb:37,
// update_service_spec.rb:59
func TestCategoryAndProductActivityLogs(t *testing.T) {
	t.Parallel()
	f := newLoggedFixture(t)
	ctx, org := t.Context(), f.h.OrgID()
	c, err := f.s.CreateCategory(ctx, org, products.CategoryInput{Code: "storage", Name: "Storage"})
	if err != nil {
		t.Fatalf("CreateCategory() error = %v", err)
	}
	if _, err := f.s.UpdateCategory(ctx, org, c.ID, products.CategoryUpdate{Name: core.Some("Object storage")}); err != nil {
		t.Fatalf("UpdateCategory() error = %v", err)
	}
	p, err := f.s.CreateProduct(ctx, org, products.ProductInput{Code: "disk", Name: "Disk", ProductType: catalog.ProductTypeFixed, Category: refs.ByID(c.ID)})
	if err != nil {
		t.Fatalf("CreateProduct() error = %v", err)
	}
	if _, err := f.s.UpdateProduct(ctx, org, p.ID, products.ProductUpdate{Name: core.Some("Block disk")}); err != nil {
		t.Fatalf("UpdateProduct() error = %v", err)
	}
	// Deleting the category deletes its product with its own log.
	if _, err := f.s.DeleteCategory(ctx, org, c.ID); err != nil {
		t.Fatalf("DeleteCategory() error = %v", err)
	}
	cid, pid := c.ID.UUID(), p.ID.UUID()
	f.h.WantActivities(
		logged(auditlog.ActivityProductCategoryCreated, auditlog.ResourceProductCategory, cid),
		logged(auditlog.ActivityProductCategoryUpdated, auditlog.ResourceProductCategory, cid, "name"),
		logged(auditlog.ActivityProductCreated, auditlog.ResourceProduct, pid),
		logged(auditlog.ActivityProductUpdated, auditlog.ResourceProduct, pid, "name"),
		logged(auditlog.ActivityProductDeleted, auditlog.ResourceProduct, pid),
		logged(auditlog.ActivityProductCategoryDeleted, auditlog.ResourceProductCategory, cid),
	)
}

// source: spec/services/products/destroy_service_spec.rb:38
func TestProductDeleteActivityLog(t *testing.T) {
	t.Parallel()
	f := newLoggedFixture(t)
	if _, err := f.s.DeleteProduct(t.Context(), f.h.OrgID(), f.product.ID); err != nil {
		t.Fatalf("DeleteProduct() error = %v", err)
	}
	f.h.WantActivities(logged(auditlog.ActivityProductDeleted, auditlog.ResourceProduct, f.product.ID.UUID()))
}

// source: spec/services/product_filters/create_service_spec.rb:233,
// update_service_spec.rb:145, destroy_service_spec.rb:20
func TestFilterActivityLogs(t *testing.T) {
	t.Parallel()
	f := newLoggedFixture(t)
	ctx, org := t.Context(), f.h.OrgID()
	pf := f.filter(t, "us", "us")
	if _, err := f.s.UpdateFilter(ctx, org, f.product.ID, pf.ID, products.FilterUpdate{Name: core.Some("United States")}); err != nil {
		t.Fatalf("UpdateFilter() error = %v", err)
	}
	if _, err := f.s.DeleteFilter(ctx, org, f.product.ID, pf.ID); err != nil {
		t.Fatalf("DeleteFilter() error = %v", err)
	}
	id := pf.ID.UUID()
	f.h.WantActivities(
		logged(auditlog.ActivityProductFilterCreated, auditlog.ResourceProductFilter, id),
		logged(auditlog.ActivityProductFilterUpdated, auditlog.ResourceProductFilter, id, "name"),
		logged(auditlog.ActivityProductFilterDeleted, auditlog.ResourceProductFilter, id),
	)
}

// The rates of a card are logged as updates of the card, with its rates
// in the changes; the rates created with the card log nothing of their
// own.
//
// source: spec/services/rate_cards/create_service_spec.rb:89,128,
// update_service_spec.rb:79, destroy_service_spec.rb:25,
// spec/services/rate_card_rates/create_service_spec.rb:73,
// update_service_spec.rb:26, destroy_service_spec.rb:23
func TestRateCardActivityLogs(t *testing.T) {
	t.Parallel()
	f := newLoggedFixture(t)
	ctx, org := t.Context(), f.h.OrgID()
	c := f.card(t, "api-usd", func(in *products.RateCardInput) {
		in.Rates = []products.RateInput{standard(t, "first", now)}
	})
	if _, err := f.s.UpdateRateCard(ctx, org, c.ID, products.RateCardUpdate{Name: core.Some("API (USD)")}); err != nil {
		t.Fatalf("UpdateRateCard() error = %v", err)
	}
	next, err := f.s.CreateRate(ctx, org, refs.ByID(c.ID), standard(t, "next", now.AddDate(0, 0, 2)))
	if err != nil {
		t.Fatalf("CreateRate() error = %v", err)
	}
	if _, err := f.s.UpdateRate(ctx, org, refs.ByID(c.ID), refs.ByID(next.ID), products.RateUpdate{MinAmount: core.Some(int64(100))}); err != nil {
		t.Fatalf("UpdateRate() error = %v", err)
	}
	if _, err := f.s.DeleteRate(ctx, org, refs.ByID(c.ID), refs.ByID(next.ID)); err != nil {
		t.Fatalf("DeleteRate() error = %v", err)
	}
	if _, err := f.s.DeleteRateCard(ctx, org, c.ID); err != nil {
		t.Fatalf("DeleteRateCard() error = %v", err)
	}
	id := c.ID.UUID()
	f.h.WantActivities(
		logged(auditlog.ActivityRateCardCreated, auditlog.ResourceRateCard, id),
		logged(auditlog.ActivityRateCardUpdated, auditlog.ResourceRateCard, id, "name"),
		logged(auditlog.ActivityRateCardUpdated, auditlog.ResourceRateCard, id, "rates"),
		logged(auditlog.ActivityRateCardUpdated, auditlog.ResourceRateCard, id, "rates"),
		logged(auditlog.ActivityRateCardUpdated, auditlog.ResourceRateCard, id, "rates"),
		logged(auditlog.ActivityRateCardDeleted, auditlog.ResourceRateCard, id),
	)
}

// A change that fails logs nothing.
func TestActivityLogsFailure(t *testing.T) {
	t.Parallel()
	f := newLoggedFixture(t)
	ctx, org := t.Context(), f.h.OrgID()
	if _, err := f.s.CreateCategory(ctx, org, products.CategoryInput{Code: f.category.Code, Name: "Again"}); err == nil {
		t.Fatal("CreateCategory() error = nil, want an error")
	}
	c := f.card(t, "api-usd")
	f.attach(t, c)
	if _, err := f.s.DeleteRateCard(ctx, org, c.ID); err == nil {
		t.Fatal("DeleteRateCard() error = nil, want an error")
	}
	f.h.WantActivities(logged(auditlog.ActivityRateCardCreated, auditlog.ResourceRateCard, c.ID.UUID()))
	if got := f.h.Activities(auditlog.ActivityProductCategoryCreated, auditlog.ActivityRateCardDeleted); len(got) != 0 {
		t.Errorf("logs: got = %d, want = 0", len(got))
	}
}
