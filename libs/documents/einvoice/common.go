package einvoice

import (
	"cmp"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// settlement is what payment means and allowances print about the document
// they belong to.
type settlement struct {
	labels   Labels
	currency money.Currency
	// createdAt is the creation instant of the document, printed for card
	// payments.
	createdAt time.Time
	// cardDigits is the masked card number of a card payment.
	cardDigits string
}

// paymentLabel names a payment means.
func (s settlement) paymentLabel(means int) string {
	switch means {
	case paymentStandard:
		return s.labels.StandardPayment
	case paymentPrepaid:
		return s.labels.PrepaidCredits
	case paymentCreditNote:
		return s.labels.CreditNotes
	case paymentCreditCard:
		return s.labels.CreditCard
	default:
		return ""
	}
}

// paymentInformation describes a payment means of amount units.
func (s settlement) paymentInformation(means int, amount money.Decimal) string {
	switch means {
	case paymentStandard:
		return s.paymentLabel(means)
	case paymentPrepaid, paymentCreditNote:
		return interpolate(s.labels.PaymentInformation,
			"payment_label", s.paymentLabel(means),
			"currency", s.currency.Code(),
			"amount", amountText(amount, s.currency))
	case paymentCreditCard:
		return interpolate(s.labels.CreditCardInformation, "date", timeText(s.createdAt))
	default:
		return ""
	}
}

// discountReason is the reason of the discount at rate.
func (s settlement) discountReason(rate money.Decimal) string {
	return interpolate(s.labels.DiscountReason, "tax_rate", percent(rate))
}

// taxCategory is the VAT category: out of scope for prepaid credits, zero
// rated at 0 %, standard otherwise.
func taxCategory(credit bool, rate money.Decimal) string {
	switch {
	case credit:
		return categoryOutOfScope
	case rate.IsZero():
		return categoryZero
	default:
		return categoryStandard
	}
}

// taxSubtotal is the VAT breakdown at one rate, in units.
type taxSubtotal struct {
	category string
	rate     money.Decimal
	basis    money.Decimal
	tax      money.Decimal
}

// allowance is the document-level discount at one rate, in units.
type allowance struct {
	rate   money.Decimal
	amount money.Decimal
}

// rateGroup is the members sharing a tax rate.
type rateGroup struct {
	rate    money.Decimal
	members []int
}

// groupByRate groups n members by rate, ascending; members keep their
// order.
func groupByRate(n int, rate func(int) money.Decimal) []rateGroup {
	var groups []rateGroup
	for i := range n {
		r := rate(i)
		g := slices.IndexFunc(groups, func(g rateGroup) bool { return g.rate.Equal(r) })
		if g < 0 {
			g = len(groups)
			groups = append(groups, rateGroup{rate: r})
		}
		groups[g].members = append(groups[g].members, i)
	}
	slices.SortStableFunc(groups, func(a, b rateGroup) int { return a.rate.Cmp(b.rate) })
	return groups
}

// byAmount returns the indexes of n lines ordered by ascending amount,
// ties in their given order.
func byAmount(n int, amount func(int) int64) []int {
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(amount(a), amount(b)) })
	return order
}
