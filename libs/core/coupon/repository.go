package coupon

import (
	"context"
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
}

// Repository stores coupons. Every method is scoped to an organization.
type Repository interface {
	// Create inserts c; a code used by another active coupon of the
	// organization fails with core.ErrConflict.
	Create(ctx context.Context, c *Coupon) error
	// Update replaces the stored coupon c.ID with c (same uniqueness rule
	// as Create).
	Update(ctx context.Context, c *Coupon) error
	// Get returns the coupon id of org, terminated or not, or
	// core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.CouponID) (*Coupon, error)
	// GetByCode returns the active coupon with that code, or
	// core.ErrNotFound.
	GetByCode(ctx context.Context, org core.OrganizationID, code string) (*Coupon, error)
	// List returns the organization's coupons in creation order.
	List(ctx context.Context, org core.OrganizationID, f Filter, page core.PageRequest) (core.Page[*Coupon], error)
	// ListExpired returns the active time_limit coupons of every
	// organization whose expiration is at or before now, for the periodic
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
	// List returns the organization's applied coupons in creation order.
	List(ctx context.Context, org core.OrganizationID, f AppliedFilter, page core.PageRequest) (core.Page[*AppliedCoupon], error)
}
