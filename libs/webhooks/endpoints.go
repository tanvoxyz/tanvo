package webhooks

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// Resources of the not-found errors of [Endpoints].
const (
	// ResourceEndpoint is a webhook endpoint.
	ResourceEndpoint = "webhook_endpoint"
	// ResourceDelivery is a webhook delivery (lago's "webhook").
	ResourceDelivery = "webhook"
)

// Security log of endpoint changes (lago Utils::SecurityLog log_type and
// log_event).
const (
	// SecurityLogEndpoint is the log type of endpoint changes.
	SecurityLogEndpoint = "webhook_endpoint"
	// EventEndpointCreated: an endpoint was created; the resources hold its
	// webhook_url.
	EventEndpointCreated = "webhook_endpoint.created"
	// EventEndpointUpdated: an endpoint changed; the resources hold its
	// webhook_url and, when the URL changed, webhook_url's {deleted, added}
	// in place of it.
	EventEndpointUpdated = "webhook_endpoint.updated"
	// EventEndpointDeleted: an endpoint was deleted; the resources hold its
	// webhook_url.
	EventEndpointDeleted = "webhook_endpoint.deleted"
)

// SecurityLogger records the security log entries of endpoint changes;
// apps/server adapts the security log store to it.
type SecurityLogger interface {
	// RecordEndpoint records event of the [SecurityLogEndpoint] type on org
	// with resources. ctx carries the change's transaction, which the
	// entry joins.
	RecordEndpoint(ctx context.Context, org core.OrganizationID, event string, resources map[string]any) error
}

// CodeIsSucceeded refuses the retry of a delivery that succeeded (lago
// Webhooks::RetryService).
const CodeIsSucceeded = "is_succeeded"

// Endpoints manages the webhook endpoints of organizations and their
// deliveries (lago WebhookEndpoints::*Service, Webhooks::RetryService).
type Endpoints struct {
	// Tx is the transaction boundary.
	Tx port.Transactor
	// Clock is the time source.
	Clock core.Clock
	// IDs generates the endpoint IDs.
	IDs core.IDGenerator
	// Rand is the entropy of signing secrets (crypto/rand.Reader).
	Rand io.Reader
	// Resolver resolves the hosts of endpoint URLs; nil is the default
	// resolver.
	Resolver Resolver
	// AllowPrivate accepts plain HTTP and private addresses
	// (TANVO_WEBHOOKS_ALLOW_PRIVATE).
	AllowPrivate bool
	// SecurityLog records the endpoint changes; nil records nothing.
	SecurityLog SecurityLogger
}

// EndpointInput is an endpoint to create.
type EndpointInput struct {
	// URL is the destination.
	URL string
	// Name is an optional label.
	Name *string
	// EventTypes are the subscribed event types, normalized by
	// [webhook.NormalizeEventTypes]: nil and ["*"] subscribe to every type.
	EventTypes []string
}

// EndpointUpdate is a partial update of an endpoint: only the set fields
// change.
type EndpointUpdate struct {
	// URL is the destination.
	URL core.Optional[string]
	// Name is the label.
	Name core.Optional[*string]
	// EventTypes are the subscribed event types: nil and ["*"] subscribe to
	// every type, an empty list to none.
	EventTypes core.Optional[[]string]
}

// Create creates an endpoint with a fresh signing secret. A URL used by
// another endpoint of the organization is url value_already_exist, an
// invalid or private one url url_invalid, an unknown event type
// event_types invalid_types; the eleventh endpoint fails with the rule
// exceeded_limit.
//
// source: app/services/webhook_endpoints/create_service.rb:14-42, app/models/webhook_endpoint.rb:25-31
func (s *Endpoints) Create(ctx context.Context, org core.OrganizationID, in EndpointInput) (*webhook.Endpoint, error) {
	secret, err := webhook.NewSecret(s.Rand)
	if err != nil {
		return nil, err
	}
	now := s.Clock.Now().UTC()
	e := &webhook.Endpoint{
		ID:             core.NewID[core.WebhookEndpointID](s.IDs),
		OrganizationID: org,
		URL:            in.URL,
		Name:           in.Name,
		EventTypes:     webhook.NormalizeEventTypes(in.EventTypes),
		Secret:         secret,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	err = s.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := s.validate(ctx, r, e); err != nil {
			return err
		}
		// The limit is a validation on create only (webhook_endpoint.rb:28).
		n, err := r.WebhookEndpoints.Count(ctx, org)
		if err != nil {
			return fmt.Errorf("count endpoints: %w", err)
		}
		if err := webhook.CheckEndpointLimit(n); err != nil {
			return err
		}
		if err := urlConflict(r.WebhookEndpoints.Create(ctx, e)); err != nil {
			return err
		}
		return s.record(ctx, org, EventEndpointCreated, map[string]any{"webhook_url": e.URL})
	})
	if err != nil {
		return nil, fmt.Errorf("create webhook endpoint: %w", err)
	}
	return e, nil
}

// Update changes the set fields of the endpoint id, with the rules of
// [Endpoints.Create].
//
// source: app/services/webhook_endpoints/update_service.rb:15-48
func (s *Endpoints) Update(ctx context.Context, org core.OrganizationID, id core.WebhookEndpointID, in EndpointUpdate) (*webhook.Endpoint, error) {
	var out *webhook.Endpoint
	err := s.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		e, err := r.WebhookEndpoints.Get(ctx, org, id)
		if err != nil {
			return endpointNotFound(err)
		}
		previous := e.URL
		in.URL.Apply(&e.URL)
		in.Name.Apply(&e.Name)
		if in.EventTypes.Set {
			e.EventTypes = webhook.NormalizeEventTypes(in.EventTypes.Value)
		}
		if err := s.validate(ctx, r, e); err != nil {
			return err
		}
		e.UpdatedAt = s.Clock.Now().UTC()
		if err := urlConflict(r.WebhookEndpoints.Update(ctx, e)); err != nil {
			return err
		}
		out = e
		resources := map[string]any{"webhook_url": e.URL}
		if previous != e.URL {
			resources["webhook_url"] = map[string]any{"deleted": previous, "added": e.URL}
		}
		return s.record(ctx, org, EventEndpointUpdated, resources)
	})
	if err != nil {
		return nil, fmt.Errorf("update webhook endpoint %s: %w", id, err)
	}
	return out, nil
}

// Delete deletes the endpoint id with its deliveries and returns it.
//
// source: app/services/webhook_endpoints/destroy_service.rb:14-34
func (s *Endpoints) Delete(ctx context.Context, org core.OrganizationID, id core.WebhookEndpointID) (*webhook.Endpoint, error) {
	var out *webhook.Endpoint
	err := s.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		e, err := r.WebhookEndpoints.Get(ctx, org, id)
		if err != nil {
			return endpointNotFound(err)
		}
		if err := r.WebhookEndpoints.Delete(ctx, org, id); err != nil {
			return endpointNotFound(err)
		}
		out = e
		return s.record(ctx, org, EventEndpointDeleted, map[string]any{"webhook_url": e.URL})
	})
	if err != nil {
		return nil, fmt.Errorf("delete webhook endpoint %s: %w", id, err)
	}
	return out, nil
}

// Get returns the endpoint id.
func (s *Endpoints) Get(ctx context.Context, org core.OrganizationID, id core.WebhookEndpointID) (*webhook.Endpoint, error) {
	var out *webhook.Endpoint
	err := s.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		e, err := r.WebhookEndpoints.Get(ctx, org, id)
		out = e
		return endpointNotFound(err)
	})
	return out, err
}

// List returns a page of the organization's endpoints by URL, then newest
// first. An organization has at most [webhook.MaxEndpoints] endpoints, so
// they are sorted here; the page token is the URL of the last endpoint of
// the previous page.
//
// source: app/queries/webhook_endpoints_query.rb:6-15
func (s *Endpoints) List(ctx context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*webhook.Endpoint], error) {
	var all []*webhook.Endpoint
	err := s.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		req := core.PageRequest{Size: core.MaxPageSize}
		for {
			p, err := r.WebhookEndpoints.List(ctx, org, req)
			if err != nil {
				return err
			}
			all = append(all, p.Items...)
			if p.NextToken == "" {
				return nil
			}
			req.Token = p.NextToken
		}
	})
	if err != nil {
		return core.Page[*webhook.Endpoint]{}, fmt.Errorf("list webhook endpoints: %w", err)
	}
	slices.SortStableFunc(all, func(a, b *webhook.Endpoint) int {
		if c := cmp.Compare(a.URL, b.URL); c != 0 {
			return c
		}
		return b.CreatedAt.Compare(a.CreatedAt)
	})
	start := 0
	if page.Token != "" {
		start = len(all)
		for i, e := range all {
			if e.URL > page.Token {
				start = i
				break
			}
		}
	}
	end := min(start+page.Limit(), len(all))
	out := core.Page[*webhook.Endpoint]{Items: all[start:end]}
	if end < len(all) && end > start {
		out.NextToken = all[end-1].URL
	}
	return out, nil
}

// Delivery returns the delivery id of the endpoint.
//
// source: app/graphql/resolvers/webhook_resolver.rb:15-19
func (s *Endpoints) Delivery(ctx context.Context, org core.OrganizationID, endpoint core.WebhookEndpointID, id core.WebhookID) (*webhook.Delivery, error) {
	var out *webhook.Delivery
	err := s.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = delivery(ctx, r, org, endpoint, id)
		return err
	})
	return out, err
}

// Deliveries returns a page of the deliveries of the endpoint matching f,
// the last updated first.
//
// source: app/queries/webhooks_query.rb:7-24
func (s *Endpoints) Deliveries(ctx context.Context, org core.OrganizationID, endpoint core.WebhookEndpointID, f webhook.DeliveryFilter, page core.PageRequest) (core.Page[*webhook.Delivery], error) {
	f.Order = core.OrderRecent
	var out core.Page[*webhook.Delivery]
	err := s.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if _, err := r.WebhookEndpoints.Get(ctx, org, endpoint); err != nil {
			return endpointNotFound(err)
		}
		var err error
		out, err = r.WebhookDeliveries.ListByEndpoint(ctx, org, endpoint, f, page)
		return err
	})
	return out, err
}

// Retry schedules a new attempt of the delivery id of the endpoint now; a
// delivery that succeeded fails with the rule is_succeeded.
//
// source: app/services/webhooks/retry_service.rb:14-21
func (s *Endpoints) Retry(ctx context.Context, org core.OrganizationID, endpoint core.WebhookEndpointID, id core.WebhookID) (*webhook.Delivery, error) {
	var out *webhook.Delivery
	err := s.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		d, err := delivery(ctx, r, org, endpoint, id)
		if err != nil {
			return err
		}
		if d.Status == webhook.StatusSucceeded {
			return core.NewRuleError(CodeIsSucceeded, "the webhook succeeded")
		}
		out, err = (&Dispatcher{Tx: s.Tx, Clock: s.Clock}).Retry(ctx, org, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// record records a security log entry of an endpoint change in the
// change's transaction.
func (s *Endpoints) record(ctx context.Context, org core.OrganizationID, event string, resources map[string]any) error {
	if s.SecurityLog == nil {
		return nil
	}
	if err := s.SecurityLog.RecordEndpoint(ctx, org, event, resources); err != nil {
		return fmt.Errorf("record %s: %w", event, err)
	}
	return nil
}

func delivery(ctx context.Context, r port.Repos, org core.OrganizationID, endpoint core.WebhookEndpointID, id core.WebhookID) (*webhook.Delivery, error) {
	d, err := r.WebhookDeliveries.Get(ctx, org, id)
	if errors.Is(err, core.ErrNotFound) || (err == nil && d.EndpointID != endpoint) {
		return nil, core.NotFound(ResourceDelivery)
	}
	return d, err
}

// validate checks e as lago's model does: the endpoint's own rules, then
// the URL's address and its uniqueness in the organization.
func (s *Endpoints) validate(ctx context.Context, r port.Repos, e *webhook.Endpoint) error {
	var v core.Validator
	v.Merge("", e.Validate())
	if e.URL != "" && webhook.ValidURL(e.URL) {
		if err := CheckURL(ctx, s.Resolver, e.URL, s.AllowPrivate); err != nil {
			v.Add("url", webhook.CodeInvalidURL)
		}
		// Lago validates uniqueness in the model (webhook_endpoint.rb:27);
		// at most MaxEndpoints endpoints are scanned.
		taken := false
		req := core.PageRequest{Size: core.MaxPageSize}
		for {
			p, err := r.WebhookEndpoints.List(ctx, e.OrganizationID, req)
			if err != nil {
				return fmt.Errorf("check url: %w", err)
			}
			taken = taken || slices.ContainsFunc(p.Items, func(o *webhook.Endpoint) bool { return o.ID != e.ID && o.URL == e.URL })
			if p.NextToken == "" {
				break
			}
			req.Token = p.NextToken
		}
		v.Check(!taken, "url", core.CodeAlreadyExists)
	}
	return v.Err()
}

func endpointNotFound(err error) error {
	if errors.Is(err, core.ErrNotFound) {
		return core.NotFound(ResourceEndpoint)
	}
	return err
}

// urlConflict turns a uniqueness violation, which a concurrent write can
// still cause after validate, into url value_already_exist.
func urlConflict(err error) error {
	if errors.Is(err, core.ErrConflict) {
		return &core.ValidationError{Errors: []core.FieldError{{Field: "url", Code: core.CodeAlreadyExists}}}
	}
	return err
}
