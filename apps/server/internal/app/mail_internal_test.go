package app

import (
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/trace/noop"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/libs/mail"
)

// TestNewMailerWithoutSMTP checks that an installation without an SMTP
// server or sender address still composes the system emails it drops.
func TestNewMailerWithoutSMTP(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, from, want string
	}{
		{"no sender address", "", dropFrom},
		{"sender address", "billing@acme.test", "billing@acme.test"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			composer, _, err := newMailer(config.Config{MailFrom: tt.from, AppURL: "https://app.tanvo.test"}, slog.New(slog.DiscardHandler), noop.NewTracerProvider())
			if err != nil {
				t.Fatal(err)
			}
			m, err := composer.PasswordResetRequested(mail.PasswordResetEmail{Email: "admin@acme.test", Token: "token"})
			if err != nil {
				t.Fatalf("compose: got = %v, want = nil", err)
			}
			if got := m.From.Address; got != tt.want {
				t.Errorf("from: got = %s, want = %s", got, tt.want)
			}
		})
	}
}
