# Unported spec cases

Spec cases of the document generation and the document emails that are not ported, each with its reason. Paths are relative to the upstream `spec/` directory. Ported cases carry a `source:` reference in the tests; together the two lists cover every case of the files in `testdata/specs.json` (checked by `go run ./tools/speccov`).

Cases: 135. Ported: 120. Unported: 15.

## Reasons

- **tempfile** (9): Lago renders through temporary files it must unlink. Tanvo renders in memory, and the Typst child process writes only inside the renderer's private directory, which libs/documents removes (Renderer.Close); there is no temporary file per document.
- **queue** (3): The configurable queue shared example asserts the Sidekiq queue name and its SIDEKIQ_* environment switch. Tanvo's jobs run on the River queues of libs/jobs, chosen per job kind (architecture §9), with no per-kind environment switch.
- **actor** (2): No caller passes user_id or api_key_id to a mailer: the only mailer parameter beyond the document is resend: true (app/services/emails/resend_service.rb:31-47), so the email.sent log is always from the system source. Tanvo's document email jobs carry no actor; the resend status is ported (apps/server/internal/app/documents_test.go).
- **premium** (1): Tanvo has no premium gates: every feature is available to every organization (the Tanvo-wide rule of docs/parity.md: no license checks in the core).

## Cases

### jobs/credit_notes/generate_documents_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 16 | it_behaves_like a configurable queue | queue |

### jobs/invoices/generate_documents_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 17 | it_behaves_like a configurable queue | queue |

### jobs/payment_receipts/generate_documents_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 17 | it_behaves_like a configurable queue | queue |

### jobs/send_email_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 36 | with user_id > "passes user_id to activity log" | actor |
| 52 | with api_key_id > "passes api_key_id to activity log" | actor |

### services/credit_notes/generate_pdf_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 76 | when creating temp files > "unlink the pdf file at the end" | tempfile |

### services/credit_notes/generate_xml_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 53 | when using temp files > "removes the temp file at the end" | tempfile |
| 64 | when using temp files > when error happens > "always removes the temp file" | tempfile |

### services/emails/resend_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 79 | with an invoice > when premium license is not available > "returns a forbidden failure" | premium |

### services/invoices/generate_pdf_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 170 | when creating temp files > "unlink the pdf file at the end" | tempfile |

### services/invoices/generate_xml_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 51 | when using temp files > "removes the temp file at the end" | tempfile |
| 62 | when using temp files > when error happens > "always removes the temp file" | tempfile |

### services/payment_receipts/generate_pdf_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 124 | when creating temp files > "unlink the pdf file at the end" | tempfile |

### services/payment_receipts/generate_xml_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 62 | when using temp files > "removes the temp file at the end" | tempfile |
| 73 | when using temp files > when error happens > "always removes the temp file" | tempfile |
