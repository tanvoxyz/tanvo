// Package refs resolves the references of catalog objects that requests
// carry: an ID or a code (lago's services take codes, such as tax_codes;
// Tanvo's API takes an ID or `code:<code>`). A reference that matches no
// object fails with core.NotFound of its resource, as lago's
// not_found_failure! does.
package refs
