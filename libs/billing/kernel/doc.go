// Package kernel holds what every billing service shares: the dependencies
// of the engine ([Deps]), the ports billing defines on top of libs/core
// (tax provider, refund gateway, invoice credits), the loaded customer and
// subscription environments ([CustomerEnv], [SubscriptionEnv]), invoice
// primitives (numbering, dates, final status, taxes and totals), job
// argument types and small helpers.
//
// The packages of libs/billing depend on kernel and never on each other's
// internals; kernel depends only on libs/core and the pure libraries.
package kernel
