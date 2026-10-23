# Skipped lago-api specs (libs/auth)

lago-api cases not ported to `libs/auth`, with the reason. Everything else in
the specs below is ported; each ported case carries a `// source:` comment.

| Spec | Cases | Reason |
| :--- | :--- | :--- |
| `api_keys/create_service_spec.rb`, `update_service_spec.rb`, `rotate_service_spec.rb` | premium / free organization, "api permissions addon" contexts | Tanvo has no premium or license gating (parity §1.50): permissions and preferred expiry are always available. The ported cases are the premium ones. |
| `api_keys/cache_service_spec.rb` | all | No key cache in the library; apps/server may cache `APIKeys.Authenticate`. |
| `api_keys/track_usage_service_spec.rb` | cache-key layout | Uses are collected by `UsageTracker` in memory and flushed to the store's recorded uses (`api_key_uses`) instead of Redis keys; the hourly clock job applies them per organization (`DispatchUsage`, `ApplyUsage`). The observable behavior is ported. |
| `users_service_spec.rb`, `invites/accept_service_spec.rb`, `auth/*/login_service_spec.rb` | "registers the user device" | Every flow opens its session through `Users.open`, which registers the device; `TestRegisterDevice` ports `user_devices/register_service_spec.rb` and covers sign-in, sign-up and invites once instead of per provider. |
| `utils/device_info_spec.rb` | user-agent parsing | apps/server parses the user agent into `SessionMeta.Browser`, `OS` and `DeviceType`; libs/auth stores what it is given. |
| `users_service_spec.rb` | `SegmentIdentifyJob`, `SegmentTrackJob` | No product analytics. |
| `users_service_spec.rb`, `auth/google_service_spec.rb` | JWT decoding of `result.token` | Sessions are opaque tokens; the login method is asserted on `Session.Method`. |
| `auth/okta/login_service_spec.rb:39`, `auth/entra_id/login_service_spec.rb:39` | "protects requests against private addresses" | The HTTP client is injected (`OIDCConfig.HTTPClient`); apps/server passes one that refuses private addresses. |
| `auth/entra_id/authorize_service_spec.rb:45`, `auth/entra_id/login_service_spec.rb:172` | domains or users differing only by casing | Domains and emails are stored lower-case, so two spellings cannot coexist. The case-insensitive routing and sign-in cases are ported. |
| `auth/google_service_spec.rb:58`, `:146` | device registration on Google sign-in | As above. |
| `auth/google_service_spec.rb:115`, `:175`, `:241` | "google auth is not set up" at callback time | Checked once at `Begin` (`oidc_missing_setup`); a flow cannot start without the connection. |
| `auth/superset_service_spec.rb`, `auth/superset/*` | all | Analytics embedding belongs to `ee/analytics`. |
| `customer_portal/customer_update_service_spec.rb` | all | Customer updates from the portal are an apps/server handler over libs/core; only token generation is in libs/auth. |
| mailer assertions (`have_enqueued_mail`) | all | Emails are `mail.send` jobs (`MailJob`); the tests assert the enqueued jobs instead. |
