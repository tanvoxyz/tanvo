// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Apache-2.0

package proto_test

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/access/v1"
	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/alert/v1"
	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/analytics/v1"
	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/attribution/v1"
	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/coupon/v1"
	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/creditnote/v1"
	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/customer/v1"
	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/dunning/v1"
	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/event/v1"
	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/export/v1"
	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/integration/v1"
	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/invoice/v1"
	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/log/v1"
	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/organization/v1"
	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/payment/v1"
	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/portal/v1"
	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/quote/v1"
	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/subscription/v1"
	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/tax/v1"
	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/usage/v1"
	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/wallet/v1"
	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/webhook/v1"
	_ "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/x402/v1"
)

// tanvoFiles returns the registered descriptors of the Tanvo API files.
func tanvoFiles(t *testing.T) []protoreflect.FileDescriptor {
	t.Helper()
	var files []protoreflect.FileDescriptor
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if strings.HasPrefix(fd.Path(), "tanvo/") {
			files = append(files, fd)
		}
		return true
	})
	slices.SortFunc(files, func(a, b protoreflect.FileDescriptor) int { return strings.Compare(a.Path(), b.Path()) })
	return files
}

// services returns every service of the Tanvo API.
func services(t *testing.T) []protoreflect.ServiceDescriptor {
	t.Helper()
	var out []protoreflect.ServiceDescriptor
	for _, fd := range tanvoFiles(t) {
		for i := range fd.Services().Len() {
			out = append(out, fd.Services().Get(i))
		}
	}
	return out
}

// methods returns every RPC of the Tanvo API.
func methods(t *testing.T) []protoreflect.MethodDescriptor {
	t.Helper()
	var out []protoreflect.MethodDescriptor
	for _, sd := range services(t) {
		for i := range sd.Methods().Len() {
			out = append(out, sd.Methods().Get(i))
		}
	}
	return out
}

func TestEverySourceFileIsRegistered(t *testing.T) {
	var sources []string
	err := filepath.WalkDir("tanvo", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".proto") {
			sources = append(sources, filepath.ToSlash(path))
		}
		return err
	})
	if err != nil {
		t.Fatalf("walk tanvo: %v", err)
	}
	var registered []string
	for _, fd := range tanvoFiles(t) {
		registered = append(registered, fd.Path())
	}
	slices.Sort(sources)
	if !slices.Equal(registered, sources) {
		t.Errorf("registered files = %v, want = %v (regenerate, or blank-import the new package)", registered, sources)
	}
}

func TestEveryRPCHasAnHTTPAnnotation(t *testing.T) {
	for _, md := range methods(t) {
		rule, _ := proto.GetExtension(md.Options(), annotations.E_Http).(*annotations.HttpRule)
		if rule == nil || rule.GetPattern() == nil {
			t.Errorf("%s: google.api.http = none, want = a binding", md.FullName())
		}
	}
}

func TestEveryRPCDeclaresItsAccess(t *testing.T) {
	for _, md := range methods(t) {
		access, _ := proto.GetExtension(md.Options(), commonv1.E_Access).(*commonv1.Access)
		if access == nil {
			t.Errorf("%s: tanvo.common.v1.access = none, want = set", md.FullName())
			continue
		}
		if access.GetPermission() == "" && !access.GetAnonymous() && !access.GetPortal() && !access.GetAuthenticated() {
			t.Errorf("%s: access = %v, want = a permission or anonymous, portal or authenticated", md.FullName(), access)
		}
	}
}

// fieldRules returns the buf.validate rules of a field, or nil.
func fieldRules(fd protoreflect.FieldDescriptor) *validate.FieldRules {
	rules, _ := proto.GetExtension(fd.Options(), validate.E_Field).(*validate.FieldRules)
	return rules
}

// isRequired reports whether a field is marked REQUIRED by google.api.field_behavior.
func isRequired(fd protoreflect.FieldDescriptor) bool {
	behaviors, _ := proto.GetExtension(fd.Options(), annotations.E_FieldBehavior).([]annotations.FieldBehavior)
	return slices.Contains(behaviors, annotations.FieldBehavior_REQUIRED)
}

// enforcesPresence reports whether buf.validate rejects an unset field:
// required, a non-empty list, or an enum excluding its zero value.
func enforcesPresence(rules *validate.FieldRules) bool {
	return rules.GetRequired() ||
		rules.GetRepeated().GetMinItems() > 0 ||
		slices.Contains(rules.GetEnum().GetNotIn(), 0)
}

// hasShapeRule reports whether string rules constrain the value beyond
// presence (a format, pattern, length or predefined rule).
func hasShapeRule(rules *validate.FieldRules) bool {
	s := rules.GetString()
	if s == nil {
		return false
	}
	set := 0
	s.ProtoReflect().Range(func(protoreflect.FieldDescriptor, protoreflect.Value) bool {
		set++
		return true
	})
	return set > 0 || len(s.ProtoReflect().GetUnknown()) > 0
}

func TestRequiredRequestIdentifiersAreValidated(t *testing.T) {
	for _, md := range methods(t) {
		in := md.Input()
		for i := range in.Fields().Len() {
			fd := in.Fields().Get(i)
			rules := fieldRules(fd)
			enforced := enforcesPresence(rules)
			if !isRequired(fd) && !enforced {
				continue
			}
			if isRequired(fd) != enforced {
				t.Errorf("%s: field_behavior REQUIRED = %t, buf.validate presence = %t, want = both", fd.FullName(), isRequired(fd), enforced)
			}
			name := string(fd.Name())
			if fd.Kind() != protoreflect.StringKind || fd.IsList() || (!strings.HasSuffix(name, "_id") && name != "name") {
				continue
			}
			if !hasShapeRule(rules) {
				t.Errorf("%s: string rules = none, want = uuid, ref, code, pattern or length", fd.FullName())
			}
		}
	}
}

// paritySections maps each feature section of docs/parity.md to the
// services serving it; an empty list is a section without RPCs.
var paritySections = map[string][]string{
	"1.1":  nil,
	"1.2":  {"tanvo.organization.v1.OrganizationService", "tanvo.organization.v1.ServerService"},
	"1.3":  {"tanvo.organization.v1.BillingEntityService"},
	"1.4":  {"tanvo.access.v1.MemberService"},
	"1.5":  {"tanvo.access.v1.AuthService", "tanvo.access.v1.SsoConnectionService"},
	"1.6":  {"tanvo.access.v1.ApiKeyService"},
	"1.7":  {"tanvo.customer.v1.CustomerService"},
	"1.8":  {"tanvo.catalog.v1.BillableMetricService"},
	"1.9":  nil,
	"1.10": {"tanvo.catalog.v1.PlanService"},
	"1.11": {"tanvo.catalog.v1.ChargeService"},
	"1.12": {"tanvo.catalog.v1.ChargeService"},
	"1.13": {"tanvo.catalog.v1.FixedChargeService", "tanvo.catalog.v1.AddOnService"},
	"1.14": {"tanvo.catalog.v1.ProductService", "tanvo.catalog.v1.RateCardService", "tanvo.catalog.v1.PlanRateCardService"},
	"1.15": {"tanvo.catalog.v1.PricingUnitService"},
	"1.16": {"tanvo.catalog.v1.FeatureService", "tanvo.catalog.v1.EntitlementService"},
	"1.17": {"tanvo.event.v1.EventService"},
	"1.18": nil,
	"1.19": {"tanvo.usage.v1.UsageService"},
	"1.20": {"tanvo.subscription.v1.SubscriptionService"},
	"1.21": nil,
	"1.22": {"tanvo.subscription.v1.SubscriptionService"},
	"1.23": {"tanvo.invoice.v1.InvoiceService", "tanvo.invoice.v1.InvoiceCustomSectionService"},
	"1.24": {"tanvo.invoice.v1.FeeService"},
	"1.25": {"tanvo.tax.v1.TaxService"},
	"1.26": {"tanvo.coupon.v1.CouponService"},
	"1.27": nil,
	"1.28": {"tanvo.creditnote.v1.CreditNoteService"},
	"1.29": {"tanvo.wallet.v1.WalletService"},
	"1.30": {"tanvo.catalog.v1.PlanService"},
	"1.31": {"tanvo.subscription.v1.SubscriptionService"},
	"1.32": {"tanvo.alert.v1.AlertService"},
	"1.33": {"tanvo.payment.v1.PaymentService", "tanvo.payment.v1.PaymentMethodService"},
	"1.34": {"tanvo.integration.v1.IntegrationService"},
	"1.35": {"tanvo.payment.v1.PaymentRequestService", "tanvo.payment.v1.PaymentReceiptService", "tanvo.dunning.v1.DunningCampaignService"},
	"1.36": {"tanvo.x402.v1.X402ConnectionService", "tanvo.x402.v1.X402Service"},
	"1.37": {"tanvo.integration.v1.IntegrationService"},
	"1.38": {"tanvo.webhook.v1.WebhookService"},
	"1.39": nil,
	"1.40": {"tanvo.webhook.v1.StreamingDestinationService"},
	"1.41": {"tanvo.quote.v1.QuoteService", "tanvo.quote.v1.OrderFormService", "tanvo.quote.v1.OrderService"},
	"1.42": {"tanvo.attribution.v1.UsageAttributionService"},
	"1.43": {"tanvo.export.v1.DataExportService"},
	"1.44": {"tanvo.analytics.v1.AnalyticsService"},
	"1.45": {"tanvo.log.v1.LogService"},
	"1.46": {"tanvo.portal.v1.CustomerPortalService"},
	"1.47": nil,
	"1.48": nil,
	"1.49": nil,
	"1.50": nil,
}

// TestParityReport logs the parity sections without RPCs and checks the
// section map against the services and docs/parity.md. Sections without
// RPCs are reported, not failed: they are served by libraries, jobs or
// plain HTTP routes.
func TestParityReport(t *testing.T) {
	known := map[string]bool{}
	for _, sd := range services(t) {
		known[string(sd.FullName())] = true
	}
	mapped := map[string]bool{}
	for section, names := range paritySections {
		for _, name := range names {
			if !known[name] {
				t.Errorf("parity section %s: service %s = unknown, want = a registered service", section, name)
			}
			mapped[name] = true
		}
	}
	for name := range known {
		if !mapped[name] {
			t.Errorf("service %s: parity section = none, want = mapped in paritySections", name)
		}
	}

	f, err := os.Open(filepath.Join("..", "..", "docs", "parity.md"))
	if err != nil {
		t.Skipf("docs/parity.md not readable: %v", err)
	}
	defer f.Close()
	heading := regexp.MustCompile(`^### (1\.\d+) (.+)$`)
	var report []string
	sc := bufio.NewScanner(f)
	seen := map[string]bool{}
	for sc.Scan() {
		m := heading.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		seen[m[1]] = true
		names, ok := paritySections[m[1]]
		switch {
		case !ok:
			t.Errorf("parity section %s %s: mapping = none, want = an entry in paritySections", m[1], m[2])
		case len(names) == 0:
			report = append(report, m[1]+" "+m[2])
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read docs/parity.md: %v", err)
	}
	for section := range paritySections {
		if !seen[section] {
			t.Errorf("parity section %s: heading = none, want = present in docs/parity.md", section)
		}
	}
	t.Logf("parity sections without RPCs (%d):\n  %s", len(report), strings.Join(report, "\n  "))
}
