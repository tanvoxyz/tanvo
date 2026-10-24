package customers_test

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/customers"
	"github.com/tanvoxyz/tanvo/libs/core"
)

func TestX402AgentAddress(t *testing.T) {
	const checksummed = "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed"
	h, svc := setup(t)

	// source: spec/models/customer_spec.rb:369-376
	c, err := svc.Create(h.Ctx, h.OrgID(), "agent", customers.Input{X402AgentAddress: core.Some(new("0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed"))})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.X402AgentAddress == nil || *c.X402AgentAddress != checksummed {
		t.Errorf("address: got = %v, want = %s", c.X402AgentAddress, checksummed)
	}

	// source: spec/models/customer_spec.rb:387-397
	for _, a := range []string{"0x5AAeb6053F3E94C9b9A09f33669435E7Ef1BeAed", "0x5aaeb6053f3e94c9b9a09f33669435e7ef1beae"} {
		_, err := svc.Create(h.Ctx, h.OrgID(), "bad", customers.Input{X402AgentAddress: core.Some(new(a))})
		if got := fieldCode(err, "x402_agent_address"); got != core.CodeInvalidFormat {
			t.Errorf("%s: got = %q (%v), want = %s", a, got, err, core.CodeInvalidFormat)
		}
	}

	// source: spec/models/customer_spec.rb:420-431
	_, err = svc.Create(h.Ctx, h.OrgID(), "agent-2", customers.Input{X402AgentAddress: core.Some(new(checksummed))})
	if got := fieldCode(err, "x402_agent_address"); got != core.CodeAlreadyExists {
		t.Errorf("duplicate: got = %q (%v), want = %s", got, err, core.CodeAlreadyExists)
	}

	// source: spec/models/customer_spec.rb:407-418
	_, err = svc.Update(h.Ctx, h.OrgID(), c.ID, customers.Input{X402AgentAddress: core.Some(new("0xfB6916095ca1df60bB79Ce92cE3Ea74c37c5d359"))})
	if got := fieldCode(err, "x402_agent_address"); got != core.CodeImmutable {
		t.Errorf("change: got = %q (%v), want = %s", got, err, core.CodeImmutable)
	}
	u, err := svc.Update(h.Ctx, h.OrgID(), c.ID, customers.Input{
		Name:             core.Some(new("Agent")),
		X402AgentAddress: core.Some(new("0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed")),
	})
	if err != nil || *u.Name != "Agent" || *u.X402AgentAddress != checksummed {
		t.Errorf("update: got = %v, %v, want = renamed, address kept", u, err)
	}
}
