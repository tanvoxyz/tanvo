package mail

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Sender delivers messages.
type Sender interface {
	// Send delivers m to all its recipients.
	Send(ctx context.Context, m *Message) error
}

// PortMailer adapts a [Sender] to the core port.Mailer interface.
type PortMailer struct {
	sender Sender
	logger *slog.Logger
}

var _ port.Mailer = (*PortMailer)(nil)

// NewPortMailer returns a port.Mailer that sends through s; a nil logger
// uses slog.Default.
func NewPortMailer(s Sender, logger *slog.Logger) *PortMailer {
	if logger == nil {
		logger = slog.Default()
	}
	return &PortMailer{sender: s, logger: logger}
}

// Send implements port.Mailer.
func (p *PortMailer) Send(ctx context.Context, org core.OrganizationID, e port.Email) error {
	m, err := FromPortEmail(e)
	if err != nil {
		return fmt.Errorf("send email: %w", err)
	}
	if err := p.sender.Send(ctx, m); err != nil {
		return fmt.Errorf("send email: %w", err)
	}
	p.logger.InfoContext(ctx, "email sent", "organization_id", org.String(), "subject", m.Subject, "recipients", len(m.Recipients()))
	return nil
}

// MemorySender keeps sent messages in memory, for tests. It is safe for
// concurrent use.
type MemorySender struct {
	mu   sync.Mutex
	sent []*Message
	err  error
}

var _ Sender = (*MemorySender)(nil)

// NewMemorySender returns an empty MemorySender.
func NewMemorySender() *MemorySender { return &MemorySender{} }

// Send validates and records a copy of m, or returns the error set with
// [MemorySender.FailWith].
func (s *MemorySender) Send(ctx context.Context, m *Message) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("send email: %w", err)
	}
	if err := m.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	c := *m
	c.Attachments = slices.Clone(m.Attachments)
	s.sent = append(s.sent, &c)
	return nil
}

// FailWith makes later sends fail with err; nil restores success.
func (s *MemorySender) FailWith(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// Messages returns the messages sent so far, oldest first.
func (s *MemorySender) Messages() []*Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sent)
}

// Reset forgets the sent messages.
func (s *MemorySender) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = nil
}
