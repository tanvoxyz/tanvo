# libs/proto

The Protobuf definitions of the Tanvo API and the code generated from them. Licensed under Apache-2.0 ([LICENSE](LICENSE)), so clients and `ee/*` can use it freely.

## Layout

| Path | Contents |
| :--- | :--- |
| `tanvo/<domain>/v1/*.proto` | Sources: `edition = "2023"`, package `tanvo.<domain>.v1` |
| `tanvo/common/v1` | Shared types (`Money`, `Address`, `Document`, …), enums, the `access` method option and predefined string rules (`decimal`, `currency`, `ref`, `code`) |
| `gen/go/tanvo/<domain>/v1` | Go messages (Opaque API), package `<domain>v1` |
| `gen/go/tanvo/<domain>/v1/<domain>v1connect` | Connect handlers and clients |
| `gen/ts` | TypeScript messages and service descriptors (protoc-gen-es v2), including dependencies |
| `gen/openapi/tanvo.openapi.yaml` | OpenAPI 3.1 document of the HTTP/JSON bindings |
| `api_test.go` | Descriptor checks over every RPC, and the parity report |

`gen/` is committed and never edited by hand.

## Generating

```bash
pnpm nx run proto:generate      # buf generate
pnpm nx run proto:buf-lint      # buf lint && buf format --diff --exit-code
pnpm nx run proto:test          # descriptor checks; add -v for the parity report
```

Generation uses buf remote plugins, so it needs network access to buf.build. Dependencies are pinned in `buf.lock` (protovalidate, googleapis).

| Plugin | Version | Output |
| :--- | :--- | :--- |
| `buf.build/protocolbuffers/go` | v1.36.12, `default_api_level=API_OPAQUE` | `gen/go` |
| `buf.build/connectrpc/go` | v2.0.2 | `gen/go` |
| `buf.build/bufbuild/es` | v2.16.0, `target=ts` | `gen/ts` |
| `buf.build/community/sudorandom-connect-openapi` | v0.28.0 (MIT), `with-proto-names`, `only-googleapi-http` | `gen/openapi` |

Connect v2 handlers use simple signatures (`func(ctx, *Req) (*Res, error)`) by default; there is no `simple` option to set.

The OpenAPI document comes from protoc-gen-connect-openapi: it is MIT licensed, reads `google.api.http` annotations, writes JSON names as proto names (snake_case, as served by Vanguard) and emits OpenAPI 3.1. Its document title is the plugin default, because the `base` option needs a local file and cannot be used with a remote plugin.

## Conventions

- Every public RPC has a `google.api.http` binding (AIP resource style) and a `(tanvo.common.v1.access)` option: the permission it needs, and the API key resource (`api_resource`) when API keys may call it. RPCs without `api_resource` are dashboard only.
- Standard methods: `Create` takes the resource as body; `Update` takes the resource and a `FieldMask`, with the resource's `id` in the path; `List` takes `page_size` (20 by default, at most 1000), `page_token`, an AIP-160 `filter` and, where useful, `search`, and returns `next_page_token`.
- Custom methods use snake_case verbs: `:terminate`, `:void`, `:batch_create`.
- References accept an ID or a prefixed natural key (`external_id:…`, `code:…`) where the resource has one (`(tanvo.common.v1.ref)`).
- Amounts and quantities are decimal strings (`Money.amount`, `(tanvo.common.v1.decimal)`), never floating point. Instants are `google.protobuf.Timestamp` named `*_at`; calendar dates are `google.type.Date`.
- Requests are validated with protovalidate: top-level required fields carry both `(google.api.field_behavior) = REQUIRED` and a buf.validate presence rule, enums are `defined_only`, and cross-field rules are CEL.
