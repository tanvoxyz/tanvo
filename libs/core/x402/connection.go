package x402

import (
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// FacilitatorKind is the service that verifies and settles payments.
type FacilitatorKind string

// FacilitatorKind values.
const (
	// FacilitatorCoinbaseCDP is "coinbase_cdp": the Coinbase Developer
	// Platform x402 facilitator.
	FacilitatorCoinbaseCDP FacilitatorKind = "coinbase_cdp"
)

// Valid reports whether k is a declared facilitator.
func (k FacilitatorKind) Valid() bool { return k == FacilitatorCoinbaseCDP }

// String returns the stored form of k.
func (k FacilitatorKind) String() string { return string(k) }

// Validation codes of connections and settlements.
const (
	// CodeMixedEnvironments is networks mixing mainnets and testnets.
	CodeMixedEnvironments = "mixed_environments"
	// CodeInvalidChecksum is a mixed-case EVM address with a wrong
	// EIP-55 checksum.
	CodeInvalidChecksum = "invalid_checksum"
	// CodeMissingEVMPayoutAddress is an EVM network without an EVM
	// payout address.
	CodeMissingEVMPayoutAddress = "missing_evm_payout_address"
	// CodeMissingSVMPayoutAddress is a Solana network without a Solana
	// payout address.
	CodeMissingSVMPayoutAddress = "missing_svm_payout_address"
	// CodeSameOrganization is a reference to another organization.
	CodeSameOrganization = "must_belong_to_same_organization"
	// CodeNotOfferedByConnection is a network the connection does not
	// offer.
	CodeNotOfferedByConnection = "not_offered_by_connection"
	// CodeNotConnectionPayoutAddress is a payee other than the
	// connection's payout address.
	CodeNotConnectionPayoutAddress = "not_connection_payout_address"
	// CodeNotConnectionAsset is a token other than the connection's.
	CodeNotConnectionAsset = "not_connection_asset"
)

// Connection is an organization's account at a facilitator: the networks
// and token it accepts, where the money goes, and whether unknown agents
// may pay to become customers (parity §1.36).
//
// source: app/models/x402/connection.rb
type Connection struct {
	// ID identifies the connection.
	ID core.X402ConnectionID
	// OrganizationID owns the connection.
	OrganizationID core.OrganizationID
	// Code is unique among the organization's live connections.
	Code string
	// Name is the display name.
	Name string
	// Facilitator is the facilitator service.
	Facilitator FacilitatorKind
	// Asset is the token accepted on every network.
	Asset AssetCode
	// Networks are the networks payments settle on, all mainnets or all
	// testnets.
	Networks []Network
	// PayoutAddresses is the payee of each family of Networks.
	PayoutAddresses map[Family]string
	// AutoCreateCustomers lets an unknown agent pay.
	AutoCreateCustomers bool
	// Secrets holds the sealed facilitator credentials (the CDP API key
	// ID and secret, see [Credentials]); core never opens them.
	Secrets []byte
	// DeletedAt is set once the connection is deleted.
	DeletedAt *time.Time
	// CreatedAt and UpdatedAt are the row timestamps.
	CreatedAt, UpdatedAt time.Time
}

// Credentials are the facilitator credentials sealed in
// [Connection.Secrets].
type Credentials struct {
	// APIKeyID is the CDP API key name.
	APIKeyID string `json:"cdp_api_key_id"`
	// APIKeySecret is the CDP API key secret: an EC private key in PEM,
	// or a base64 Ed25519 key.
	APIKeySecret string `json:"cdp_api_key_secret"`
}

// Validate checks the credentials are set.
func (c Credentials) Validate() error {
	var v core.Validator
	v.Required("cdp_api_key_id", c.APIKeyID)
	v.Required("cdp_api_key_secret", c.APIKeySecret)
	return v.Err()
}

// Families returns the families of the connection's supported networks,
// without repetition, in network order.
func (c *Connection) Families() []Family {
	var out []Family
	for _, n := range c.Networks {
		if f, ok := n.Family(); ok && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// Offers reports whether the connection settles on n.
func (c *Connection) Offers(n Network) bool { return slices.Contains(c.Networks, n) }

// Normalize stores the EVM payout address checksummed.
//
// source: app/models/x402/connection.rb:43-47
func (c *Connection) Normalize() {
	if a, ok := c.PayoutAddresses[FamilyEVM]; ok {
		c.PayoutAddresses[FamilyEVM] = NormalizeAddress(a, FamilyEVM)
	}
}

// Validate checks the connection: code, name, sealed secrets and networks
// set; supported networks of one environment; a payout address of each
// network family, each a valid address of its family.
//
// source: app/models/x402/connection.rb:28-81
func (c *Connection) Validate() error {
	var v core.Validator
	v.Required("code", c.Code)
	v.Required("name", c.Name)
	v.Enum("facilitator", c.Facilitator)
	v.Enum("asset", c.Asset)
	v.Check(len(c.Secrets) > 0, "secrets", core.CodeRequired)
	if len(c.Networks) == 0 {
		v.Add("networks", core.CodeRequired)
	}
	envs := map[Environment]bool{}
	known := 0
	for _, n := range c.Networks {
		if e, ok := n.Environment(); ok {
			envs[e] = true
			known++
		}
	}
	v.Check(known == len(c.Networks), "networks", core.CodeInvalid)
	v.Check(len(envs) <= 1, "networks", CodeMixedEnvironments)
	c.validatePayouts(&v)
	return v.Err()
}

func (c *Connection) validatePayouts(v *core.Validator) {
	if len(c.PayoutAddresses) == 0 {
		v.Add("payout_addresses", core.CodeInvalid)
		return
	}
	for f := range c.PayoutAddresses {
		if !f.Valid() {
			v.Add("payout_addresses", core.CodeInvalid)
			return
		}
	}
	for _, f := range []Family{FamilyEVM, FamilySVM} {
		a, ok := c.PayoutAddresses[f]
		switch {
		case !ok:
		case ValidAddress(a, f):
		case f == FamilyEVM && IsEVMAddress(a):
			v.Add("payout_addresses", CodeInvalidChecksum)
		default:
			v.Add("payout_addresses", core.CodeInvalidFormat)
		}
	}
	for _, f := range c.Families() {
		if _, ok := c.PayoutAddresses[f]; !ok {
			v.Add("payout_addresses", "missing_"+string(f)+"_payout_address")
		}
	}
}
