package webhooks

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The reference vector of the Standard Webhooks specification
// (standard-webhooks/standard-webhooks, libraries' shared tests).
const (
	vectorSecret = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
	vectorID     = "msg_p5jXN8AQM9LWM0D4loKWxJek"
	vectorBody   = `{"test": 2432232314}`
	vectorSig    = "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="
)

var vectorTime = time.Unix(1614265330, 0)

func TestSignVector(t *testing.T) {
	got, err := Sign(vectorSecret, vectorID, vectorTime, []byte(vectorBody))
	if err != nil || got != vectorSig {
		t.Errorf("Sign: got = %s %v, want = %s", got, err, vectorSig)
	}
}

func TestVerify(t *testing.T) {
	const other = "whsec_dGhpcyBpcyBhbm90aGVyIHNlY3JldCBrZXkhIQ=="
	headers := func(id, ts, sig string) http.Header {
		h := http.Header{}
		h.Set(HeaderID, id)
		h.Set(HeaderTimestamp, ts)
		h.Set(HeaderSignature, sig)
		return h
	}
	now := vectorTime.Add(time.Minute)
	tests := []struct {
		name   string
		secret string
		h      http.Header
		body   string
		now    time.Time
		want   error
	}{
		{"valid", vectorSecret, headers(vectorID, "1614265330", vectorSig), vectorBody, now, nil},
		{"one of several signatures", vectorSecret, headers(vectorID, "1614265330", "v1,Zm9v v2,abc "+vectorSig), vectorBody, now, nil},
		{"secret without prefix", strings.TrimPrefix(vectorSecret, "whsec_"), headers(vectorID, "1614265330", vectorSig), vectorBody, now, nil},
		{"tampered body", vectorSecret, headers(vectorID, "1614265330", vectorSig), `{"test": 1}`, now, ErrSignature},
		{"other id", vectorSecret, headers("msg_other", "1614265330", vectorSig), vectorBody, now, ErrSignature},
		{"other secret", other, headers(vectorID, "1614265330", vectorSig), vectorBody, now, ErrSignature},
		{"unknown version only", vectorSecret, headers(vectorID, "1614265330", "v2"+vectorSig[2:]), vectorBody, now, ErrSignature},
		{"too old", vectorSecret, headers(vectorID, "1614265330", vectorSig), vectorBody, vectorTime.Add(6 * time.Minute), ErrTimestamp},
		{"too new", vectorSecret, headers(vectorID, "1614265330", vectorSig), vectorBody, vectorTime.Add(-6 * time.Minute), ErrTimestamp},
		{"bad timestamp", vectorSecret, headers(vectorID, "soon", vectorSig), vectorBody, now, ErrTimestamp},
		{"missing headers", vectorSecret, headers(vectorID, "", vectorSig), vectorBody, now, ErrMissingHeaders},
		{"invalid secret", "whsec_!!", headers(vectorID, "1614265330", vectorSig), vectorBody, now, ErrInvalidSecret},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Verify(tt.secret, tt.h, []byte(tt.body), tt.now, 0); !errors.Is(err, tt.want) {
				t.Errorf("got = %v, want = %v", err, tt.want)
			}
		})
	}
}

func TestSetHeadersRotation(t *testing.T) {
	const next = "whsec_dGhpcyBpcyBhbm90aGVyIHNlY3JldCBrZXkhIQ=="
	h := http.Header{}
	if err := SetHeaders(h, []string{vectorSecret, next}, vectorID, vectorTime, []byte(vectorBody)); err != nil {
		t.Fatal(err)
	}
	if got := h.Get(HeaderTimestamp); got != "1614265330" {
		t.Errorf("timestamp: got = %s, want = 1614265330", got)
	}
	for _, s := range []string{vectorSecret, next} {
		if err := Verify(s, h, []byte(vectorBody), vectorTime, 0); err != nil {
			t.Errorf("Verify with %s: got = %v, want = nil", s, err)
		}
	}
}
