package kernel

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// PayInAdvanceInterval returns the period a document shows for a charge
// fee of a pay-in-advance charge billed in arrears, or paid in advance on a
// charge that is not invoiceable: the charges period holding the fee's
// billing instant, in current-usage mode (lago Fee#date_boundaries,
// app/models/fee.rb:342-365, with
// Subscriptions::DatesService.charge_pay_in_advance_interval,
// app/services/subscriptions/dates_service.rb:28-39). The billing instant
// is the timestamp of is, the fee's invoice subscription, for a fee billed
// in arrears, else (or when is is nil, an invoice not saved) the fee's own
// timestamp.
//
// Like lago it is resolved when the document is rendered, from the
// subscription as it is then: the period depends on state that can change
// after billing, such as the subscription's termination (which ends the
// charges period) and the customer's time zone. ok is false for the other
// fees and when the charges period does not apply.
func PayInAdvanceInterval(ctx context.Context, r port.Repos, f *invoice.Fee, c *catalog.Charge, is *invoice.Subscription) (p period.Period, ok bool, err error) {
	if f.Type != core.FeeTypeCharge || c == nil || !c.PayInAdvance || f.SubscriptionID.IsZero() {
		return period.Period{}, false, nil
	}
	ts := f.Properties.Timestamp
	switch {
	case !f.PayInAdvance:
		if is != nil {
			ts = is.Timestamp
		}
	case c.Invoiceable:
		return period.Period{}, false, nil
	}
	env, err := LoadSubscriptionEnv(ctx, r, f.OrganizationID, f.SubscriptionID)
	if err != nil {
		return period.Period{}, false, fmt.Errorf("pay-in-advance interval of fee %s: %w", f.ID, err)
	}
	d, err := env.Dates(ts, period.CurrentUsage)
	if err != nil {
		return period.Period{}, false, fmt.Errorf("pay-in-advance interval of fee %s: %w", f.ID, err)
	}
	p, ok = d.ChargesPeriod()
	return p, ok, nil
}
