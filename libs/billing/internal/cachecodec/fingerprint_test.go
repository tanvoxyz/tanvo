package cachecodec

import (
	"testing"
	"time"
)

type shapeA struct {
	Amount int64
	At     time.Time
	Next   *shapeA
}

type shapeB struct {
	Amount int64
	At     time.Time
	Next   *shapeB
	Extra  string
}

type shapeC struct {
	Amount int32
	At     time.Time
	Next   *shapeC
}

func TestFingerprint(t *testing.T) {
	a := Fingerprint[shapeA]()
	if got := Fingerprint[shapeA](); got != a {
		t.Errorf("Fingerprint() got = %s then %s, want = stable", a, got)
	}
	for name, got := range map[string]string{
		"added field":   Fingerprint[shapeB](),
		"changed type":  Fingerprint[shapeC](),
		"wrapped slice": Fingerprint[[]shapeA](),
	} {
		if got == a {
			t.Errorf("Fingerprint() of %s got = %s, want = another than %s", name, got, a)
		}
	}
	if len(a) != 12 {
		t.Errorf("len(Fingerprint()) got = %d, want = 12", len(a))
	}
}
