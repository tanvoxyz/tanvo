package mail

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	netmail "net/mail"
	"net/textproto"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// testCert returns a self-signed certificate for 127.0.0.1 and a pool
// trusting it.
func testCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "tanvo smtp test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// delivery is what the test server received in one transaction.
type delivery struct {
	from     string
	rcpts    []string
	data     []byte
	tls      bool
	authUser string
	mech     string
}

// smtpServer is a minimal in-process ESMTP server: EHLO, STARTTLS, AUTH
// PLAIN and LOGIN, MAIL, RCPT, DATA, RSET, NOOP and QUIT.
type smtpServer struct {
	ln          net.Listener
	tlsConf     *tls.Config
	pool        *x509.CertPool
	implicitTLS bool
	startTLS    bool
	authMechs   string
	user, pass  string
	rcptReply   string
	stall       bool

	mu   sync.Mutex
	got  []delivery
	wg   sync.WaitGroup
	done chan struct{}
}

func startSMTP(t *testing.T, configure func(*smtpServer)) *smtpServer {
	t.Helper()
	cert, pool := testCert(t)
	s := &smtpServer{
		tlsConf:  &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		pool:     pool,
		startTLS: true, authMechs: "PLAIN LOGIN", user: "user", pass: "secret",
		done: make(chan struct{}),
	}
	if configure != nil {
		configure(s)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if s.implicitTLS {
		ln = tls.NewListener(ln, s.tlsConf)
	}
	s.ln = ln
	s.wg.Add(1)
	go s.serve()
	t.Cleanup(func() {
		close(s.done)
		_ = ln.Close()
		s.wg.Wait()
	})
	return s
}

func (s *smtpServer) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *smtpServer) deliveries() []delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.got)
}

func (s *smtpServer) serve() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { _ = c.Close() }()
			s.handle(c)
		}()
	}
}

func (s *smtpServer) handle(conn net.Conn) {
	if s.stall {
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
		}
		return
	}
	_, secure := conn.(*tls.Conn)
	tp := textproto.NewConn(conn)
	reply := func(format string, args ...any) bool { return tp.PrintfLine(format, args...) == nil }
	if !reply("220 tanvo.test ESMTP") {
		return
	}
	var d delivery
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			ext := []string{"tanvo.test", "8BITMIME"}
			if s.startTLS && !secure {
				ext = append(ext, "STARTTLS")
			}
			if s.authMechs != "" {
				ext = append(ext, "AUTH "+s.authMechs)
			}
			for i, e := range ext {
				sep := "-"
				if i == len(ext)-1 {
					sep = " "
				}
				reply("250%s%s", sep, e)
			}
		case "STARTTLS":
			reply("220 ready")
			tc := tls.Server(conn, s.tlsConf)
			if err := tc.Handshake(); err != nil {
				return
			}
			conn, secure = tc, true
			tp = textproto.NewConn(tc)
			reply = func(format string, args ...any) bool { return tp.PrintfLine(format, args...) == nil }
		case "AUTH":
			mech, initial, _ := strings.Cut(arg, " ")
			var user, pass string
			switch strings.ToUpper(mech) {
			case "PLAIN":
				b, err := base64.StdEncoding.DecodeString(initial)
				parts := strings.Split(string(b), "\x00")
				if err != nil || len(parts) != 3 {
					reply("501 malformed")
					continue
				}
				user, pass = parts[1], parts[2]
			case "LOGIN":
				read := func(prompt string) (string, bool) {
					reply("334 %s", base64.StdEncoding.EncodeToString([]byte(prompt)))
					l, err := tp.ReadLine()
					if err != nil {
						return "", false
					}
					b, err := base64.StdEncoding.DecodeString(l)
					return string(b), err == nil
				}
				var ok bool
				if user, ok = read("Username:"); !ok {
					return
				}
				if pass, ok = read("Password:"); !ok {
					return
				}
			default:
				reply("504 unrecognized mechanism")
				continue
			}
			if user != s.user || pass != s.pass {
				reply("535 5.7.8 authentication failed")
				continue
			}
			d.authUser, d.mech = user, strings.ToUpper(mech)
			reply("235 2.7.0 authenticated")
		case "MAIL":
			d.from, d.tls = reversePath(arg), secure
			reply("250 ok")
		case "RCPT":
			if s.rcptReply != "" {
				reply("%s", s.rcptReply)
				continue
			}
			d.rcpts = append(d.rcpts, reversePath(arg))
			reply("250 ok")
		case "DATA":
			reply("354 go ahead")
			b, err := tp.ReadDotBytes()
			if err != nil {
				return
			}
			d.data = b
			s.mu.Lock()
			s.got = append(s.got, d)
			s.mu.Unlock()
			d = delivery{authUser: d.authUser, mech: d.mech}
			reply("250 queued")
		case "RSET", "NOOP":
			reply("250 ok")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 not implemented")
		}
	}
}

// reversePath returns the address between angle brackets of a MAIL or
// RCPT argument, ignoring parameters such as BODY=8BITMIME.
func reversePath(arg string) string {
	_, rest, _ := strings.Cut(arg, "<")
	addr, _, _ := strings.Cut(rest, ">")
	return addr
}

func smtpConfig(s *smtpServer) SMTPConfig {
	return SMTPConfig{
		Host: "127.0.0.1", Port: s.port(), Username: s.user, Password: s.pass,
		TLSConfig: &tls.Config{RootCAs: s.pool, MinVersion: tls.VersionTLS12},
		Timeout:   5 * time.Second,
		Clock:     core.FixedClock(time.Date(2024, time.October, 3, 12, 0, 0, 0, time.UTC)),
	}
}

func TestSMTPSender(t *testing.T) {
	tests := []struct {
		name      string
		server    func(*smtpServer)
		config    func(*SMTPConfig)
		wantErr   bool
		temporary bool
		wantTLS   bool
		wantMech  string
	}{
		{name: "starttls with auto plain", wantTLS: true, wantMech: "PLAIN"},
		{name: "starttls with login", config: func(c *SMTPConfig) { c.Auth = AuthLogin }, wantTLS: true, wantMech: "LOGIN"},
		{name: "auto falls back to login", server: func(s *smtpServer) { s.authMechs = "CRAM-MD5 LOGIN" }, wantTLS: true, wantMech: "LOGIN"},
		{name: "auto without a known mechanism", server: func(s *smtpServer) { s.authMechs = "CRAM-MD5" }, wantErr: true},
		{
			name: "implicit tls", server: func(s *smtpServer) { s.implicitTLS, s.startTLS = true, false },
			config: func(c *SMTPConfig) { c.Security = SecurityTLS }, wantTLS: true, wantMech: "PLAIN",
		},
		{name: "starttls required but not offered", server: func(s *smtpServer) { s.startTLS = false }, wantErr: true},
		{
			name: "starttls optional and not offered", server: func(s *smtpServer) { s.startTLS, s.authMechs = false, "" },
			config: func(c *SMTPConfig) { c.Security, c.Username = SecuritySTARTTLSOptional, "" },
		},
		{
			name: "auth without tls refused", server: func(s *smtpServer) { s.startTLS = false },
			config: func(c *SMTPConfig) { c.Security = SecurityNone }, wantErr: true,
		},
		{
			name: "insecure auth allowed", server: func(s *smtpServer) { s.startTLS = false },
			config: func(c *SMTPConfig) { c.Security, c.AllowInsecureAuth = SecurityNone, true }, wantMech: "PLAIN",
		},
		{name: "wrong password", config: func(c *SMTPConfig) { c.Password = "nope" }, wantErr: true},
		{name: "untrusted certificate", config: func(c *SMTPConfig) { c.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12} }, wantErr: true},
		{name: "temporary rcpt failure", server: func(s *smtpServer) { s.rcptReply = "450 4.2.1 mailbox busy" }, wantErr: true, temporary: true},
		{name: "permanent rcpt failure", server: func(s *smtpServer) { s.rcptReply = "550 5.1.1 no such user" }, wantErr: true},
		{
			name: "timeout", server: func(s *smtpServer) { s.stall = true },
			config: func(c *SMTPConfig) { c.Timeout = 200 * time.Millisecond }, wantErr: true, temporary: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := startSMTP(t, tt.server)
			cfg := smtpConfig(srv)
			if tt.config != nil {
				tt.config(&cfg)
			}
			s, err := NewSMTPSender(cfg)
			if err != nil {
				t.Fatalf("NewSMTPSender() error = %v", err)
			}
			m := sampleMessage()
			err = s.Send(t.Context(), m)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Send() error = %v, want error = %v", err, tt.wantErr)
			}
			if err != nil {
				if got := IsTemporary(err); got != tt.temporary {
					t.Errorf("IsTemporary(%v) = %v, want = %v", err, got, tt.temporary)
				}
				if len(srv.deliveries()) != 0 {
					t.Errorf("server received a message although Send failed")
				}
				return
			}
			got := srv.deliveries()
			if len(got) != 1 {
				t.Fatalf("server received %d messages, want = 1", len(got))
			}
			d := got[0]
			if d.tls != tt.wantTLS || d.mech != tt.wantMech {
				t.Errorf("tls, mech = %v, %q, want = %v, %q", d.tls, d.mech, tt.wantTLS, tt.wantMech)
			}
			if d.from != m.From.Address {
				t.Errorf("MAIL FROM = %q, want = %q", d.from, m.From.Address)
			}
			if want := m.Recipients(); !slices.Equal(d.rcpts, want) {
				t.Errorf("RCPT TO = %v, want = %v", d.rcpts, want)
			}
			pm, err := netmail.ReadMessage(bytes.NewReader(d.data))
			if err != nil {
				t.Fatalf("ReadMessage() error = %v", err)
			}
			if date, _ := pm.Header.Date(); !date.Equal(time.Time(cfg.Clock.(core.FixedClock))) {
				t.Errorf("Date = %v, want the configured clock", date)
			}
			if strings.Contains(string(d.data), "hidden@example.com") {
				t.Errorf("DATA reveals the Bcc")
			}
		})
	}
}

func TestSMTPSenderCancel(t *testing.T) {
	srv := startSMTP(t, func(s *smtpServer) { s.stall = true })
	s, err := NewSMTPSender(smtpConfig(srv))
	if err != nil {
		t.Fatalf("NewSMTPSender() error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	err = s.Send(ctx, sampleMessage())
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Send() error = %v, want = %v", err, context.Canceled)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Send() returned after %v, want it aborted promptly", d)
	}
}

func TestNewSMTPSender(t *testing.T) {
	tests := []struct {
		cfg      SMTPConfig
		wantErr  bool
		wantPort int
	}{
		{SMTPConfig{}, true, 0},
		{SMTPConfig{Host: "smtp.example.com", Security: "ssl"}, true, 0},
		{SMTPConfig{Host: "smtp.example.com", Auth: "cram-md5"}, true, 0},
		{SMTPConfig{Host: "smtp.example.com"}, false, 587},
		{SMTPConfig{Host: "smtp.example.com", Security: SecurityTLS}, false, 465},
		{SMTPConfig{Host: "smtp.example.com", Port: 2525}, false, 2525},
	}
	for _, tt := range tests {
		s, err := NewSMTPSender(tt.cfg)
		if (err != nil) != tt.wantErr {
			t.Errorf("NewSMTPSender(%+v) error = %v, want error = %v", tt.cfg, err, tt.wantErr)
			continue
		}
		if err == nil && s.cfg.Port != tt.wantPort {
			t.Errorf("NewSMTPSender(%+v) port = %d, want = %d", tt.cfg, s.cfg.Port, tt.wantPort)
		}
	}
}

func TestIsTemporary(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{&textproto.Error{Code: 421, Msg: "busy"}, true},
		{fmt.Errorf("rcpt: %w", &textproto.Error{Code: 451, Msg: "later"}), true},
		{&textproto.Error{Code: 550, Msg: "no"}, false},
		{io.EOF, true},
		{context.DeadlineExceeded, true},
		{os.ErrDeadlineExceeded, true},
		{net.ErrClosed, true},
		{errors.New("smtp: server does not offer STARTTLS"), false},
		{nil, false},
	}
	for _, tt := range tests {
		if got := IsTemporary(tt.err); got != tt.want {
			t.Errorf("IsTemporary(%v) = %v, want = %v", tt.err, got, tt.want)
		}
	}
}
