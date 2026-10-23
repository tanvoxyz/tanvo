# Skipped lago-api cases (plans)

Cases of `spec/services/plans/prepare_destroy_service_spec.rb`, `spec/services/plans/destroy_service_spec.rb` and `spec/jobs/plans/destroy_job_spec.rb` not ported to `libs/billing/plans`, with the reason.

| Source | Reason |
| :--- | :--- |
| `destroy_service_spec.rb:28` (produces an activity log) | Activity logs are the domain events; plan.deleted is emitted when the deletion is prepared (prepare_destroy_service_spec.rb:18). |
| `destroy_job_spec.rb:54` exact failure message | The job returns the first failed step's error; ported as "termination failures" in service_test.go. |
