# Skipped lago-api cases

These lago-api specs for the mailers, the resend service and the email activity log are not ported. Each entry gives the reason. Every other case in `spec/mailers/*.rb`, `spec/services/emails/resend_service_spec.rb` and `spec/services/utils/email_activity_log_spec.rb` is ported and carries a `// source:` comment.

| Source | Case | Reason |
| :--- | :--- | :--- |
| `spec/mailers/invoice_mailer_spec.rb:46` | with no pdf file, calls the invoice PDF generate service | libs/mail takes PDFs as bytes. Rendering a missing PDF is the caller's job (libs/documents). A missing invoice PDF is simply not attached. |
| `spec/mailers/credit_note_mailer_spec.rb:34` | with no pdf file, calls the credit note PDF generate service | Same as above. |
| `spec/mailers/payment_receipt_mailer_spec.rb:41` (the `GeneratePdfService` expectation) | generation is attempted before `FilesNotReadyError` | The `ErrFilesNotReady` half is ported. Generating the PDF is libs/documents' job. |
| `spec/mailers/payment_request_mailer_spec.rb:52` (the `GeneratePaymentUrlService` expectation) | the payment URL service is called with the payable | The payment URL is an input (`PaymentURL`). The link rendering half is ported. |
| `spec/mailers/admin_mailer_spec.rb:62` | always notifies the oldest admin | Choosing the recipient is a store query. The builder takes `OwnerEmail`. |
| `spec/mailers/admin_mailer_spec.rb:80` | uses the audit snapshot when no actor email is passed | The audit log lives outside libs/mail. The caller passes `ActorEmail`. |
| `spec/mailers/admin_mailer_spec.rb:86` | retries transient failures through SendEmailJob | Retries belong to libs/jobs. `IsTemporary` classifies the errors and is tested. |
| `spec/mailers/api_key_mailer_spec.rb:19` (the manager membership) | only admins are in BCC | Choosing the admins is a store query. The builder BCCs the `AdminEmails` it is given, which is tested. |
| `spec/mailers/data_export_mailer_spec.rb:45` (the `deliveries` count) | delivery is or is not performed | Ported as a build-or-skip check. Counting deliveries is ActionMailer behavior. |
| `spec/services/emails/resend_service_spec.rb:74` | premium license is not available | Tanvo has no license gates for resending (no premium tier in the AGPL core). |
| `spec/services/emails/resend_service_spec.rb:41` (the `have_enqueued_mail` expectations) | the mail job is enqueued | Enqueueing is libs/jobs' job. `ValidateResend` returns the envelope that the job passes to the builder. |
| `spec/services/utils/email_activity_log_spec.rb:32` | sends to Kafka with topic and key | Tanvo records activity through its own event pipeline. `NewActivity` builds the entry. |
| `spec/services/utils/email_activity_log_spec.rb:86` | does not send when Kafka is unavailable | Same as above. |
| `spec/services/utils/email_activity_log_spec.rb:113` | producer exceptions are reported to Sentry or re-raised | Same as above. Tanvo does not use Sentry. |
| `spec/services/utils/email_activity_log_spec.rb:289` | uses payment_receipt as resource_type and resource_id | The resource columns belong to the activity log writer. `DocumentRef` carries the type and ID, which is tested. |
