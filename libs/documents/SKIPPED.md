# lago-api specs not ported to libs/documents

Sources are lago-api at 25b8b7b09 (payment terms: 8ceddebb7). Every other
example of the template, helper and e-invoice specs is ported, with a
`source:` line in its test.

| Spec | Reason |
| :--- | :--- |
| `spec/views/app/views/templates/invoices/` v1–v3 snapshots | Legacy template versions; Tanvo renders only the v4 layout. |
| `fee_display_helper_spec.rb`: product fee titles, product filters | Products are not in the core model. |
| `invoice_subscriptions_helper_spec.rb`: invalid datetime and nil properties in `billing_period` | Periods are typed `time.Time` values; neither case can be represented. |
| `fee_boundaries_helper_spec.rb:262`: commitment fee without properties on a plan paid in advance | Legacy lago commitment fees (`derive_commitment_billing_period`); every Tanvo commitment fee stores its reconciled period in its properties. |
| `invoice_subscriptions_helper_spec.rb`: `load_subscriptions_with_fees` | Database loading; covered by `FromCore`. |
| `credit_note.slim_spec.rb`: "different tax rates" | The spec stubs both items' rate to 20%, so its snapshot prints 20% twice; Tanvo prints each item's own rate. The case is ported with the stub artifact skipped. |
| `e_invoices` jobs and `generate_xml` services | Orchestration (jobs, blob attachment, premium `issue_receipts` checks) belongs to the server, not this library. |
| `e_invoices/*_spec.rb`: Nokogiri builder mocking | The examples that assert builder calls are covered by the byte-for-byte XML goldens instead. |
| `{invoices,credit_notes,payment_receipts}/generate_pdf_service_spec.rb`: Gotenberg HTTP calls | Tanvo compiles with a Typst child process; the rendering tests (`render_test.go`) replace them. |
