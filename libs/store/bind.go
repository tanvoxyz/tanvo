package store

import "github.com/tanvoxyz/tanvo/libs/core/port"

// bind returns the repositories of the transaction, one bindX per
// aggregate file (repo_<area>.go).
func (t *txState) bind() port.Repos {
	var r port.Repos
	t.bindInfra(&r)
	t.bindOrganization(&r)
	t.bindAccess(&r)
	t.bindCustomer(&r)
	t.bindCoupon(&r)
	t.bindDunning(&r)
	t.bindCatalog(&r)
	t.bindCatalogFeatures(&r)
	t.bindSubscription(&r)
	t.bindEvent(&r)
	t.bindAlert(&r)
	t.bindInvoice(&r)
	t.bindCreditNote(&r)
	t.bindWallet(&r)
	t.bindPayment(&r)
	t.bindIntegration(&r)
	t.bindWebhook(&r)
	t.bindExport(&r)
	t.bindQuote(&r)
	return r
}
