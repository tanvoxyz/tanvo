# Skipped spec cases (spec/services/data_exports/**)

Every lago data export service example not ported as a test in this package, with the reason.

| source | case | reason |
| :--- | :--- | :--- |
| spec/services/data_exports/combine_parts_service_spec.rb:42 | sends an email | The completed-export mail is not wired in apps/server yet |
| spec/services/data_exports/export_resources_service_spec.rb:128 | unsupported resource type | The resource type is an enumeration validated at creation; no unsupported type can be stored |
| spec/services/data_exports/csv/invoices_spec.rb:149 | preloaded offset amounts without additional queries | Query counting is an ActiveRecord concern; the loader caches each record by ID |
| spec/services/data_exports/csv/invoice_fees_spec.rb:54 | timezone-agnostic column names | The column names are the constants of InvoiceFeeHeaders (`fee_from_date`, `fee_to_date`) |
| spec/services/data_exports/csv/invoice_fees_spec.rb:251 | pay-in-advance charge billed in arrears | Same path as :235 (the fee's pay_in_advance decides) |
| spec/services/data_exports/csv/invoice_fees_spec.rb:349 | add-on fee in a negative offset | Add-on dates are the UTC dates of the stored boundaries, so the offset cannot shift them; :357 covers it |
| spec/services/data_exports/csv/resolve_fee_billing_period_service_spec.rb | fee billing period resolution | Covered through the invoice fee CSV cases; the resolver is unexported |
