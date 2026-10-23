package validators

import "github.com/tanvoxyz/tanvo/libs/core"

// collector gathers errors grouped by field in the order each field first
// failed, as lago's error hash does (duplicates kept).
type collector struct {
	fields []string
	codes  map[string][]string
}

func (c *collector) add(field, code string) {
	if c.codes == nil {
		c.codes = map[string][]string{}
	}
	if _, ok := c.codes[field]; !ok {
		c.fields = append(c.fields, field)
	}
	c.codes[field] = append(c.codes[field], code)
}

func (c *collector) empty() bool { return len(c.fields) == 0 }

// err returns the errors as a *core.ValidationError, nil when there are
// none.
func (c *collector) err() error {
	if c.empty() {
		return nil
	}
	out := &core.ValidationError{}
	for _, f := range c.fields {
		for _, code := range c.codes[f] {
			out.Errors = append(out.Errors, core.FieldError{Field: f, Code: code})
		}
	}
	return out
}
