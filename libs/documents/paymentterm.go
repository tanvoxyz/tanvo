package documents

import (
	"strconv"

	"github.com/tanvoxyz/tanvo/libs/documents/internal/i18n"
)

// paymentTermText is the payment term row of an invoice: its net payment
// term snapshot in days (invoice.payment_term_days,
// app/views/templates/invoices/v4.slim:443).
func (w *writer) paymentTermText(d *Invoice) string {
	return w.l.T("invoice.payment_term_days", i18n.A("net_payment_term", strconv.Itoa(d.Invoice.NetPaymentTermDays)))
}
