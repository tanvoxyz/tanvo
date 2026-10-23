package billing

import (
	"github.com/tanvoxyz/tanvo/libs/billing/customers"
	"github.com/tanvoxyz/tanvo/libs/tax"
)

// WithVIES checks the VAT numbers of customers with c
// ([customers.JobVIESCheck]); without it the check fails.
func WithVIES(c tax.VIESChecker) Option {
	return func(e *Engine) { customers.WithVIES(c)(e.Customers) }
}
