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

func TestMemoryBucketsConformance(t *testing.T) {
	storetest.RunBucketConformance(t, func(_ *testing.T, defs []metering.BucketDefinition, events []metering.Event) (metering.Store, metering.BucketReader) {
		s := metering.NewMemoryStore(events)
		return s, s.Buckets(defs...)
	})
}

func TestMemoryBucketSpecs(t *testing.T) {
	storetest.RunBucketSpecs(t, func(_ *testing.T, defs []metering.BucketDefinition, events []metering.Event) (metering.Store, metering.BucketReader) {
		s := metering.NewMemoryStore(events)
		return s, s.Buckets(defs...)
	})
}

func TestMemoryAttributedUsage(t *testing.T) {
	storetest.RunAttributed(t, func(_ *testing.T, events []metering.Event) metering.AttributedUsageReader {
		return metering.NewMemoryStore(events)
	})
}
