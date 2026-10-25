# Unported spec cases

Spec cases of the credit notes API that are not ported, each with its reason. Paths are relative to the upstream `spec/` directory. Ported cases carry a `source:` reference in the tests; together the two lists cover every case of the files in `testdata/specs.json` (checked by `go run ./tools/speccov`).

Cases: 169. Ported: 130. Unported: 39.

## Reasons

- **graphql** (33): GraphQL plumbing: the shared authentication and permission examples (requires current user/organization/permission) and the GraphQL type definitions. Tanvo serves Connect RPCs; authentication and dashboard permissions are libs/auth's, tested there, and the response shape is the proto of libs/proto. The API key permission examples are ported (TestCreditNotePermissions).
- **n-plus-one** (2): N+1 query detection of ActiveRecord includes, which has no counterpart: the store loads a page's items and applied taxes with one query each (libs/store creditNoteLoad) and the metadata with the credit note row; there is no lazy association loading.
- **one-api** (2): Kept deviation: one RPC, DownloadCreditNote, serves the REST and dashboard clients and follows the REST controller (credit_notes_controller.rb download_pdf and download_xml: enqueue the document job, answer empty, then return the links), where the dashboard mutation renders the document at once (mutations/credit_notes/download.rb). The invoice download does the same (docs/parity.md, Download PDF and XML).
- **attributed-feature-flag** (1): The account_tree feature flag of the attributed usage endpoint: Tanvo has no feature flags (parity §1.50); an organization whose events are not in the ClickHouse store gets the same PermissionDenied feature_unavailable (TestGetAttributedUsageUnavailable).
- **provider** (1): Needs a tax provider refusing the report. The refusal is ported at the service, which keeps it as a tax error and returns the error (libs/billing/creditnotes/refund_test.go, report_service_spec.rb:141); the RPC maps it like every service error.

The REST and GraphQL credit note endpoints are one Connect service, `CreditNoteService` (creditnotes.go), over libs/billing/creditnotes; the cases are ported against its REST transcoding in creditnotes_test.go and creditnotes_view_test.go. The resend mutation is ported through the document service behind `ResendCreditNote` (apps/server/internal/app/creditnote_resend_test.go).

The index cases of `spec/support/shared_examples/credit_note_index.rb` (billing entity codes, :332 and :350) are not `_spec.rb` files; they are ported in TestListCreditNoteFilters citing `credit_note_index.rb:8-9`.

## Cases

### graphql/resolvers/credit_note_resolver_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 55 | it_behaves_like requires current user | graphql |
| 56 | it_behaves_like requires current organization | graphql |
| 57 | it_behaves_like requires permission | graphql |

### graphql/resolvers/credit_notes_resolver_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 36 | it_behaves_like requires current user | graphql |
| 37 | it_behaves_like requires current organization | graphql |
| 38 | it_behaves_like requires permission | graphql |
| 365 | with N+1 queries detection on metadata > "does not trigger N+1 queries for metadata" | n-plus-one |
| 400 | with N+1 queries detection on items and fees > "does not trigger N+1 queries for items and fees" | n-plus-one |

### graphql/resolvers/invoice_credit_notes_resolver_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 31 | it_behaves_like requires current user | graphql |
| 32 | it_behaves_like requires current organization | graphql |
| 33 | it_behaves_like requires permission | graphql |

### graphql/resolvers/credit_notes/estimate_resolver_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 56 | it_behaves_like requires current user | graphql |
| 57 | it_behaves_like requires current organization | graphql |

### graphql/types/credit_notes/object_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/mutations/credit_notes/create_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 54 | it_behaves_like requires current user | graphql |
| 55 | it_behaves_like requires current organization | graphql |
| 56 | it_behaves_like requires permission | graphql |

### graphql/mutations/credit_notes/download_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 24 | it_behaves_like requires current user | graphql |
| 25 | it_behaves_like requires current organization | graphql |
| 26 | it_behaves_like requires permission | graphql |
| 28 | "generates the credit note PDF" | one-api |

### graphql/mutations/credit_notes/download_xml_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 30 | it_behaves_like requires current user | graphql |
| 31 | it_behaves_like requires current organization | graphql |
| 32 | it_behaves_like requires permission | graphql |
| 34 | "generates the credit note XML" | one-api |

### graphql/mutations/credit_notes/resend_email_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 31 | it_behaves_like requires current user | graphql |
| 32 | it_behaves_like requires current organization | graphql |
| 33 | it_behaves_like requires permission | graphql |

### graphql/mutations/credit_notes/retry_tax_reporting_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 96 | it_behaves_like requires current user | graphql |
| 97 | it_behaves_like requires current organization | graphql |
| 98 | it_behaves_like requires permission | graphql |
| 124 | when there is tax error > "returns the error" | provider |

### graphql/mutations/credit_notes/update_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 24 | it_behaves_like requires current user | graphql |
| 25 | it_behaves_like requires current organization | graphql |
| 26 | it_behaves_like requires permission | graphql |

### graphql/mutations/credit_notes/void_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 25 | it_behaves_like requires current user | graphql |
| 26 | it_behaves_like requires current organization | graphql |
| 27 | it_behaves_like requires permission | graphql |

### requests/api/v1/customers/attributed_usage_controller_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 292 | when the feature flag is disabled > "returns a forbidden error" | attributed-feature-flag |
