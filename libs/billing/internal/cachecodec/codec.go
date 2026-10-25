package cachecodec

import (
	stdjson "encoding/json"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// Marshal encodes v.
func Marshal(v any) ([]byte, error) {
	return json.Marshal(v, options())
}

// Unmarshal decodes b into v, a pointer.
func Unmarshal(b []byte, v any) error {
	return json.Unmarshal(b, v, options())
}

func options() json.Options {
	return json.JoinOptions(
		json.FormatNilSliceAsNull(true),
		json.FormatNilMapAsNull(true),
		json.WithMarshalers(json.JoinMarshalers(
			json.MarshalToFunc(marshalCurrency),
			json.MarshalToFunc(marshalRaw),
		)),
		json.WithUnmarshalers(json.JoinUnmarshalers(
			json.UnmarshalFromFunc(unmarshalCurrency),
			json.UnmarshalFromFunc(unmarshalRaw),
		)),
	)
}

// currency is the encoding of a [money.Currency]: an ISO currency is
// found again by its code, a pricing unit is rebuilt from its parts.
type currency struct {
	Code     string `json:"code"`
	Name     string `json:"name,omitempty"`
	Exponent int32  `json:"exponent,omitempty"`
	Custom   bool   `json:"custom,omitempty"`
}

func marshalCurrency(enc *jsontext.Encoder, c money.Currency) error {
	w := currency{Code: c.Code()}
	if c.IsCustom() {
		w.Name, w.Exponent, w.Custom = c.Name(), c.Exponent(), true
	}
	return json.MarshalEncode(enc, w)
}

func unmarshalCurrency(dec *jsontext.Decoder, c *money.Currency) error {
	var w currency
	if err := json.UnmarshalDecode(dec, &w); err != nil {
		return err
	}
	var err error
	switch {
	case w.Code == "":
		*c = money.Currency{}
	case w.Custom:
		*c, err = money.NewCustomCurrency(w.Code, w.Name, w.Exponent)
	default:
		*c, err = money.LookupCurrency(w.Code)
	}
	if err != nil {
		return fmt.Errorf("cachecodec: currency: %w", err)
	}
	return nil
}

// marshalRaw keeps the bytes of a raw message as they are (base64), nil
// as null.
func marshalRaw(enc *jsontext.Encoder, m stdjson.RawMessage) error {
	if m == nil {
		return enc.WriteToken(jsontext.Null)
	}
	return json.MarshalEncode(enc, []byte(m))
}

func unmarshalRaw(dec *jsontext.Decoder, m *stdjson.RawMessage) error {
	if dec.PeekKind() == 'n' {
		if _, err := dec.ReadToken(); err != nil {
			return err
		}
		*m = nil
		return nil
	}
	var b []byte
	if err := json.UnmarshalDecode(dec, &b); err != nil {
		return err
	}
	*m = b
	return nil
}
