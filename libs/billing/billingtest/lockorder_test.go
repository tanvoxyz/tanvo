package billingtest

import (
	"testing"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// TestLockOrderSubscriptions checks that the lock-order checker accepts
// several subscription locks only under a billing_schedule lock or in
// ascending ID order (domain §6).
func TestLockOrderSubscriptions(t *testing.T) {
	low := port.LockKey{Scope: "subscription", ID: uuid.MustParse("00000000-0000-7000-8000-000000000001")}
	high := port.LockKey{Scope: "subscription", ID: uuid.MustParse("00000000-0000-7000-8000-000000000002")}
	schedule := port.CustomerLock(customer.LockScopeBillingSchedule, core.CustomerID{})
	tests := []struct {
		name    string
		keys    []port.LockKey
		wantErr bool
	}{
		{"ascending", []port.LockKey{low, high}, false},
		{"descending", []port.LockKey{high, low}, true},
		{"descending under billing_schedule", []port.LockKey{schedule, high, low}, false},
		{"re-entrant", []port.LockKey{low, high, low}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var o lockOrder
			var err error
			for _, k := range tt.keys {
				if err = o.check(k); err != nil {
					break
				}
			}
			if (err != nil) != tt.wantErr {
				t.Errorf("check() error = %v, want error = %t", err, tt.wantErr)
			}
		})
	}
}
