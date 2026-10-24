package x402pay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// ErrNoFacilitator reports a facilitator call without [x402.Facilitators]
// or without a [Sealer] to open the connection's credentials.
var ErrNoFacilitator = errors.New("x402pay: no facilitator configured")

// Sealer encrypts the credentials kept at rest; a sealed value opens only
// for its organization.
type Sealer interface {
	// Seal encrypts plaintext for org.
	Seal(ctx context.Context, org core.OrganizationID, plaintext []byte) ([]byte, error)
	// Open decrypts a value sealed for org.
	Open(ctx context.Context, org core.OrganizationID, sealed []byte) ([]byte, error)
}

// Service runs x402 payments.
type Service struct {
	d            *kernel.Deps
	box          Sealer
	facilitators x402.Facilitators
	log          *slog.Logger
}

// New returns the service over d; box opens the connections' credentials
// and facilitators reaches their facilitators (either nil makes every
// facilitator call fail with [ErrNoFacilitator]).
func New(d *kernel.Deps, box Sealer, facilitators x402.Facilitators) (*Service, error) {
	switch {
	case d == nil, d.Tx == nil:
		return nil, fmt.Errorf("%w: Tx", kernel.ErrMissingDependency)
	case d.Clock == nil:
		return nil, fmt.Errorf("%w: Clock", kernel.ErrMissingDependency)
	case d.IDs == nil:
		return nil, fmt.Errorf("%w: IDs", kernel.ErrMissingDependency)
	}
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Service{d: d, box: box, facilitators: facilitators, log: log}, nil
}

// facilitator opens the facilitator of c with its credentials.
//
// source: app/services/x402/facilitator/client.rb
func (s *Service) facilitator(ctx context.Context, c *x402.Connection) (x402.Facilitator, error) {
	if s.box == nil || s.facilitators == nil {
		return nil, ErrNoFacilitator
	}
	plain, err := s.box.Open(ctx, c.OrganizationID, c.Secrets)
	if err != nil {
		return nil, fmt.Errorf("open x402 connection %s credentials: %w", c.ID, err)
	}
	var creds x402.Credentials
	if err := json.Unmarshal(plain, &creds); err != nil {
		return nil, fmt.Errorf("decode x402 connection %s credentials: %w", c.ID, err)
	}
	f, err := s.facilitators.For(c.Facilitator, creds)
	if err != nil {
		return nil, fmt.Errorf("x402 connection %s facilitator: %w", c.ID, err)
	}
	return f, nil
}

// ConnectionInput is a new connection.
type ConnectionInput struct {
	// Code identifies the connection in the organization.
	Code string
	// Name names it.
	Name string
	// Facilitator is the facilitator; coinbase_cdp when empty.
	Facilitator x402.FacilitatorKind
	// Asset is the token; usdc when empty.
	Asset x402.AssetCode
	// Networks are the networks it settles on.
	Networks []x402.Network
	// PayoutAddresses are the payout addresses of each network family.
	PayoutAddresses map[x402.Family]string
	// AutoCreateCustomers lets strangers pay; true when nil.
	AutoCreateCustomers *bool
	// Credentials are the facilitator credentials, sealed at rest.
	Credentials x402.Credentials
}

// CreateConnection creates a connection of org. Connections are
// configured outside the API (v1.55.0 has none); this is the operator's
// and the tests' way in. A code held by a live connection is
// code: value_already_exist.
func (s *Service) CreateConnection(ctx context.Context, org core.OrganizationID, in ConnectionInput) (*x402.Connection, error) {
	if s.box == nil {
		return nil, ErrNoFacilitator
	}
	now := s.d.Clock.Now().UTC()
	c := &x402.Connection{
		ID: core.NewID[core.X402ConnectionID](s.d.IDs), OrganizationID: org, Code: in.Code, Name: in.Name,
		Facilitator: in.Facilitator, Asset: in.Asset, Networks: in.Networks, PayoutAddresses: in.PayoutAddresses,
		AutoCreateCustomers: in.AutoCreateCustomers == nil || *in.AutoCreateCustomers, CreatedAt: now, UpdatedAt: now,
	}
	if c.Facilitator == "" {
		c.Facilitator = x402.FacilitatorCoinbaseCDP
	}
	if c.Asset == "" {
		c.Asset = x402.AssetUSDC
	}
	var v core.Validator
	v.Merge("", in.Credentials.Validate())
	if v.Valid() {
		plain, err := json.Marshal(in.Credentials)
		if err != nil {
			return nil, fmt.Errorf("encode x402 credentials: %w", err)
		}
		if c.Secrets, err = s.box.Seal(ctx, org, plain); err != nil {
			return nil, fmt.Errorf("seal x402 credentials: %w", err)
		}
	} else {
		c.Secrets = []byte{0}
	}
	c.Normalize()
	v.Merge("", c.Validate())
	if err := v.Err(); err != nil {
		return nil, err
	}
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if _, err := r.X402Connections.GetByCode(ctx, org, c.Code); err == nil {
			return fieldError("code", core.CodeAlreadyExists)
		} else if !errors.Is(err, core.ErrNotFound) {
			return fmt.Errorf("get x402 connection %q: %w", c.Code, err)
		}
		if err := r.X402Connections.Create(ctx, c); err != nil {
			if errors.Is(err, core.ErrConflict) {
				return fieldError("code", core.CodeAlreadyExists)
			}
			return fmt.Errorf("create x402 connection: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// fieldError is a validation failure of one field.
func fieldError(field, code string) error {
	var v core.Validator
	v.Add(field, code)
	return v.Err()
}

// refusal is a refusal on the base field (lago's
// single_validation_failure).
func refusal(code string) error { return fieldError("base", code) }
