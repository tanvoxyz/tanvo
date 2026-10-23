package validators

import (
	"fmt"
	"strconv"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// oneOff is lago OneOff::BusinessValidator.
func (b *business) oneOff() error {
	b.validateCurrency()
	if err := b.validateBillingEntity(); err != nil {
		return err
	}
	for i, item := range b.items.get("addOns").items() {
		if err := b.validateAddOnExistence(item, i); err != nil {
			return err
		}
		b.validateAddOnDatetimes(item, i)
	}
	return nil
}

func addOnField(i int, suffix string) string {
	return "billing_items.addOns." + strconv.Itoa(i) + "." + suffix
}

// validateAddOnExistence requires the add-on to exist, deleted or not.
func (b *business) validateAddOnExistence(item *value, i int) error {
	id, ok := lookup(item.get("id"), core.ParseID[core.AddOnID])
	if ok {
		_, err := b.r.AddOns.Get(b.ctx, b.org(), id)
		missing, err := notFound(err)
		if err != nil {
			return fmt.Errorf("get add-on %s: %w", id, err)
		}
		ok = !missing
	}
	if !ok {
		b.errs.add(addOnField(i, "id"), "add_on_not_found")
	}
	return nil
}

// validateAddOnDatetimes requires both bounds of a period, from not after
// to, in the payload and in the overrides.
func (b *business) validateAddOnDatetimes(item *value, i int) {
	for _, section := range []string{"payload", "overrides"} {
		from, to := item.get(section, "fromDatetime"), item.get(section, "toDatetime")
		switch {
		case from.isNull() && to.isNull():
		case from.isNull():
			b.errs.add(addOnField(i, section+".fromDatetime"), core.CodeRequired)
		case to.isNull():
			b.errs.add(addOnField(i, section+".toDatetime"), core.CodeRequired)
		default:
			fs, _ := from.str()
			ts, _ := to.str()
			ft, fok := parseTime(fs)
			tt, tok := parseTime(ts)
			if fok && tok && ft.After(tt) {
				b.errs.add(addOnField(i, section+".fromDatetime"), "invalid_date_range")
			}
		}
	}
}
