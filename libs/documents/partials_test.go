package documents

import (
	"slices"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
)

// Ports of the partial specs of spec/views/app/views/templates/invoices/v4:
// _graduated, _graduated_percentage and _presentation_breakdowns.

func cellTexts(rows []row) [][]string {
	var out [][]string
	for _, r := range rows {
		var cells []string
		for _, c := range r.Cells {
			cells = append(cells, strings.Join(c.Text, " "))
		}
		out = append(out, cells)
	}
	return out
}

func detailRows(rows []row) [][]string {
	var details []row
	for _, r := range rows {
		if r.Style == rowDetail {
			details = append(details, r)
		}
	}
	return cellTexts(details)
}

// graduatedFee is the spec's fee of 11.77001111111111111111107306 units in
// three ranges.
func graduatedFee(t *testing.T, key string, rates [3]string) *invoice.Fee {
	t.Helper()
	ranges := []map[string]any{
		{"from_value": 0, "to_value": 2, "units": "2.0", "per_unit_total_amount": "2.0", "flat_unit_amount": "0.0"},
		{"from_value": 2, "to_value": 10, "units": "4.77001111111111111111107306", "per_unit_total_amount": "4.77", "flat_unit_amount": "0.0"},
		{"from_value": 10, "to_value": nil, "units": "5.123456789123", "per_unit_total_amount": "5.12", "flat_unit_amount": "0.0"},
	}
	for i, rg := range ranges {
		if key == "graduated_ranges" {
			rg["per_unit_amount"] = "1.0"
		} else if rates[i] != "" {
			rg["rate"] = rates[i]
		}
	}
	f := newFee(&ids{}, "charge", func(f *invoice.Fee) {
		f.Amount, f.Units, f.InvoiceDisplayName = 1174, d("11.77001111111111111111107306"), ptr("Query (hours)")
		f.AmountDetails = detailsJSON(t, map[string]any{key: ranges})
	})
	return &f
}

func graduatedRows(t *testing.T, f *invoice.Fee, percentage bool) [][]string {
	t.Helper()
	party := staticParty()
	r := &feeRows{w: newWriter(testCatalog(t), &party, "", usd())}
	d := details(f)
	ranges := d.GraduatedRanges
	if percentage {
		ranges = d.GraduatedPercentageRanges
	}
	r.graduated(f, ranges, percentage)
	return detailRows(r.rows)
}

func columnTexts(rows [][]string, i int) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r[i])
	}
	return out
}

func TestGraduatedUnits(t *testing.T) {
	for _, c := range []struct {
		name, source, key string
		percentage        bool
	}{
		{"graduated", "spec/views/app/views/templates/invoices/v4/_graduated.slim_spec.rb:57", "graduated_ranges", false},
		{"graduated percentage", "spec/views/app/views/templates/invoices/v4/_graduated_percentage.slim_spec.rb:68", "graduated_percentage_ranges", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Logf("source: %s", c.source)
			rows := graduatedRows(t, graduatedFee(t, c.key, [3]string{"1.0", "1.0", "1.0"}), c.percentage)
			if got, want := columnTexts(rows, 1), []string{"2", "4.770011", "5.123457"}; !slices.Equal(got, want) {
				t.Errorf("units: got = %q, want = %q", got, want)
			}
		})
	}
}

func TestGraduatedPercentageRates(t *testing.T) {
	for _, c := range []struct {
		name, source string
		rates        [3]string
		want         []string
	}{
		{"regular", "_graduated_percentage.slim_spec.rb:75", [3]string{"1.0", "1.0", "1.0"}, []string{"1%", "1%", "1%"}},
		{"irregular", "_graduated_percentage.slim_spec.rb:84", [3]string{"0.8999999999999999", "", "0.555e1"}, []string{"0.9%", "", "5.55%"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Logf("source: spec/views/app/views/templates/invoices/v4/%s", c.source)
			rows := graduatedRows(t, graduatedFee(t, "graduated_percentage_ranges", c.rates), true)
			if got := columnTexts(rows, 2); !slices.Equal(got, c.want) {
				t.Errorf("rates: got = %q, want = %q", got, c.want)
			}
		})
	}
}

// presentationTable renders _presentation_breakdowns for one fee of the
// spec's "compute" charge: displayed keys region (unless hideRegion) and
// department; a "" value stands for a null one.
func presentationTable(t *testing.T, hideRegion bool, units string, groupedBy map[string]string, filter *string, breakdowns ...invoice.PresentationBreakdown) [][]string {
	t.Helper()
	g := &ids{}
	plan := newPlan(g, "plan", nil)
	sub := newSubscription(g, plan)
	keys := `{"value":"region","options":{"display_in_invoice":` + map[bool]string{true: "false", false: "true"}[hideRegion] + `}},` +
		`{"value":"department","options":{"display_in_invoice":true}}`
	if breakdowns != nil && breakdowns[0].PresentationBy["country"] != nil && hideRegion {
		keys = `{"value":"country","options":{"display_in_invoice":false}}`
	}
	c, m := metricCharge(g, plan, "metric", func(c *catalog.Charge) {
		c.InvoiceDisplayName = ptr("compute")
		c.Properties = catalog.MustProperties(`{"amount":"100","presentation_group_keys":[` + keys + `]}`)
	})
	f := chargeFee(g, &sub, &c, &m, func(f *invoice.Fee) {
		f.Units, f.InvoiceDisplayName, f.GroupedBy = d(units), ptr("compute"), groupedBy
	})
	if filter != nil {
		cf := catalog.ChargeFilter{ID: g.filter(), ChargeID: c.ID, InvoiceDisplayName: filter, Values: map[string][]string{"region": {"eu"}}}
		f.ChargeFilter, f.Fee.ChargeFilterID = &cf, cf.ID
	}
	f.Fee.PresentationBreakdowns = breakdowns
	d := &Invoice{Party: staticParty(), Subscriptions: []Subscription{sub}, Fees: []Fee{f}}
	w := newWriter(testCatalog(t), &d.Party, "", usd())
	w.subs[sub.Subscription.ID] = &d.Subscriptions[0]
	v := &invoiceView{writer: w, d: d, inv: &d.Invoice}
	var out [][]string
	for _, b := range v.presentationBreakdowns([]*Fee{&d.Fees[0]}) {
		out = append(out, cellTexts(b.Rows[1:])...) // without the header row
	}
	return out
}

func pb(units string, by ...string) invoice.PresentationBreakdown {
	m := map[string]*string{}
	for i := 0; i+1 < len(by); i += 2 {
		if by[i+1] == "" {
			m[by[i]] = nil
		} else {
			m[by[i]] = ptr(by[i+1])
		}
	}
	return invoice.PresentationBreakdown{PresentationBy: m, Units: d(units)}
}

func TestPresentationBreakdowns(t *testing.T) {
	const src = "spec/views/app/views/templates/invoices/v4/_presentation_breakdowns.slim_spec.rb:"
	eu := ptr("eu")
	scenario1 := []invoice.PresentationBreakdown{
		pb("40", "region", "eu", "department", "engineering"),
		pb("35", "region", "us", "department", "engineering"),
		pb("25", "region", "us", "department", "sales"),
		pb("10", "region", "us", "department", ""),
	}
	euBreakdowns := func(a, b, c string) []invoice.PresentationBreakdown {
		return []invoice.PresentationBreakdown{
			pb(a, "region", "eu", "department", "engineering"),
			pb(b, "region", "eu", "department", "sales"),
			pb(c, "region", "eu", "department", ""),
		}
	}
	for _, c := range []struct {
		name, line string
		hide       bool
		units      string
		groupedBy  map[string]string
		filter     *string
		breakdowns []invoice.PresentationBreakdown
		want       string
	}{
		{"scenario 1", "76", false, "110", nil, nil, scenario1, "compute|110;eu, engineering|40;us, engineering|35;us, sales|25;us|10"},
		{"scenario 1, region hidden", "104", true, "110", nil, nil, scenario1, "compute|110;engineering|75;sales|25;|10"},
		{"scenario 2", "142", false, "65", map[string]string{"region": "eu"}, nil, euBreakdowns("40", "20", "5"), "compute • eu|65;eu, engineering|40;eu, sales|20;eu|5"},
		{"scenario 2, region hidden", "176", true, "65", map[string]string{"region": "eu"}, nil, euBreakdowns("40", "20", "5"), "compute • eu|65;engineering|40;sales|20;|5"},
		{"scenario 3", "220", false, "50", nil, eu, euBreakdowns("30", "15", "5"), "compute • eu|50;eu, engineering|30;eu, sales|15;eu|5"},
		{"scenario 3, region hidden", "248", true, "50", nil, eu, euBreakdowns("30", "15", "5"), "compute • eu|50;engineering|30;sales|15;|5"},
		{"scenario 4", "347", false, "50", map[string]string{"region": "eu"}, eu, euBreakdowns("30", "15", "5"), "compute • eu • eu|50;eu, engineering|30;eu, sales|15;eu|5"},
		{"scenario 4, region hidden", "375", true, "50", map[string]string{"region": "eu"}, eu, euBreakdowns("30", "15", "5"), "compute • eu • eu|50;engineering|30;sales|15;|5"},
		{"scenario 5", "281", false, "100", nil, nil, []invoice.PresentationBreakdown{pb("60", "country", "us"), pb("40", "country", "eu")}, ""},
		{"scenario 5, key hidden", "299", true, "100", nil, nil, []invoice.PresentationBreakdown{pb("60", "country", "us"), pb("40", "country", "eu")}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Logf("source: %s%s", src, c.line)
			var rows []string
			for _, r := range presentationTable(t, c.hide, c.units, c.groupedBy, c.filter, c.breakdowns...) {
				rows = append(rows, strings.Join(r, "|"))
			}
			if got := strings.Join(rows, ";"); got != c.want {
				t.Errorf("table: got = %q, want = %q", got, c.want)
			}
		})
	}
}
