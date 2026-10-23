package metering_test

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/metering/storetest"
)

func TestMemoryStore(t *testing.T) {
	storetest.Run(t, func(_ *testing.T, events []metering.Event) metering.Store {
		return metering.NewMemoryStore(events)
	})
}
