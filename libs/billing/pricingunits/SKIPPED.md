# Unported lago-api cases

The lago-api specs `spec/services/pricing_units/{create,update}_service_spec.rb`
are ported into `service_test.go`. Each ported case has a `// source:` comment.
This file lists the cases that were not ported, grouped by reason.

## License gate

- `create_service_spec.rb:58-87`, `update_service_spec.rb:78-131`: the freemium organization fails with `forbidden`. Features behind lago's premium license are part of the core with no gate (architecture §14), so the premium cases are the ported ones.
