package usage

import "github.com/tanvoxyz/tanvo/libs/billing/kernel"

// Service is the usage service.
type Service struct {
	d *kernel.Deps
}

// New returns the service over the checked dependencies d.
func New(d *kernel.Deps) *Service { return &Service{d: d} }
