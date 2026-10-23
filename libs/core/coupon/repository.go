package coupon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Filter narrows [Repository.List].
type Filter struct {
	// Status keeps coupons with that status; "" keeps all.
	Status Status
	// Search keeps coupons whose code or name contains the text
	// (case-insensitive).
	Search string
	// Order is the list order; core.OrderRecent is the order of the API:
	// by status (active first), expiration (no_expiration first) and
	// expiration_at (earliest first, none last), then newest first and by
	// ID ([RecentPosition]).
	Order core.ListOrder
}

// Repository stores coupons. Every method is scoped to an organization.
type Repository interface {
	// Create inserts c; a code used by another coupon of the organization
	// that is not deleted fails with core.ErrConflict.
	Create(ctx context.Context, c *Coupon) error
	// Update replaces the stored coupon c.ID with c (same uniqueness rule
	// as Create).
	Update(ctx context.Context, c *Coupon) error
	// Get returns the coupon id of org, terminated or deleted or not, or
	// core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.CouponID) (*Coupon, error)
	// GetByCode returns the coupon with that code that is not deleted,
	// terminated or not, or core.ErrNotFound.
	GetByCode(ctx context.Context, org core.OrganizationID, code string) (*Coupon, error)
	// List returns the organization's coupons that are not deleted, in
	// creation order or f.Order.
	List(ctx context.Context, org core.OrganizationID, f Filter, page core.PageRequest) (core.Page[*Coupon], error)
	// ListExpired returns the active time_limit coupons, not deleted, of
	// every organization whose expiration is at or before now, for the periodic
	// termination job (domain §8.1).
	ListExpired(ctx context.Context, now time.Time, page core.PageRequest) (core.Page[*Coupon], error)
}

// AppliedFilter narrows [AppliedRepository.List].
type AppliedFilter struct {
	// CustomerID keeps the applied coupons of that customer; zero keeps
	// all.
	CustomerID core.CustomerID
	// CouponID keeps the applications of that coupon; zero keeps all.
	CouponID core.CouponID
	// Status keeps applied coupons with that status; "" keeps all.
	Status Status
	// CouponIDs and CouponCodes keep the applications of these coupons and
	// of the coupons with these codes, deleted ones included; an
	// application matches either. Both empty keep all.
	CouponIDs   []core.CouponID
	CouponCodes []string
	// KeptCustomers keeps the applied coupons of customers that are not
	// deleted.
	KeptCustomers bool
	// Order is the list order; core.OrderRecent lists newest first, then
	// by ID.
	Order core.ListOrder
}

// AppliedRepository stores applied coupons. Every method is scoped to an
// organization.
type AppliedRepository interface {
	// Create inserts a.
	Create(ctx context.Context, a *AppliedCoupon) error
	// Update replaces the stored applied coupon a.ID with a.
	Update(ctx context.Context, a *AppliedCoupon) error
	// Get returns the applied coupon id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.AppliedCouponID) (*AppliedCoupon, error)
	// ListActiveByCustomer returns the customer's active applied coupons,
	// oldest application first (the deduction order of domain §22.7).
	ListActiveByCustomer(ctx context.Context, org core.OrganizationID, customer core.CustomerID) ([]*AppliedCoupon, error)
	// ListActiveByCoupon returns the active applications of coupon, for
	// terminating them when the coupon is deleted (domain §8.1).
	ListActiveByCoupon(ctx context.Context, org core.OrganizationID, coupon core.CouponID) ([]*AppliedCoupon, error)
	// CountByCouponAndCustomer counts the applications of coupon to
	// customer, active or not (the reusable rule of domain §8.2).
	CountByCouponAndCustomer(ctx context.Context, org core.OrganizationID, coupon core.CouponID, customer core.CustomerID) (int, error)
	// List returns the organization's applied coupons in creation order or
	// f.Order.
	List(ctx context.Context, org core.OrganizationID, f AppliedFilter, page core.PageRequest) (core.Page[*AppliedCoupon], error)
}

// RecentPosition is the position of a coupon in the core.OrderRecent
// order of [Repository.List].
type RecentPosition struct {
	// Status is the coupon's status.
	Status Status
	// Expiration is the coupon's expiration.
	Expiration Expiration
	// ExpirationAt is the coupon's expiry instant, nil for none.
	ExpirationAt *time.Time
	// CreatedAt is the coupon's creation instant.
	CreatedAt time.Time
}

// PositionOf returns the position of c.
func PositionOf(c *Coupon) RecentPosition {
	return RecentPosition{Status: c.Status, Expiration: c.Expiration, ExpirationAt: c.ExpirationAt, CreatedAt: c.CreatedAt}
}

// Key returns the textual form of p, the Key of a page cursor.
func (p RecentPosition) Key() string {
	at := ""
	if p.ExpirationAt != nil {
		at = p.ExpirationAt.UTC().Format(time.RFC3339Nano)
	}
	return strings.Join([]string{string(p.Status), string(p.Expiration), at, p.CreatedAt.UTC().Format(time.RFC3339Nano)}, " ")
}

// ParseRecentPosition parses a key of [RecentPosition.Key]; a malformed
// key fails with core.ErrInvalidValue.
func ParseRecentPosition(key string) (RecentPosition, error) {
	parts := strings.Split(key, " ")
	if len(parts) != 4 {
		return RecentPosition{}, fmt.Errorf("%w: page token", core.ErrInvalidValue)
	}
	p := RecentPosition{Status: Status(parts[0]), Expiration: Expiration(parts[1])}
	if parts[2] != "" {
		at, err := time.Parse(time.RFC3339Nano, parts[2])
		if err != nil {
			return RecentPosition{}, fmt.Errorf("%w: page token", core.ErrInvalidValue)
		}
		p.ExpirationAt = &at
	}
	at, err := time.Parse(time.RFC3339Nano, parts[3])
	if err != nil {
		return RecentPosition{}, fmt.Errorf("%w: page token", core.ErrInvalidValue)
	}
	p.CreatedAt = at
	return p, nil
}

// Compare orders p and q in the core.OrderRecent order: negative when p
// comes first.
func (p RecentPosition) Compare(q RecentPosition) int {
	if c := strings.Compare(string(p.Status), string(q.Status)); c != 0 {
		return c
	}
	if c := strings.Compare(string(p.Expiration), string(q.Expiration)); c != 0 {
		return c
	}
	switch {
	case p.ExpirationAt == nil && q.ExpirationAt != nil:
		return 1
	case p.ExpirationAt != nil && q.ExpirationAt == nil:
		return -1
	case p.ExpirationAt != nil:
		if c := p.ExpirationAt.Compare(*q.ExpirationAt); c != 0 {
			return c
		}
	}
	return q.CreatedAt.Compare(p.CreatedAt)
}
