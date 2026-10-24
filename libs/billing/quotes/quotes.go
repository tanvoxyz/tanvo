package quotes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/quotes/validators"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// VersionView is a quote version with what its API representation shows
// (lago V1::QuoteVersionSerializer).
type VersionView struct {
	// Version is the version.
	Version *quote.Version
	// BillingEntityCode is the code of its billing entity, if any.
	BillingEntityCode string
}

// QuoteView is a quote with what its API representation shows (lago
// V1::QuoteSerializer).
type QuoteView struct {
	// Quote is the quote.
	Quote *quote.Quote
	// Current is its latest version.
	Current *VersionView
	// Images are the URLs of its images, by image id; empty URLs without a
	// blob store.
	Images map[string]string
}

// CreateInput is a quote to create (lago Quotes::CreateService and
// GraphQL Mutations::Quotes::Create).
type CreateInput struct {
	// OrganizationID is the organization.
	OrganizationID core.OrganizationID
	// CustomerID is the customer the quote is for.
	CustomerID core.CustomerID
	// OrderType is what executing the order does.
	OrderType quote.OrderType
	// SubscriptionID is the amended subscription, required for amendments.
	SubscriptionID core.SubscriptionID
	// Owners are the members responsible for the quote; duplicates are
	// dropped.
	Owners []core.MembershipID
	// BillingItems are the quoted items of the first version, a JSON
	// object; nil for none.
	BillingItems json.RawMessage
	// Content is the document of the first version.
	Content *string
	// BillingEntityID is the issuer of the first version; zero follows the
	// amended subscription or the customer.
	BillingEntityID core.BillingEntityID
}

// Create creates a quote and its first draft version, and emits
// quote.created (lago Quotes::CreateService,
// app/services/quotes/create_service.rb:20-51). The deal currency is the
// amended subscription's plan currency, else the customer's, else the
// default currency of the issuing billing entity (create_service.rb:79-90).
func (s *Service) Create(ctx context.Context, in CreateInput) (*QuoteView, error) {
	org := in.OrganizationID
	var out *QuoteView
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := r.Customers.Get(ctx, org, in.CustomerID)
		if errors.Is(err, core.ErrNotFound) || (err == nil && c.Deleted()) {
			return core.NotFound("customer")
		} else if err != nil {
			return fmt.Errorf("load customer %s: %w", in.CustomerID, err)
		}
		if in.OrderType == quote.OrderTypeSubscriptionAmendment && in.SubscriptionID.IsZero() {
			return core.NotFound("subscription")
		}
		var plan *money.Currency
		if !in.SubscriptionID.IsZero() {
			sub, err := r.Subscriptions.Get(ctx, org, in.SubscriptionID)
			if errors.Is(err, core.ErrNotFound) || (err == nil && sub.CustomerID != c.ID) {
				return core.NotFound("subscription")
			} else if err != nil {
				return fmt.Errorf("load subscription %s: %w", in.SubscriptionID, err)
			}
			p, err := r.Plans.Get(ctx, org, sub.PlanID)
			if err != nil {
				return fmt.Errorf("load plan %s: %w", sub.PlanID, err)
			}
			plan = &p.Currency
		}
		owners := dedupOwners(in.Owners)
		if err := checkOwners(ctx, r, org, owners); err != nil {
			return err
		}

		now := s.d.Now()
		q := &quote.Quote{
			ID:             core.NewID[core.QuoteID](s.d.IDs),
			OrganizationID: org,
			CustomerID:     c.ID,
			OrderType:      in.OrderType,
			Owners:         owners,
			SubscriptionID: in.SubscriptionID,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		seq, err := r.Quotes.NextSequence(ctx, org)
		if err != nil {
			return fmt.Errorf("next quote number: %w", err)
		}
		q.AssignNumber(seq)
		if err := q.Validate(); err != nil {
			return err
		}
		if err := r.Quotes.Create(ctx, q); err != nil {
			return fmt.Errorf("create quote: %w", err)
		}

		v := quote.FirstVersion(core.NewID[core.QuoteVersionID](s.d.IDs), q, in.BillingItems, now)
		v.Content, v.BillingEntityID = in.Content, in.BillingEntityID
		if plan != nil {
			v.Currency = *plan
		} else if v.Currency, err = s.defaultCurrency(ctx, r, org, c.ID, c.Currency, in.BillingEntityID); err != nil {
			return err
		}
		if err := s.validateVersion(ctx, r, q, v, validators.ScopeUpdate); err != nil {
			return err
		}
		if err := r.QuoteVersions.Create(ctx, v); err != nil {
			return fmt.Errorf("create quote version: %w", err)
		}
		if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeQuoteCreated, "quote", v.ID.UUID(), quoteWithVersion{Quote: q, Version: v}); err != nil {
			return err
		}
		// source: app/services/quotes/create_service.rb:41
		if err := s.logQuote(ctx, r, auditlog.ActivityQuoteCreated, nil, q); err != nil {
			return err
		}
		out, err = s.quoteView(ctx, r, q)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("create quote: %w", err)
	}
	return out, nil
}

// defaultCurrency is the customer's currency, else the default currency
// of the issuing billing entity: be when it exists, the customer's
// otherwise (create_service.rb:79-90).
func (s *Service) defaultCurrency(ctx context.Context, r port.Repos, org core.OrganizationID, cust core.CustomerID, cur money.Currency, be core.BillingEntityID) (money.Currency, error) {
	if !cur.IsZero() {
		return cur, nil
	}
	if !be.IsZero() {
		e, err := r.BillingEntities.Get(ctx, org, be)
		if err == nil {
			return e.DefaultCurrency, nil
		}
		if !errors.Is(err, core.ErrNotFound) {
			return money.Currency{}, fmt.Errorf("load billing entity %s: %w", be, err)
		}
	}
	env, err := kernel.LoadCustomerEnv(ctx, r, org, cust, core.BillingEntityID{})
	if err != nil {
		return money.Currency{}, err
	}
	return env.BillingEntity.DefaultCurrency, nil
}

// validateVersion runs the billing items validators of scope on v.
func (s *Service) validateVersion(ctx context.Context, r port.Repos, q *quote.Quote, v *quote.Version, scope validators.Scope) error {
	if err := validators.Validate(ctx, r, q, v, scope, s.d.Now()); err != nil {
		return fmt.Errorf("validate quote version: %w", err)
	}
	return nil
}

// UpdateInput changes a quote (lago Quotes::UpdateService).
type UpdateInput struct {
	// Owners, when set, replace the owners.
	Owners core.Optional[[]core.MembershipID]
}

// Update changes the owners of quote id (lago Quotes::UpdateService,
// app/services/quotes/update_service.rb:26-72): every owner must be an
// active member of the organization.
func (s *Service) Update(ctx context.Context, org core.OrganizationID, id core.QuoteID, in UpdateInput) (*QuoteView, error) {
	var out *QuoteView
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		q, err := r.Quotes.Get(ctx, org, id)
		if err != nil {
			return notFound(err, "quote")
		}
		if in.Owners.Set {
			before, err := auditlog.ObjectOf(q)
			if err != nil {
				return err
			}
			owners := dedupOwners(in.Owners.Value)
			if err := checkOwners(ctx, r, org, owners); err != nil {
				return err
			}
			// sync_owners!: the kept owners keep their place, the new ones
			// follow.
			kept := slices.DeleteFunc(slices.Clone(q.Owners), func(o core.MembershipID) bool { return !slices.Contains(owners, o) })
			for _, o := range owners {
				if !slices.Contains(kept, o) {
					kept = append(kept, o)
				}
			}
			q.Owners = kept
			q.UpdatedAt = s.d.Now()
			if err := q.Validate(); err != nil {
				return err
			}
			if err := r.Quotes.Update(ctx, q); err != nil {
				return fmt.Errorf("update quote %s: %w", id, err)
			}
			// source: app/services/quotes/update_service.rb:13-17 (only
			// when owners are given)
			if err := s.logQuote(ctx, r, auditlog.ActivityQuoteUpdated, before, q); err != nil {
				return err
			}
		}
		out, err = s.quoteView(ctx, r, q)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("update quote %s: %w", id, err)
	}
	return out, nil
}

func dedupOwners(in []core.MembershipID) []core.MembershipID {
	out := make([]core.MembershipID, 0, len(in))
	for _, o := range in {
		if !slices.Contains(out, o) {
			out = append(out, o)
		}
	}
	return out
}

// checkOwners requires every owner to be an active member of org, failing
// with owners invalid (create_service.rb:63-68).
func checkOwners(ctx context.Context, r port.Repos, org core.OrganizationID, owners []core.MembershipID) error {
	for _, o := range owners {
		m, err := r.Memberships.Get(ctx, org, o)
		if errors.Is(err, core.ErrNotFound) || (err == nil && m.Status != access.MembershipStatusActive) {
			return fieldError("owners", CodeOwnersInvalid)
		} else if err != nil {
			return fmt.Errorf("load membership %s: %w", o, err)
		}
	}
	return nil
}

// Get returns quote id of org.
func (s *Service) Get(ctx context.Context, org core.OrganizationID, id core.QuoteID) (*QuoteView, error) {
	var out *QuoteView
	err := s.read(ctx, func(ctx context.Context, r port.Repos) error {
		q, err := r.Quotes.Get(ctx, org, id)
		if err != nil {
			return notFound(err, "quote")
		}
		out, err = s.quoteView(ctx, r, q)
		return err
	})
	return out, err
}

// List returns the quotes of org matching f, newest first (lago
// QuotesQuery).
func (s *Service) List(ctx context.Context, org core.OrganizationID, f quote.Filter, page core.PageRequest) (core.Page[*QuoteView], error) {
	var out core.Page[*QuoteView]
	err := s.read(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := r.Quotes.List(ctx, org, f, page)
		if err != nil {
			return fmt.Errorf("list quotes: %w", err)
		}
		out.NextToken = p.NextToken
		for _, q := range p.Items {
			v, err := s.quoteView(ctx, r, q)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, v)
		}
		return nil
	})
	return out, err
}

// ListVersions returns the versions of quote id, latest first (lago
// Api::V1::Quotes::VersionsController#index).
func (s *Service) ListVersions(ctx context.Context, org core.OrganizationID, id core.QuoteID, page core.PageRequest) (core.Page[*VersionView], error) {
	var out core.Page[*VersionView]
	err := s.read(ctx, func(ctx context.Context, r port.Repos) error {
		if _, err := r.Quotes.Get(ctx, org, id); err != nil {
			return notFound(err, "quote")
		}
		vs, err := r.QuoteVersions.ListByQuote(ctx, org, id)
		if err != nil {
			return fmt.Errorf("list versions of quote %s: %w", id, err)
		}
		start := 0
		if page.Token != "" {
			c, err := core.DecodeCursor(page.Token)
			if err != nil {
				return err
			}
			n, err := strconv.Atoi(c.Key)
			if err != nil {
				return fmt.Errorf("%w: page token", core.ErrInvalidValue)
			}
			start = len(vs)
			for i, v := range vs {
				if v.Version < n {
					start = i
					break
				}
			}
		}
		end := min(start+page.Limit(), len(vs))
		for _, v := range vs[start:end] {
			vv, err := versionView(ctx, r, v)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, vv)
		}
		if end < len(vs) {
			last := vs[end-1]
			out.NextToken = core.Cursor{Key: strconv.Itoa(last.Version), ID: last.ID.UUID()}.Encode()
		}
		return nil
	})
	return out, err
}

// GetVersion returns version id of quote q.
func (s *Service) GetVersion(ctx context.Context, org core.OrganizationID, q core.QuoteID, id core.QuoteVersionID) (*VersionView, error) {
	var out *VersionView
	err := s.read(ctx, func(ctx context.Context, r port.Repos) error {
		v, err := loadVersion(ctx, r, org, q, id)
		if err != nil {
			return err
		}
		out, err = versionView(ctx, r, v)
		return err
	})
	return out, err
}

// loadVersion loads version id of quote q, not found as quote_version
// when it is another quote's.
func loadVersion(ctx context.Context, r port.Repos, org core.OrganizationID, q core.QuoteID, id core.QuoteVersionID) (*quote.Version, error) {
	v, err := r.QuoteVersions.Get(ctx, org, id)
	if err != nil {
		return nil, notFound(err, "quote_version")
	}
	if !q.IsZero() && v.QuoteID != q {
		return nil, core.NotFound("quote_version")
	}
	return v, nil
}

func (s *Service) quoteView(ctx context.Context, r port.Repos, q *quote.Quote) (*QuoteView, error) {
	out := &QuoteView{Quote: q, Images: map[string]string{}}
	v, err := r.QuoteVersions.GetCurrent(ctx, q.OrganizationID, q.ID)
	switch {
	case err == nil:
		if out.Current, err = versionView(ctx, r, v); err != nil {
			return nil, err
		}
	case !errors.Is(err, core.ErrNotFound):
		return nil, fmt.Errorf("load current version of quote %s: %w", q.ID, err)
	}
	for _, ref := range q.Images {
		u, err := s.blobURL(ctx, q.OrganizationID, ref)
		if err != nil {
			return nil, err
		}
		out.Images[string(ref)] = u
	}
	return out, nil
}

func versionView(ctx context.Context, r port.Repos, v *quote.Version) (*VersionView, error) {
	out := &VersionView{Version: v}
	if v.BillingEntityID.IsZero() {
		return out, nil
	}
	be, err := r.BillingEntities.Get(ctx, v.OrganizationID, v.BillingEntityID)
	switch {
	case err == nil:
		out.BillingEntityCode = be.Code
	case !errors.Is(err, core.ErrNotFound):
		return nil, fmt.Errorf("load billing entity %s: %w", v.BillingEntityID, err)
	}
	return out, nil
}

// blobURL is the download URL of ref; empty without a blob store.
func (s *Service) blobURL(ctx context.Context, org core.OrganizationID, ref core.BlobRef) (string, error) {
	if ref.IsZero() || s.blobs == nil {
		return "", nil
	}
	u, err := s.blobs.URL(ctx, org, ref)
	if err != nil {
		return "", fmt.Errorf("url of %s: %w", ref, err)
	}
	return u, nil
}
