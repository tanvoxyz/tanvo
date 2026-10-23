// Package lifecycle bills subscription lifecycle changes (domain §9,
// §22.13, §22.14): creation and activation (with activation rules),
// upgrades, downgrades and their swap at the billing run, terminations,
// trial ends, and the sweeps that activate pending subscriptions and
// terminate ended ones. It drives invoicing through the [Biller] port and
// issues termination credit notes through [TerminationCrediter]; every
// change runs under the customer's billing schedule lock and the
// subscription lock and emits the subscription webhooks.
package lifecycle
