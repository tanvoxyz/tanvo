package einvoice

// Document type codes (UNTDID 1001).
const (
	typePaymentReceipt    = 202
	typeCommercialInvoice = 380
	typeCreditNote        = 381
	typePrepaidInvoice    = 386
	typeSelfBilledInvoice = 389
)

// taxScheme is the tax type (UNTDID 5153).
const taxScheme = "VAT"

// vatExemptionOutOfScope is the VAT exemption reason of supplies outside
// the scope of VAT (VATEX).
const vatExemptionOutOfScope = "VATEX-EU-O"

// Payment means codes (UNTDID 4461).
const (
	paymentStandard   = 1
	paymentCreditCard = 48
	paymentPrepaid    = 57
	paymentCreditNote = 97
)

// Allowance/charge indicators.
const (
	indicatorDiscount = false
	indicatorCharge   = true
)

// Tax category codes (UNTDID 5305).
const (
	categoryStandard   = "S"
	categoryOutOfScope = "O"
	categoryZero       = "Z"
)

// unitCode is the unit of every quantity: one (UN/ECE Recommendation 20).
const unitCode = "C62"

// responsePaid is the response code of a paid invoice in a payment
// receipt.
const responsePaid = "PD"

// CII namespaces and context.
const (
	ciiNSXS  = "http://www.w3.org/2001/XMLSchema"
	ciiNSRSM = "urn:un:unece:uncefact:data:standard:CrossIndustryInvoice:100"
	ciiNSQDT = "urn:un:unece:uncefact:data:standard:QualifiedDataType:100"
	ciiNSRAM = "urn:un:unece:uncefact:data:standard:ReusableAggregateBusinessInformationEntity:100"
	ciiNSUDT = "urn:un:unece:uncefact:data:standard:UnqualifiedDataType:100"

	// ciiDateFormat is the date format code CCYYMMDD (UNTDID 2379).
	ciiDateFormat = "102"
	// ciiTaxSchemeID qualifies a VAT registration number.
	ciiTaxSchemeID = "VA"
)

// UBL namespaces and profiles.
const (
	ublNSCAC         = "urn:oasis:names:specification:ubl:schema:xsd:CommonAggregateComponents-2"
	ublNSCBC         = "urn:oasis:names:specification:ubl:schema:xsd:CommonBasicComponents-2"
	ublNSInvoice     = "urn:oasis:names:specification:ubl:schema:xsd:Invoice-2"
	ublNSCreditNote  = "urn:oasis:names:specification:ubl:schema:xsd:CreditNote-2"
	ublNSApplication = "urn:oasis:names:specification:ubl:schema:xsd:ApplicationResponse-2"

	ublVersion = "2.1"

	profileEN16931    = "urn:cen.eu:en16931:2017"
	profileXRechnung  = profileEN16931 + "#compliant#urn:xeinkauf.de:kosit:xrechnung_3.0"
	profilePeppolBIS  = "urn:fdc:peppol.eu:2017:poacc:billing:01:1.0"
	receiptCustomizer = "urn:oasis:names:specification:ubl:xpath:ApplicationResponse-2.4"
	receiptProfile    = "urn:oasis:names:specification:ubl:schema:xsd:ApplicationResponse-2"
)

// creditNoteSettlement is the payment terms of a credit note.
const creditNoteSettlement = "Credit note - immediate settlement"

// ciiNamespaces are the namespace declarations of a CII document.
func ciiNamespaces() []attr {
	return []attr{
		{"xmlns:xs", ciiNSXS},
		{"xmlns:rsm", ciiNSRSM},
		{"xmlns:qdt", ciiNSQDT},
		{"xmlns:ram", ciiNSRAM},
		{"xmlns:udt", ciiNSUDT},
	}
}

// ublNamespaces are the namespace declarations of a UBL document whose
// default namespace is def.
func ublNamespaces(def string) []attr {
	return []attr{
		{"xmlns", def},
		{"xmlns:cac", ublNSCAC},
		{"xmlns:cbc", ublNSCBC},
	}
}
