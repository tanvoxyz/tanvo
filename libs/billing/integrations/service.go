package integrations

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Resource is the resource name of the not-found errors.
const Resource = "integration"

// CodeInUse is the rule broken by deleting a payment integration that
// payments, payment methods or refunds still reference.
const CodeInUse = "integration_in_use"

// ErrNoSealer reports credentials to store without a [Sealer].
var ErrNoSealer = errors.New("integrations: no secret sealer configured")

// Sealer encrypts the credentials kept at rest; *auth.AESSecretBox
// implements it. A sealed value opens only for its organization.
type Sealer interface {
	// Seal encrypts plaintext for org.
	Seal(ctx context.Context, org core.OrganizationID, plaintext []byte) ([]byte, error)
	// Open decrypts a value sealed for org.
	Open(ctx context.Context, org core.OrganizationID, sealed []byte) ([]byte, error)
}

type randReader = io.Reader

// Service manages integrations.
type Service struct {
	d    *kernel.Deps
	box  Sealer
	rand io.Reader
	log  SecurityLogger
}

// New returns the service over d; box seals the credentials and may be nil
// only when no credentials are stored.
func New(d *kernel.Deps, box Sealer) *Service { return &Service{d: d, box: box, rand: rand.Reader} }

// Input is the integration to create.
type Input struct {
	// Code is unique in the organization.
	Code string
	// Name is the display name.
	Name string
	// Provider is the vendor; it sets the category.
	Provider integration.Provider
	// Values are the provider settings and credentials, by lago key
	// (Key* constants); empty values are left out.
	Values map[string]string
	// SyncInvoices, SyncCreditNotes, SyncPayments and SyncSubscriptions
	// are the provider's sync flags.
	SyncInvoices bool
	// SyncCreditNotes pushes credit notes.
	SyncCreditNotes bool
	// SyncPayments pushes payments.
	SyncPayments bool
	// SyncSubscriptions pushes subscriptions.
	SyncSubscriptions bool
}

// Update are the changes of an integration; unset fields are kept.
type Update struct {
	// Provider, when set, must be the integration's: the settings given are
	// that provider's.
	Provider integration.Provider
	// Code is the new code, unique like [Input.Code] (every lago update
	// service sets it when given).
	Code core.Optional[string]
	// Name is the new display name.
	Name core.Optional[string]
	// Values are the settings and credentials to replace, by lago key;
	// an empty value clears a setting.
	Values map[string]string
	// SyncInvoices is the new flag.
	SyncInvoices core.Optional[bool]
	// SyncCreditNotes is the new flag.
	SyncCreditNotes core.Optional[bool]
	// SyncPayments is the new flag.
	SyncPayments core.Optional[bool]
	// SyncSubscriptions is the new flag.
	SyncSubscriptions core.Optional[bool]
}

// Create creates an integration (lago Integrations::<Provider>::
// CreateService and PaymentProviders::<Provider>Service#create_or_update).
func (s *Service) Create(ctx context.Context, org core.OrganizationID, in Input) (*integration.Integration, error) {
	sp, ok := specs[in.Provider]
	if !ok {
		return nil, &core.ValidationError{Errors: []core.FieldError{{Field: "provider", Code: core.CodeInvalid}}}
	}
	cat, _ := integration.CategoryOf(in.Provider)
	now := s.d.Now()
	i := &integration.Integration{
		ID: core.NewID[core.IntegrationID](s.d.IDs), OrganizationID: org, Code: in.Code, Name: in.Name,
		Category: cat, Provider: in.Provider, CreatedAt: now, UpdatedAt: now,
	}
	var v core.Validator
	values := map[string]string{}
	for k, val := range in.Values {
		if val == "" {
			continue
		}
		if !sp.accepts(k) {
			v.Add(k, core.CodeForbidden)
			continue
		}
		values[k] = normalize(in.Provider, k, val)
	}
	for _, k := range sp.required {
		v.Required(k, values[k])
	}
	sp.checkValues(&v, values)
	flags := map[string]bool{
		syncInvoices: in.SyncInvoices, syncCreditNotes: in.SyncCreditNotes,
		syncPayments: in.SyncPayments, syncSubscriptions: in.SyncSubscriptions,
	}
	for f, on := range flags {
		v.Check(!on || slices.Contains(sp.sync, f), f, integration.CodeSyncNotSupported)
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	i.SyncInvoices, i.SyncCreditNotes, i.SyncPayments, i.SyncSubscriptions = in.SyncInvoices, in.SyncCreditNotes, in.SyncPayments, in.SyncSubscriptions
	settings, secrets := map[string]string{}, map[string]string{}
	for k, val := range values {
		if sp.isSecret(k) {
			secrets[k] = val
		} else {
			settings[k] = val
		}
	}
	for _, k := range slices.Sorted(maps.Keys(sp.generated)) {
		g, err := sp.generated[k](s.rand)
		if err != nil {
			return nil, fmt.Errorf("generate %s: %w", k, err)
		}
		secrets[k] = g
	}
	if err := s.store(ctx, i, settings, secrets); err != nil {
		return nil, err
	}
	if err := i.Validate(); err != nil {
		return nil, err
	}
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := codeConflict(r.Integrations.Create(ctx, i)); err != nil {
			return err
		}
		return s.record(ctx, i, EventCreated, nil)
	})
	if err != nil {
		return nil, err
	}
	return i, nil
}

// Update changes an integration (lago Integrations::<Provider>::
// UpdateService and the payment providers' create_or_update). Only the
// keys lago updates may change.
func (s *Service) Update(ctx context.Context, org core.OrganizationID, id core.IntegrationID, in Update) (*integration.Integration, error) {
	var out *integration.Integration
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		i, err := get(ctx, r, org, id)
		if err != nil {
			return err
		}
		before, err := logState(i)
		if err != nil {
			return err
		}
		sp := specs[i.Provider]
		var v core.Validator
		v.Check(in.Provider == "" || in.Provider == i.Provider, string(in.Provider), core.CodeForbidden)
		for k := range in.Values {
			if !slices.Contains(sp.updatable, k) {
				v.Add(k, core.CodeImmutable)
			}
		}
		for f, o := range map[string]core.Optional[bool]{
			syncInvoices: in.SyncInvoices, syncCreditNotes: in.SyncCreditNotes,
			syncPayments: in.SyncPayments, syncSubscriptions: in.SyncSubscriptions,
		} {
			v.Check(!o.Set || !o.Value || slices.Contains(sp.sync, f), f, integration.CodeSyncNotSupported)
		}
		values := map[string]string{}
		for k, val := range in.Values {
			values[k] = normalize(i.Provider, k, val)
		}
		sp.checkValues(&v, values)
		for _, k := range sp.required {
			if val, ok := values[k]; ok {
				v.Required(k, val)
			}
		}
		if err := v.Err(); err != nil {
			return err
		}
		settings, err := Settings(i)
		if err != nil {
			return err
		}
		secrets, err := s.secrets(ctx, i)
		if err != nil {
			return err
		}
		for k, val := range values {
			dst := settings
			if sp.isSecret(k) {
				dst = secrets
			}
			if val == "" {
				delete(dst, k)
			} else {
				dst[k] = val
			}
		}
		in.Code.Apply(&i.Code)
		in.Name.Apply(&i.Name)
		in.SyncInvoices.Apply(&i.SyncInvoices)
		in.SyncCreditNotes.Apply(&i.SyncCreditNotes)
		in.SyncPayments.Apply(&i.SyncPayments)
		in.SyncSubscriptions.Apply(&i.SyncSubscriptions)
		if err := s.store(ctx, i, settings, secrets); err != nil {
			return err
		}
		if err := i.Validate(); err != nil {
			return err
		}
		i.UpdatedAt = s.d.Now()
		if err := codeConflict(r.Integrations.Update(ctx, i)); err != nil {
			return err
		}
		after, err := logState(i)
		if err != nil {
			return err
		}
		if err := s.record(ctx, i, EventUpdated, logDiff(before, after)); err != nil {
			return err
		}
		out = i
		return nil
	})
	return out, err
}

// Delete removes an integration. A payment provider is discarded with its
// provider customers, which unlinks its customers, while payments keep
// referencing it (lago PaymentProviders::DestroyService); another
// integration is removed with its mappings, items, resources and customer
// links (lago Integrations::DestroyService).
func (s *Service) Delete(ctx context.Context, org core.OrganizationID, id core.IntegrationID) (*integration.Integration, error) {
	var out *integration.Integration
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		i, err := get(ctx, r, org, id)
		if err != nil {
			return err
		}
		if i.Category == integration.CategoryPayment {
			now := s.d.Now()
			if err := r.Integrations.Discard(ctx, org, id, now); err != nil {
				return notFound(err)
			}
			i.DeletedAt, i.UpdatedAt = &now, now
			out = i
			return s.record(ctx, i, EventDeleted, nil)
		}
		switch err := r.Integrations.Delete(ctx, org, id); {
		case errors.Is(err, core.ErrConflict):
			return core.NewRuleError(CodeInUse, "payments, payment methods or refunds reference the integration")
		case err != nil:
			return notFound(err)
		}
		out = i
		return s.record(ctx, i, EventDeleted, nil)
	})
	return out, err
}

// Get returns an integration.
func (s *Service) Get(ctx context.Context, org core.OrganizationID, id core.IntegrationID) (*integration.Integration, error) {
	var out *integration.Integration
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) (err error) {
		out, err = get(ctx, r, org, id)
		return err
	})
	return out, err
}

// GetByCode returns the integration with that code.
func (s *Service) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*integration.Integration, error) {
	var out *integration.Integration
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) (err error) {
		out, err = r.Integrations.GetByCode(ctx, org, code)
		if err == nil && !Supported(out.Provider) {
			err = core.ErrNotFound
		}
		return notFound(err)
	})
	return out, err
}

// List lists the organization's integrations in creation order (lago
// IntegrationsResolver and PaymentProvidersResolver), leaving out the
// auth integrations of libs/auth.
func (s *Service) List(ctx context.Context, org core.OrganizationID, f integration.Filter, page core.PageRequest) (core.Page[*integration.Integration], error) {
	var out core.Page[*integration.Integration]
	if f.Category == integration.CategoryAuth || (f.Provider != "" && !Supported(f.Provider)) {
		return out, nil
	}
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		for {
			p, err := r.Integrations.List(ctx, org, f, page)
			if err != nil {
				return err
			}
			out.NextToken = p.NextToken
			for _, i := range p.Items {
				if Supported(i.Provider) {
					out.Items = append(out.Items, i)
				}
			}
			// A page of auth integrations only is skipped.
			if len(out.Items) > 0 || p.NextToken == "" {
				return nil
			}
			page.Token = p.NextToken
		}
	})
	return out, err
}

// Settings returns the non-secret settings of i by lago key.
func Settings(i *integration.Integration) (map[string]string, error) {
	out := map[string]string{}
	if len(i.Settings) == 0 {
		return out, nil
	}
	var raw map[string]any
	if err := json.Unmarshal(i.Settings, &raw); err != nil {
		return nil, fmt.Errorf("integration %s settings: %w", i.ID, err)
	}
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out, nil
}

// Secrets returns the credentials of i by lago key, for the provider
// clients.
func (s *Service) Secrets(ctx context.Context, i *integration.Integration) (map[string]string, error) {
	return s.secrets(ctx, i)
}

func (s *Service) secrets(ctx context.Context, i *integration.Integration) (map[string]string, error) {
	out := map[string]string{}
	if len(i.Secrets) == 0 {
		return out, nil
	}
	if s.box == nil {
		return nil, ErrNoSealer
	}
	plain, err := s.box.Open(ctx, i.OrganizationID, i.Secrets)
	if err != nil {
		return nil, fmt.Errorf("integration %s secrets: %w", i.ID, err)
	}
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, fmt.Errorf("integration %s secrets: %w", i.ID, err)
	}
	return out, nil
}

// store encodes settings into i and seals secrets into it.
func (s *Service) store(ctx context.Context, i *integration.Integration, settings, secrets map[string]string) error {
	if i.Provider == integration.ProviderAnrok {
		// types/integrations/anrok.rb:20-22: the account is the API key
		// before its slash.
		delete(settings, KeyExternalAccountID)
		if account, _, ok := strings.Cut(secrets[KeyAPIKey], "/"); ok {
			settings[KeyExternalAccountID] = account
		}
	}
	i.Settings = nil
	if len(settings) > 0 {
		b, err := json.Marshal(settings)
		if err != nil {
			return fmt.Errorf("encode settings: %w", err)
		}
		i.Settings = b
	}
	i.Secrets = nil
	if len(secrets) == 0 {
		return nil
	}
	if s.box == nil {
		return ErrNoSealer
	}
	b, err := json.Marshal(secrets)
	if err != nil {
		return fmt.Errorf("encode secrets: %w", err)
	}
	if i.Secrets, err = s.box.Seal(ctx, i.OrganizationID, b); err != nil {
		return fmt.Errorf("seal secrets: %w", err)
	}
	return nil
}

// get returns an integration the service manages, or
// core.NotFound("integration").
func get(ctx context.Context, r port.Repos, org core.OrganizationID, id core.IntegrationID) (*integration.Integration, error) {
	i, err := r.Integrations.Get(ctx, org, id)
	if err == nil && !Supported(i.Provider) {
		err = core.ErrNotFound
	}
	return i, notFound(err)
}

func notFound(err error) error {
	if errors.Is(err, core.ErrNotFound) {
		return core.NotFound(Resource)
	}
	return err
}

// codeConflict turns the code uniqueness violation into code
// value_already_exist (lago BaseIntegration and BaseProvider code
// uniqueness).
func codeConflict(err error) error {
	if errors.Is(err, core.ErrConflict) {
		return &core.ValidationError{Errors: []core.FieldError{{Field: "code", Code: core.CodeAlreadyExists}}}
	}
	return err
}

const alphanumerics = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// alphanumeric returns n random alphanumerics (Ruby
// SecureRandom.alphanumeric).
func alphanumeric(r io.Reader, n int) (string, error) {
	out := make([]byte, 0, n)
	buf := make([]byte, 1)
	for len(out) < n {
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		// 248 = 4 × 62: reject the bias.
		if buf[0] < 248 {
			out = append(out, alphanumerics[int(buf[0])%len(alphanumerics)])
		}
	}
	return string(out), nil
}

// hexString returns n random bytes in hex (Ruby SecureRandom.hex).
func hexString(r io.Reader, n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
