package cli_test

import (
	"bufio"
	"mime"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"sync"
	"testing"
)

// fakeSMTP is a plain ESMTP server without authentication that keeps the
// messages it receives.
type fakeSMTP struct {
	t  *testing.T
	ln net.Listener

	mu   sync.Mutex
	msgs []smtpMessage
}

// smtpMessage is a message the fake SMTP server received.
type smtpMessage struct {
	From string
	To   []string
	Msg  *mail.Message
	Raw  string
}

// Subject returns the message's decoded subject.
func (m smtpMessage) Subject() string {
	s, err := new(mime.WordDecoder).DecodeHeader(m.Msg.Header.Get("Subject"))
	if err != nil {
		return m.Msg.Header.Get("Subject")
	}
	return s
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{t: t, ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go f.accept()
	return f
}

// environ returns the server environment sending through f.
func (f *fakeSMTP) environ() []string {
	host, port, _ := net.SplitHostPort(f.ln.Addr().String())
	return []string{
		"TANVO_SMTP_HOST=" + host, "TANVO_SMTP_PORT=" + port, "TANVO_SMTP_SECURITY=none",
		"TANVO_MAIL_FROM=Tanvo <noreply@tanvo.test>",
	}
}

// messages returns the messages received so far.
func (f *fakeSMTP) messages() []smtpMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]smtpMessage(nil), f.msgs...)
}

func (f *fakeSMTP) accept() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.session(c)
	}
}

func (f *fakeSMTP) session(c net.Conn) {
	defer c.Close()
	tc := textproto.NewConn(c)
	_ = tc.PrintfLine("220 fake ESMTP")
	var cur smtpMessage
	for {
		line, err := tc.ReadLine()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			_ = tc.PrintfLine("250-fake")
			_ = tc.PrintfLine("250-8BITMIME")
			_ = tc.PrintfLine("250 SMTPUTF8")
		case "MAIL":
			cur = smtpMessage{From: addrArg(arg)}
			_ = tc.PrintfLine("250 OK")
		case "RCPT":
			cur.To = append(cur.To, addrArg(arg))
			_ = tc.PrintfLine("250 OK")
		case "DATA":
			_ = tc.PrintfLine("354 go ahead")
			b, err := tc.ReadDotBytes()
			if err != nil {
				return
			}
			cur.Raw = string(b)
			m, err := mail.ReadMessage(bufio.NewReader(strings.NewReader(cur.Raw)))
			if err != nil {
				f.t.Errorf("smtp: parse message: %v", err)
				_ = tc.PrintfLine("554 bad message")
				continue
			}
			cur.Msg = m
			f.mu.Lock()
			f.msgs = append(f.msgs, cur)
			f.mu.Unlock()
			_ = tc.PrintfLine("250 OK")
		case "RSET", "NOOP":
			_ = tc.PrintfLine("250 OK")
		case "QUIT":
			_ = tc.PrintfLine("221 bye")
			return
		default:
			_ = tc.PrintfLine("502 not implemented")
		}
	}
}

// addrArg returns the address of a "FROM:<a>" or "TO:<a>" argument.
func addrArg(arg string) string {
	_, a, _ := strings.Cut(arg, ":")
	a, _, _ = strings.Cut(strings.TrimSpace(a), " ")
	return strings.Trim(a, "<>")
}
