package app

import (
	"testing"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/ingest"
)

// TestIngestModes checks the event store selection per organization
// (parity §1.2): the per-organization overrides win over the installation
// mode.
func TestIngestModes(t *testing.T) {
	kafkaOrg := core.NewID[core.OrganizationID](core.UUIDv7Generator{})
	pgOrg := core.NewID[core.OrganizationID](core.UUIDv7Generator{})
	other := core.NewID[core.OrganizationID](core.UUIDv7Generator{})
	tests := []struct {
		name string
		mode string
		want map[core.OrganizationID]ingest.Mode
	}{
		{"postgres installation", "postgres", map[core.OrganizationID]ingest.Mode{kafkaOrg: ingest.ModeKafka, pgOrg: ingest.ModePostgres, other: ingest.ModePostgres}},
		{"kafka installation", "kafka", map[core.OrganizationID]ingest.Mode{kafkaOrg: ingest.ModeKafka, pgOrg: ingest.ModePostgres, other: ingest.ModeKafka}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{
				IngestMode:                  tt.mode,
				IngestKafkaOrganizations:    []string{kafkaOrg.String()},
				IngestPostgresOrganizations: []string{pgOrg.String()},
			}
			modeOf, err := ingestModes(cfg)
			if err != nil {
				t.Fatalf("ingestModes: %v", err)
			}
			for org, want := range tt.want {
				if got := modeOf(org); got != want {
					t.Errorf("mode of %s: got = %s, want = %s", org, got, want)
				}
			}
		})
	}
	if _, err := ingestModes(config.Config{IngestMode: "postgres", IngestKafkaOrganizations: []string{"acme"}}); err == nil {
		t.Errorf("ingestModes with a bad organization ID: got = nil, want = an error")
	}
}
