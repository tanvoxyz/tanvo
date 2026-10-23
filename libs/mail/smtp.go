package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Security is how an SMTP connection is protected.
type Security string

// Security modes.
const (
	// SecuritySTARTTLS upgrades the connection with STARTTLS and fails if
	// the server does not offer it. It is the default.
	SecuritySTARTTLS Security = "starttls"
	// SecuritySTARTTLSOptional upgrades with STARTTLS when the server
	// offers it (Rails' enable_starttls_auto).
	SecuritySTARTTLSOptional Security = "starttls_optional"
	// SecurityTLS connects with TLS from the start (SMTPS, port 465).
	SecurityTLS Security = "tls"
	// SecurityNone never uses TLS; only for local relays.
	SecurityNone Security = "none"
)

// AuthMechanism is the SMTP authentication mechanism.
type AuthMechanism string

// Authentication mechanisms.
const (
	// AuthAuto uses PLAIN when the server offers it, else LOGIN. It is
	// the default.
	AuthAuto AuthMechanism = "auto"
	// AuthPlain is RFC 4616 PLAIN.
	AuthPlain AuthMechanism = "plain"
	// AuthLogin is the LOGIN mechanism.
	AuthLogin AuthMechanism = "login"
)

// SMTPConfig configures an [SMTPSender].
type SMTPConfig struct {
	// Host is the server host name; required.
	Host string
	// Port is the server port; 0 means 465 with SecurityTLS, else 587.
	Port int
	// Username enables authentication when set.
	Username string
	// Password is the authentication password.
	Password string
	// Auth is the mechanism; empty means AuthAuto.
	Auth AuthMechanism
	// Security is the TLS mode; empty means SecuritySTARTTLS.
	Security Security
	// AllowInsecureAuth permits authenticating over a connection without
	// TLS (never sensible outside tests and local relays).
	AllowInsecureAuth bool
	// TLSConfig is the TLS configuration; nil uses the system roots with
	// ServerName set to Host.
	TLSConfig *tls.Config
	// LocalName is the name sent with EHLO; empty means "localhost".
	LocalName string
	// Timeout bounds a whole delivery; 0 means 30 seconds.
	Timeout time.Duration
	// Clock dates the messages; nil means core.SystemClock.
	Clock core.Clock
}

// SMTPSender delivers messages over SMTP, one connection per message.
type SMTPSender struct {
	cfg SMTPConfig
}

var _ Sender = (*SMTPSender)(nil)

// NewSMTPSender validates cfg and returns a sender.
func NewSMTPSender(cfg SMTPConfig) (*SMTPSender, error) {
	if cfg.Host == "" {
		return nil, errors.New("smtp: missing host")
	}
	if cfg.Security == "" {
		cfg.Security = SecuritySTARTTLS
	}
	switch cfg.Security {
	case SecuritySTARTTLS, SecuritySTARTTLSOptional, SecurityTLS, SecurityNone:
	default:
		return nil, fmt.Errorf("smtp: unknown security %q", cfg.Security)
	}
	if cfg.Auth == "" {
		cfg.Auth = AuthAuto
	}
	switch cfg.Auth {
	case AuthAuto, AuthPlain, AuthLogin:
	default:
		return nil, fmt.Errorf("smtp: unknown auth mechanism %q", cfg.Auth)
	}
	if cfg.Port == 0 {
		cfg.Port = 587
		if cfg.Security == SecurityTLS {
			cfg.Port = 465
		}
	}
	if cfg.LocalName == "" {
		cfg.LocalName = "localhost"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.Clock == nil {
		cfg.Clock = core.SystemClock{}
	}
	return &SMTPSender{cfg: cfg}, nil
}

func (s *SMTPSender) tlsConfig() *tls.Config {
	if s.cfg.TLSConfig != nil {
		c := s.cfg.TLSConfig.Clone()
		if c.ServerName == "" {
			c.ServerName = s.cfg.Host
		}
		return c
	}
	return &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}
}

// Send implements [Sender]. The delivery is bounded by the context and the
// configured timeout; canceling the context aborts it.
func (s *SMTPSender) Send(ctx context.Context, m *Message) (err error) {
	data, err := m.Encode(EncodeOptions{Date: s.cfg.Clock.Now()})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp dial %s: %w", addr, err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer func() {
		stop()
		if ctx.Err() != nil && err != nil {
			err = fmt.Errorf("%w (%w)", err, ctx.Err())
		}
	}()
	if dl, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(dl); err != nil {
			_ = conn.Close()
			return fmt.Errorf("smtp deadline: %w", err)
		}
	}
	if s.cfg.Security == SecurityTLS {
		tc := tls.Client(conn, s.tlsConfig())
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return fmt.Errorf("smtp tls handshake: %w", err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp greeting: %w", err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Hello(s.cfg.LocalName); err != nil {
		return fmt.Errorf("smtp ehlo: %w", err)
	}
	secure := s.cfg.Security == SecurityTLS
	if s.cfg.Security == SecuritySTARTTLS || s.cfg.Security == SecuritySTARTTLSOptional {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(s.tlsConfig()); err != nil {
				return fmt.Errorf("smtp starttls: %w", err)
			}
			secure = true
		} else if s.cfg.Security == SecuritySTARTTLS {
			return errors.New("smtp: server does not offer STARTTLS")
		}
	}
	if s.cfg.Username != "" {
		if !secure && !s.cfg.AllowInsecureAuth {
			return errors.New("smtp: refusing to authenticate without TLS")
		}
		a, err := s.auth(c)
		if err != nil {
			return err
		}
		if err := c.Auth(a); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(m.From.Address); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	for _, r := range m.Recipients() {
		if err := c.Rcpt(r); err != nil {
			return fmt.Errorf("smtp rcpt to %s: %w", r, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if err := c.Quit(); err != nil {
		return fmt.Errorf("smtp quit: %w", err)
	}
	return nil
}

func (s *SMTPSender) auth(c *smtp.Client) (smtp.Auth, error) {
	mech := s.cfg.Auth
	if mech == AuthAuto {
		ok, params := c.Extension("AUTH")
		if !ok {
			return nil, errors.New("smtp: server does not offer AUTH")
		}
		mechs := strings.Fields(strings.ToUpper(params))
		switch {
		case slices.Contains(mechs, "PLAIN"):
			mech = AuthPlain
		case slices.Contains(mechs, "LOGIN"):
			mech = AuthLogin
		default:
			return nil, fmt.Errorf("smtp: no supported AUTH mechanism in %q", params)
		}
	}
	if mech == AuthLogin {
		return &loginAuth{user: s.cfg.Username, pass: s.cfg.Password}, nil
	}
	return &plainAuth{user: s.cfg.Username, pass: s.cfg.Password}, nil
}

// plainAuth is RFC 4616 PLAIN. Unlike smtp.PlainAuth it lets the sender
// decide whether TLS is required (AllowInsecureAuth).
type plainAuth struct{ user, pass string }

func (a *plainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
}

func (a *plainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("smtp: unexpected PLAIN challenge")
	}
	return nil, nil
}

// loginAuth is the LOGIN mechanism: the server prompts for the user name,
// then the password.
type loginAuth struct{ user, pass string }

func (a *loginAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(challenge []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(challenge))) {
	case "username:", "user name", "username":
		return []byte(a.user), nil
	case "password:", "password":
		return []byte(a.pass), nil
	default:
		return nil, fmt.Errorf("smtp: unexpected LOGIN challenge %q", challenge)
	}
}

// IsTemporary reports whether err is a transient SMTP failure worth
// retrying: a 4xx reply, a timeout or a broken connection (lago retries
// Net::SMTPServerBusy, timeouts and EOF in SendEmailJob).
func IsTemporary(err error) bool {
	var te *textproto.Error
	if errors.As(err, &te) {
		return te.Code >= 400 && te.Code < 500
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, net.ErrClosed)
}
