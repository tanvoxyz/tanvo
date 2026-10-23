package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"sync"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectproto"
	"connectrpc.com/vanguard"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// JSONCodec is the Connect JSON codec of the API: protobuf JSON with the
// proto field names, which keeps the numbers of event properties exact.
//
// Event properties are a google.protobuf.Struct, whose numbers are
// doubles: decoded as such, 12345678901234567890 or 0.1234567890123456789
// would reach the intake rounded. Before decoding a request that holds
// tanvo.event.v1.Event messages, the codec rewrites each number among
// their properties as a JSON string of its exact decimal text, in plain
// notation (1e3 becomes "1000"), which is the text the intake stores for a
// number. Numbers with an exponent beyond ±1000 are left to the double
// decoding. Binary (protobuf, gRPC) clients send the doubles they have;
// they send strings for values a double cannot hold.
type JSONCodec struct {
	*connectproto.JSONCodec

	exact *exactNumbers
}

var _ connect.Codec = (*JSONCodec)(nil)

// NewJSONCodec returns the Connect JSON codec.
func NewJSONCodec() *JSONCodec {
	c := connectproto.NewJSONCodec()
	c.MarshalOptions.UseProtoNames = true
	return &JSONCodec{JSONCodec: c, exact: &exactNumbers{}}
}

// UnmarshalRead implements [connect.Codec].
func (c *JSONCodec) UnmarshalRead(ctx context.Context, src io.Reader, msg any) error {
	src, err := c.exact.read(src, msg)
	if err != nil {
		return err
	}
	return c.JSONCodec.UnmarshalRead(ctx, src, msg)
}

// RESTCodec is the JSON codec of the REST transcoding, with the proto
// field names, which keeps the numbers of event properties exact as
// [JSONCodec] does.
type RESTCodec struct {
	*vanguard.JSONCodec

	exact *exactNumbers
}

var _ vanguard.RESTCodec = (*RESTCodec)(nil)

// NewRESTCodec returns the REST codec resolving types with res.
func NewRESTCodec(res connectproto.TypeResolver) *RESTCodec {
	c := vanguard.NewJSONCodec(res)
	c.MarshalOptions.UseProtoNames = true
	return &RESTCodec{JSONCodec: c, exact: &exactNumbers{}}
}

// UnmarshalRead implements [connect.Codec]: a body mapped to the whole
// request (body: "*").
func (c *RESTCodec) UnmarshalRead(ctx context.Context, src io.Reader, msg any) error {
	src, err := c.exact.read(src, msg)
	if err != nil {
		return err
	}
	return c.JSONCodec.UnmarshalRead(ctx, src, msg)
}

// UnmarshalField implements [vanguard.RESTCodec]: a body mapped to one
// field of the request (body: "event").
func (c *RESTCodec) UnmarshalField(ctx context.Context, data []byte, msg proto.Message, field protoreflect.FieldDescriptor) error {
	if md := field.Message(); md != nil && !field.IsMap() && c.exact.reaches(md) {
		if field.IsList() {
			data, _ = c.exact.list(data, md)
		} else {
			data, _ = c.exact.message(data, md)
		}
	}
	return c.JSONCodec.UnmarshalField(ctx, data, msg, field)
}

// eventMessage is the message whose properties are rewritten.
const eventMessage protoreflect.FullName = "tanvo.event.v1.Event"

// maxExponent bounds the exponent of the numbers rewritten, and so the
// length of their plain notation.
const maxExponent = 1000

// exactNumbers rewrites the JSON of requests so that the numbers of event
// properties decode exactly. JSON it cannot parse is left as it is, for
// protojson to report.
type exactNumbers struct {
	reach sync.Map // protoreflect.FullName → bool
}

// read rewrites the request read from src, when msg can hold events.
func (x *exactNumbers) read(src io.Reader, msg any) (io.Reader, error) {
	m, ok := msg.(proto.Message)
	if !ok {
		return src, nil
	}
	md := m.ProtoReflect().Descriptor()
	if !x.reaches(md) {
		return src, nil
	}
	data, err := io.ReadAll(src)
	if err != nil {
		return nil, err
	}
	data, _ = x.message(data, md)
	return bytes.NewReader(data), nil
}

// reaches reports whether a message of md can hold an event.
func (x *exactNumbers) reaches(md protoreflect.MessageDescriptor) bool {
	if v, ok := x.reach.Load(md.FullName()); ok {
		return v.(bool)
	}
	r := reaches(md, map[protoreflect.FullName]bool{})
	x.reach.Store(md.FullName(), r)
	return r
}

func reaches(md protoreflect.MessageDescriptor, seen map[protoreflect.FullName]bool) bool {
	name := md.FullName()
	if name == eventMessage {
		return true
	}
	// The well-known types have JSON forms of their own.
	if seen[name] || name.Parent() == "google.protobuf" {
		return false
	}
	seen[name] = true
	fields := md.Fields()
	for i := range fields.Len() {
		f := fields.Get(i)
		if m := f.Message(); m != nil && !f.IsMap() && reaches(m, seen) {
			return true
		}
	}
	return false
}

// message rewrites data, the JSON of a message of md, and reports whether
// it changed.
func (x *exactNumbers) message(data []byte, md protoreflect.MessageDescriptor) ([]byte, bool) {
	members, ok := object(data)
	if !ok {
		return data, false
	}
	fields := md.Fields()
	changed := false
	for i, m := range members {
		f := fields.ByName(protoreflect.Name(m.key))
		if f == nil {
			f = fields.ByJSONName(m.key)
		}
		if f == nil || f.IsMap() || f.Message() == nil {
			continue
		}
		var (
			v []byte
			c bool
		)
		switch {
		case md.FullName() == eventMessage && f.Name() == "properties":
			v, c = properties(m.value)
		case !x.reaches(f.Message()):
			continue
		case f.IsList():
			v, c = x.list(m.value, f.Message())
		default:
			v, c = x.message(m.value, f.Message())
		}
		if c {
			members[i].value, changed = v, true
		}
	}
	if !changed {
		return data, false
	}
	return encodeObject(members), true
}

// list rewrites data, the JSON of a list of messages of md, and reports
// whether it changed.
func (x *exactNumbers) list(data []byte, md protoreflect.MessageDescriptor) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if t, err := dec.Token(); err != nil || t != json.Delim('[') {
		return data, false
	}
	var items []json.RawMessage
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return data, false
		}
		items = append(items, raw)
	}
	if !closed(dec, ']') {
		return data, false
	}
	changed := false
	for i, item := range items {
		if v, c := x.message(item, md); c {
			items[i], changed = v, true
		}
	}
	if !changed {
		return data, false
	}
	var b bytes.Buffer
	b.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(item)
	}
	b.WriteByte(']')
	return b.Bytes(), true
}

// properties rewrites the numbers of data, the JSON of event properties,
// as strings of their exact decimal text.
func properties(data []byte) ([]byte, bool) {
	members, ok := object(data)
	if !ok {
		return data, false
	}
	changed := false
	for i, m := range members {
		s, ok := exactText(m.value)
		if !ok {
			continue
		}
		v, err := json.Marshal(s)
		if err != nil {
			continue
		}
		members[i].value, changed = v, true
	}
	if !changed {
		return data, false
	}
	return encodeObject(members), true
}

// exactText returns the plain decimal text of raw when it is a JSON number
// within [maxExponent].
func exactText(raw json.RawMessage) (string, bool) {
	tok := string(bytes.TrimSpace(raw))
	if tok == "" || (tok[0] != '-' && (tok[0] < '0' || tok[0] > '9')) {
		return "", false
	}
	if i := strings.IndexAny(tok, "eE"); i >= 0 {
		exp, err := strconv.Atoi(tok[i+1:])
		if err != nil || exp < -maxExponent || exp > maxExponent {
			return "", false
		}
	}
	d, err := money.Parse(tok)
	if err != nil {
		return "", false
	}
	return d.String(), true
}

// member is a member of a JSON object.
type member struct {
	key   string
	value json.RawMessage
}

// object splits data, a JSON object, into its members, in order and with
// any repeated key.
func object(data []byte) ([]member, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, false
	}
	var members []member
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, ok := t.(string)
		if !ok {
			return nil, false
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, false
		}
		members = append(members, member{key: key, value: raw})
	}
	return members, closed(dec, '}')
}

// closed reports whether the next token of dec closes with delim and is
// the end of the input.
func closed(dec *json.Decoder, delim json.Delim) bool {
	if t, err := dec.Token(); err != nil || t != delim {
		return false
	}
	_, err := dec.Token()
	return err == io.EOF
}

func encodeObject(members []member) []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(m.key)
		b.Write(k)
		b.WriteByte(':')
		b.Write(m.value)
	}
	b.WriteByte('}')
	return b.Bytes()
}
