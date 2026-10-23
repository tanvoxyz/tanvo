# Unported lago-api cases

The lago-api specs `spec/services/taxes/{create,update,destroy}_service_spec.rb`
are ported into `service_test.go`. Each ported case has a `// source:` comment.
This file lists the cases that were not ported, grouped by reason.

## Merged into other cases

- `create_service_spec.rb:25`, `update_service_spec.rb:37`: "returns tax in the result". Every ported case reads the returned tax.

## Applied-tax snapshots

- `destroy_service_spec.rb:30` (draft parts): lago deletes the applied taxes of draft fees and draft invoices, of credit notes and of rate cards. Tanvo's invoice and credit-note applied taxes are snapshots without a foreign key (domain §7.7), and a draft's are recomputed by the refresh the deletion requests. The join records of customers, billing entities, plans, charges, fixed charges, add-ons and commitments are ported.

## Soft delete

- `destroy_service_spec.rb:16` ("change(Tax, :count).by(-1)") is ported as a hard delete: Tanvo deletes the tax row and cascades its join tables (`schema/02_tax.sql`), where lago sets `deleted_at`. No reader of a deleted tax remains: issued documents keep their snapshots.
