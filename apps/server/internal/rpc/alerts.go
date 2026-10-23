package rpc

import (
	"context"
	"strconv"

	"github.com/tanvoxyz/tanvo/libs/billing/alerts"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/alert"
	alertv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/alert/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/alert/v1/alertv1connect"
)

// Alerts serves tanvo.alert.v1.AlertService over the billing library's
// alert service (lago Api::V1::Subscriptions::AlertsController and
// Api::V1::Customers::Wallets::AlertsController). ListTriggeredAlerts is
// not served: the API has no list of triggered alerts.
type Alerts struct {
	alertv1connect.UnimplementedAlertServiceHandler

	svc *alerts.Service
}

var _ alertv1connect.AlertServiceHandler = (*Alerts)(nil)

// NewAlerts returns the service over svc.
func NewAlerts(svc *alerts.Service) *Alerts { return &Alerts{svc: svc} }

var (
	alertTypes = NewEnum(map[alert.Type]alertv1.AlertType{
		alert.TypeCurrentUsageAmount:               alertv1.AlertType_ALERT_TYPE_CURRENT_USAGE_AMOUNT,
		alert.TypeBillableMetricCurrentUsageAmount: alertv1.AlertType_ALERT_TYPE_BILLABLE_METRIC_CURRENT_USAGE_AMOUNT,
		alert.TypeBillableMetricCurrentUsageUnits:  alertv1.AlertType_ALERT_TYPE_BILLABLE_METRIC_CURRENT_USAGE_UNITS,
		alert.TypeLifetimeUsageAmount:              alertv1.AlertType_ALERT_TYPE_LIFETIME_USAGE_AMOUNT,
		alert.TypeBillableMetricLifetimeUsageUnits: alertv1.AlertType_ALERT_TYPE_BILLABLE_METRIC_LIFETIME_USAGE_UNITS,
		alert.TypeWalletBalanceAmount:              alertv1.AlertType_ALERT_TYPE_WALLET_BALANCE_AMOUNT,
		alert.TypeWalletCreditsBalance:             alertv1.AlertType_ALERT_TYPE_WALLET_CREDITS_BALANCE,
		alert.TypeWalletOngoingBalanceAmount:       alertv1.AlertType_ALERT_TYPE_WALLET_ONGOING_BALANCE_AMOUNT,
		alert.TypeWalletCreditsOngoingBalance:      alertv1.AlertType_ALERT_TYPE_WALLET_CREDITS_ONGOING_BALANCE,
	})
	alertDirections = NewEnum(map[alert.Direction]alertv1.AlertDirection{
		alert.DirectionIncreasing: alertv1.AlertDirection_ALERT_DIRECTION_INCREASING,
		alert.DirectionDecreasing: alertv1.AlertDirection_ALERT_DIRECTION_DECREASING,
	})
)

// alertUpdateFields are the update_mask paths of UpdateAlert; the type and
// the direction are immutable.
var alertUpdateFields = []string{"code", "name", "billable_metric_id", "thresholds"}

// alertOwnerRequest is a request naming the subscription or the wallet of
// the alerts.
type alertOwnerRequest interface {
	GetSubscriptionId() string
	GetWalletId() string
}

// CreateAlert implements AlertService.CreateAlert. The direction follows
// the type: decreasing for wallet alerts, increasing otherwise (lago
// UsageMonitoring::CreateAlertService).
func (s *Alerts) CreateAlert(ctx context.Context, req *alertv1.CreateAlertRequest) (*alertv1.CreateAlertResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := alertOwner(req)
	if err != nil {
		return nil, err
	}
	in, err := alertCreateInput("alert.", req.GetAlert())
	if err != nil {
		return nil, err
	}
	v, err := s.svc.Create(ctx, p.OrganizationID, owner, in)
	if err != nil {
		return nil, alertError(ctx, "alert.", err)
	}
	return alertv1.CreateAlertResponse_builder{Alert: alertProto(v)}.Build(), nil
}

// BatchCreateAlerts implements AlertService.BatchCreateAlerts: all the
// alerts or none (lago UsageMonitoring::Alerts::CreateBatchService).
func (s *Alerts) BatchCreateAlerts(ctx context.Context, req *alertv1.BatchCreateAlertsRequest) (*alertv1.BatchCreateAlertsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := alertOwner(req)
	if err != nil {
		return nil, err
	}
	ins := make([]alerts.CreateInput, len(req.GetAlerts()))
	for i, a := range req.GetAlerts() {
		if ins[i], err = alertCreateInput(alertIndex(i), a); err != nil {
			return nil, err
		}
	}
	vs, err := s.svc.BatchCreate(ctx, p.OrganizationID, owner, ins)
	if err != nil {
		return nil, alertError(ctx, "", err)
	}
	out := make([]*alertv1.Alert, len(vs))
	for i, v := range vs {
		out[i] = alertProto(v)
	}
	return alertv1.BatchCreateAlertsResponse_builder{Alerts: out}.Build(), nil
}

// GetAlert implements AlertService.GetAlert; alert_id is an ID or
// code:<code>.
func (s *Alerts) GetAlert(ctx context.Context, req *alertv1.GetAlertRequest) (*alertv1.GetAlertResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := alertOwner(req)
	if err != nil {
		return nil, err
	}
	ref, err := alertRef("alert_id", req.GetAlertId())
	if err != nil {
		return nil, err
	}
	v, err := s.svc.Get(ctx, p.OrganizationID, owner, ref)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return alertv1.GetAlertResponse_builder{Alert: alertProto(v)}.Build(), nil
}

// ListAlerts implements AlertService.ListAlerts (lago
// UsageMonitoring::AlertsQuery).
func (s *Alerts) ListAlerts(ctx context.Context, req *alertv1.ListAlertsRequest) (*alertv1.ListAlertsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := alertOwner(req)
	if err != nil {
		return nil, err
	}
	page, err := s.svc.List(ctx, p.OrganizationID, owner, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return alertv1.ListAlertsResponse_builder{
		Alerts:        Items(page, alertProto),
		NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// UpdateAlert implements AlertService.UpdateAlert; alert.id is an ID or
// code:<code>. The fields updated are the update_mask paths or, without
// one, the fields set (thresholds when not empty), as lago
// UsageMonitoring::UpdateAlertService.
func (s *Alerts) UpdateAlert(ctx context.Context, req *alertv1.UpdateAlertRequest) (*alertv1.UpdateAlertResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := alertOwner(req)
	if err != nil {
		return nil, err
	}
	a := req.GetAlert()
	ref, err := alertRef("alert.id", a.GetId())
	if err != nil {
		return nil, err
	}
	paths, err := UpdatePaths(req.GetUpdateMask(), alertUpdateFields, func(path string) bool {
		switch path {
		case "code":
			return a.HasCode()
		case "name":
			return a.HasName()
		case "billable_metric_id":
			return a.HasBillableMetricId()
		case "thresholds":
			return len(a.GetThresholds()) > 0
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	var in alerts.UpdateInput
	for _, path := range paths {
		switch path {
		case "code":
			in.Code = core.Some(a.GetCode())
		case "name":
			in.Name = core.Some(Opt(a.HasName() && a.GetName() != "", a.GetName()))
		case "billable_metric_id":
			m, err := alertMetricRef("alert.billable_metric_id", a.GetBillableMetricId())
			if err != nil {
				return nil, err
			}
			in.BillableMetric = core.Some(m)
		case "thresholds":
			ths, err := alertThresholds("alert.", a.GetThresholds())
			if err != nil {
				return nil, err
			}
			in.Thresholds = core.Some(ths)
		}
	}
	v, err := s.svc.Update(ctx, p.OrganizationID, owner, ref, in)
	if err != nil {
		return nil, alertError(ctx, "alert.", err)
	}
	return alertv1.UpdateAlertResponse_builder{Alert: alertProto(v)}.Build(), nil
}

// DeleteAlert implements AlertService.DeleteAlert; alert_id is an ID or
// code:<code>.
func (s *Alerts) DeleteAlert(ctx context.Context, req *alertv1.DeleteAlertRequest) (*alertv1.DeleteAlertResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := alertOwner(req)
	if err != nil {
		return nil, err
	}
	ref, err := alertRef("alert_id", req.GetAlertId())
	if err != nil {
		return nil, err
	}
	if _, err := s.svc.Delete(ctx, p.OrganizationID, owner, ref); err != nil {
		return nil, Error(ctx, err)
	}
	return alertv1.DeleteAlertResponse_builder{}.Build(), nil
}

// DeleteAlerts implements AlertService.DeleteAlerts.
func (s *Alerts) DeleteAlerts(ctx context.Context, req *alertv1.DeleteAlertsRequest) (*alertv1.DeleteAlertsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := alertOwner(req)
	if err != nil {
		return nil, err
	}
	if err := s.svc.DeleteAll(ctx, p.OrganizationID, owner); err != nil {
		return nil, Error(ctx, err)
	}
	return alertv1.DeleteAlertsResponse_builder{}.Build(), nil
}

// alertOwner parses the subscription (an ID or external_id:<id>) or the
// wallet of req.
func alertOwner(req alertOwnerRequest) (alerts.Owner, error) {
	if w := req.GetWalletId(); w != "" {
		id, err := ParseID[core.WalletID]("wallet_id", w)
		return alerts.Owner{WalletID: id}, err
	}
	r, err := ParseRef("subscription_id", req.GetSubscriptionId())
	if err != nil {
		return alerts.Owner{}, err
	}
	if r.Code != "" {
		return alerts.Owner{}, InvalidArgument("invalid reference", []FieldViolation{{
			Field: "subscription_id", Reason: core.CodeInvalid, Description: "this resource has no code",
		}})
	}
	if r.ExternalID != "" {
		return alerts.Owner{SubscriptionExternalID: r.ExternalID}, nil
	}
	id, err := ParseID[core.SubscriptionID]("subscription_id", r.ID.String())
	return alerts.Owner{SubscriptionID: id}, err
}

// alertRef parses an alert reference: an ID or code:<code>.
func alertRef(field, s string) (alerts.Ref, error) {
	r, err := ParseRef(field, s)
	if err != nil {
		return alerts.Ref{}, err
	}
	if r.ExternalID != "" {
		return alerts.Ref{}, InvalidArgument("invalid reference", []FieldViolation{{
			Field: field, Reason: core.CodeInvalid, Description: "this resource has no external_id",
		}})
	}
	if r.Code != "" {
		return alerts.Ref{Code: r.Code}, nil
	}
	id, err := ParseID[core.AlertID](field, r.ID.String())
	return alerts.Ref{ID: id}, err
}

// alertMetricRef parses a billable metric reference; empty is none.
func alertMetricRef(field, s string) (alerts.MetricRef, error) {
	r, err := CatalogRef(field, s)
	if err != nil || r.IsZero() {
		return alerts.MetricRef{}, err
	}
	if r.Code != "" {
		return alerts.MetricRef{Code: r.Code}, nil
	}
	id, err := ParseID[core.BillableMetricID](field, r.ID.String())
	return alerts.MetricRef{ID: id}, err
}

// alertCreateInput converts an alert to create; prefix is the path of a
// in the request.
func alertCreateInput(prefix string, a *alertv1.Alert) (alerts.CreateInput, error) {
	m, err := alertMetricRef(prefix+"billable_metric_id", a.GetBillableMetricId())
	if err != nil {
		return alerts.CreateInput{}, err
	}
	ths, err := alertThresholds(prefix, a.GetThresholds())
	if err != nil {
		return alerts.CreateInput{}, err
	}
	return alerts.CreateInput{
		Type:           alertTypes.Core(a.GetAlertType()),
		Code:           a.GetCode(),
		Name:           Opt(a.HasName() && a.GetName() != "", a.GetName()),
		BillableMetric: m,
		Thresholds:     ths,
	}, nil
}

func alertThresholds(prefix string, in []*alertv1.AlertThreshold) ([]alert.Threshold, error) {
	out := make([]alert.Threshold, len(in))
	for i, t := range in {
		v, err := ParseDecimal(prefix+"thresholds["+strconv.Itoa(i)+"].value", t.GetValue())
		if err != nil {
			return nil, err
		}
		out[i] = alert.Threshold{Code: Opt(t.HasCode() && t.GetCode() != "", t.GetCode()), Value: v, Recurring: t.GetRecurring()}
	}
	return out, nil
}

// alertError maps the service errors, whose field paths are the alert's,
// onto the request's alert message at prefix; the batch errors already
// carry their alerts[i] prefix.
func alertError(ctx context.Context, prefix string, err error) error {
	if v, ok := validationViolations(prefix, err); ok {
		return withReason(InvalidArgument("invalid alert", v), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

func alertIndex(i int) string { return "alerts[" + strconv.Itoa(i) + "]." }

func alertProto(v *alerts.View) *alertv1.Alert {
	a := v.Alert
	b := alertv1.Alert_builder{
		Id:            new(a.ID.String()),
		Code:          new(a.Code),
		Name:          a.Name,
		PreviousValue: new(Decimal(a.PreviousValue)),
		// Lago serializes the last processing instant (alert_serializer.rb).
		LastProcessedAt: TimestampPtr(a.LastProcessedAt),
		CreatedAt:       Timestamp(a.CreatedAt),
		UpdatedAt:       Timestamp(a.UpdatedAt),
	}
	if t := alertTypes.Proto(a.Type); t != 0 {
		b.AlertType = &t
	}
	if d := alertDirections.Proto(a.Direction); d != 0 {
		b.Direction = &d
	}
	if !v.SubscriptionID.IsZero() {
		b.SubscriptionId = new(v.SubscriptionID.String())
	}
	if a.SubscriptionExternalID != "" {
		b.ExternalSubscriptionId = new(a.SubscriptionExternalID)
	}
	if !a.WalletID.IsZero() {
		b.WalletId = new(a.WalletID.String())
	}
	if !a.BillableMetricID.IsZero() {
		b.BillableMetricId = new(a.BillableMetricID.String())
	}
	for _, t := range a.Thresholds {
		b.Thresholds = append(b.Thresholds, alertv1.AlertThreshold_builder{
			Code: t.Code, Value: new(Decimal(t.Value)), Recurring: new(t.Recurring),
		}.Build())
	}
	return b.Build()
}
