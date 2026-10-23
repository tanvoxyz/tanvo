package einvoice

// Labels are the localized strings the documents carry, in the customer's
// document locale. Templates interpolate %{name} placeholders. A zero
// Labels stands for [EnglishLabels].
type Labels struct {
	// StandardPayment labels a standard payment
	// (invoice.e_invoicing.standard_payment).
	StandardPayment string
	// PrepaidCredits labels a payment by prepaid credits
	// (invoice.prepaid_credits).
	PrepaidCredits string
	// CreditNotes labels a payment by credit notes (invoice.credit_notes).
	CreditNotes string
	// CreditCard labels a card payment (invoice.e_invoicing.credit_card).
	CreditCard string
	// PaymentInformation describes an applied credit, with %{payment_label},
	// %{currency} and %{amount} (invoice.e_invoicing.payment_information).
	PaymentInformation string
	// CreditCardInformation describes a card payment, with %{date}
	// (invoice.e_invoicing.credit_card_information).
	CreditCardInformation string
	// DiscountReason is the reason of a discount at a rate, with
	// %{tax_rate} (invoice.e_invoicing.discount_reason).
	DiscountReason string
	// PaymentTerm introduces the payment term (invoice.payment_term).
	PaymentTerm string
	// PaymentTermDays is the payment term, with %{net_payment_term}
	// (invoice.payment_term_days).
	PaymentTermDays string
	// WordsConnector joins the items of a list but the last two
	// (support.array.words_connector).
	WordsConnector string
	// TwoWordsConnector joins a list of two items
	// (support.array.two_words_connector).
	TwoWordsConnector string
	// LastWordConnector joins the last two items of a longer list
	// (support.array.last_word_connector).
	LastWordConnector string
}

// EnglishLabels returns the English labels.
func EnglishLabels() Labels {
	return Labels{
		StandardPayment:       "Standard payment",
		PrepaidCredits:        "Prepaid credits",
		CreditNotes:           "Credit notes",
		CreditCard:            "Credit Card Payment",
		PaymentInformation:    "%{payment_label} of %{currency} %{amount} applied",
		CreditCardInformation: "Credit card payment received on %{date}",
		DiscountReason:        "Discount %{tax_rate} portion",
		PaymentTerm:           "Payment term",
		PaymentTermDays:       "%{net_payment_term} days",
		WordsConnector:        ", ",
		TwoWordsConnector:     " and ",
		LastWordConnector:     ", and ",
	}
}

// orEnglish returns l, or the English labels when l is zero.
func (l Labels) orEnglish() Labels {
	if l == (Labels{}) {
		return EnglishLabels()
	}
	return l
}
