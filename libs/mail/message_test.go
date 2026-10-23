package mail

import (
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/port"
)

func TestFromPortEmail(t *testing.T) {
	tests := []struct {
		name    string
		e       port.Email
		wantTo  []string
		wantCc  []string
		wantBcc []string
		wantErr bool
	}{
		{
			name:   "to only",
			e:      port.Email{From: "Tanvo <noreply@tanvo.test>", To: []string{"a@example.com"}, Subject: "s", Text: "t"},
			wantTo: []string{"a@example.com"},
		},
		{
			name: "cc and bcc, lists and blanks",
			e: port.Email{
				From: "noreply@tanvo.test", To: []string{"a@example.com, b@example.com"},
				Cc: []string{"c@example.com", " "}, Bcc: []string{"Dee <d@example.com>"}, Subject: "s", Text: "t",
			},
			wantTo: []string{"a@example.com", "b@example.com"}, wantCc: []string{"c@example.com"}, wantBcc: []string{"d@example.com"},
		},
		{name: "invalid cc", e: port.Email{From: "noreply@tanvo.test", To: []string{"a@example.com"}, Cc: []string{"not an address"}}, wantErr: true},
	}
	addrs := func(as []Address) []string {
		out := make([]string, 0, len(as))
		for _, a := range as {
			out = append(out, a.Address)
		}
		return out
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := FromPortEmail(tt.e)
			if (err != nil) != tt.wantErr {
				t.Fatalf("FromPortEmail() error got = %v, want error = %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			for _, c := range []struct {
				field     string
				got, want []string
			}{{"To", addrs(m.To), tt.wantTo}, {"Cc", addrs(m.Cc), tt.wantCc}, {"Bcc", addrs(m.Bcc), tt.wantBcc}} {
				if len(c.want) == 0 {
					c.want = []string{}
				}
				if !slices.Equal(c.got, c.want) {
					t.Errorf("%s got = %v, want = %v", c.field, c.got, c.want)
				}
			}
		})
	}
}
