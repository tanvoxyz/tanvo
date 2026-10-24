# Tanvo 功能对齐

> [English](parity.md) | 简体中文

> 相关文档：[架构](architecture.zh-CN.md) · [领域模型](domain.zh-CN.md) · [开发指南](development.zh-CN.md) · [测试策略](testing.zh-CN.md) · [路线图](roadmap.zh-CN.md) · [AGENTS.zh-CN.md](../AGENTS.zh-CN.md)

> 本文是 lago-api 功能及其 Tanvo 对应实现的权威清单。lago-api 每项未废弃的功能都在 §1 中有一行。Lago 已废弃或遗留的部分列于 §2，Tanvo 以不同设计取代的部分列于 §3，Tanvo 有意不重现的上游缺陷列于 §4。除非注明其他仓库，Lago 源码路径都相对于 lago-api 仓库（`~/Code/frameworks/lago/api`）。

---

## 如何阅读本文

* **功能**：每个可以单独测试的行为占一行（每个计费模型、每种聚合类型、每组 webhook 事件、每项提供者能力）。
* **Lago 源码**：实现该功能的主要文件或目录；测试从对应的 `spec/` 路径移植（[测试策略 §2](testing.zh-CN.md)）。
* **Tanvo 模块**：拥有该逻辑的模块（[架构 §3](architecture.zh-CN.md#3-仓库结构)）。API 层位于 `libs/proto` 与 `apps/server`，持久化位于 `libs/store`；这些不言自明，除非它们是主要工作，否则不再重复。
* **状态**：`已完成`（已实现并带有移植的测试）、`逻辑已完成`（计算已在所属的库中实现并带有移植的测试；持久化、API 或任务尚待完成）、`进行中`（所属的库正在构建）、`计划中`（尚未开始）。
* **说明**：Tanvo 有意与 Lago 不同之处。没有说明的行与 Lago 行为一致，适用于所有地方的 API 约定除外（§3.1）。

Lago 用 `License.premium?`、`organization.premium_integrations` 或 `organization.feature_flags` 控制许多功能。Tanvo 的 AGPL 核心中没有许可证检查，已发布的功能也没有功能开关。所有计费、支付、集成与访问控制功能都属于核心。只有 Lago 以私有云服务形式运行的功能，才成为 `ee/` 下单独的企业服务（[架构 §14](architecture.zh-CN.md#14-企业版ee)）。§1.50 给出 Lago 每个开关的归属。

## 对齐基线

对齐目标是 lago-api 发布版本 **v1.55.0**（提交 `25b8b7b09`，确定基线时 Lago 的最新发布版本）。只存在于 Lago `main` 或未发布分支上的代码不是对齐目标。Lago 发布更新的版本后，Tanvo 会专门做一轮对齐，把基线移到该版本，并更新本节。

没有例外：Lago 未发布的功能（例如其 `feat-payment-terms-*` 分支上的结构化付款条件；v1.55.0 已包含其字段但未读取）在正式发布之前不会实现。

## 1. 功能清单

### 1.1 金额与数字

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 金额与数量的精确小数运算 | `app/services/` 中普遍使用的 `BigDecimal` | libs/money | 已完成 | 基于 `cockroachdb/apd/v3` 的 `money.Decimal`；从不使用 `float64` |
| ISO 4217 货币（Lago 接受的全部货币）及其最小单位指数 | `config/initializers/money.rb`、`app/models/concerns/currencies.rb` | libs/money | 已完成 | |
| 最小单位换算（分 ↔ 小数金额） | 模型中对 `money-rails` 的使用 | libs/money | 已完成 | |
| 按货币精度四舍五入（half-up） | `app/services/` 中通过 `.round` 的舍入 | libs/money | 已完成 | 每个调用处都显式给出舍入模式 |
| 最大余数法把金额分摊到各部分 | `app/services/credit_notes/apply_taxes_service.rb`、`app/services/invoices/` 中的税额分摊 | libs/money | 已完成 | 用于税额与额度的分摊 |
| 超出最小单位的精确金额（`precise_amount_cents`、`precise_unit_amount`） | `app/models/fee.rb` | libs/money | 已完成 | 以 `numeric` 存储 |
| 钱包额度精度：额度向下取整到 5 位小数 | `app/services/wallet_transactions/` | libs/money | 已完成 | |

### 1.2 组织

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 组织资料（名称、法定名称与编号、地址、邮箱、税号、标志） | `app/models/organization.rb`、`app/services/organizations/update_service.rb` | libs/core | 计划中 | |
| 组织时区、默认货币、文档语言 | `app/models/organization.rb` | libs/core | 计划中 | 作为默认值；计费主体与客户可以覆盖 |
| 组织设置的读取与更新 API | `app/controllers/api/v1/organizations_controller.rb` | apps/server | 进行中 | `OrganizationService`（`auth.OrganizationSettings`）读取组织并更新其 slug、邮箱、地址、徽标与认证方式；地址逐字段合并。在接入 blob 存储前拒绝上传徽标。`organizations_controller.rb` 的其他资料字段尚未开放 |
| 组织 slug（用于文档编号与收据） | `app/models/organization.rb` | libs/core、libs/auth | 已完成 | 变更时规范化，并检查是否为保留或已占用的 slug（`app/services/organizations/update_service.rb`） |
| 审计日志保留期设置（`audit_logs_period`） | `app/models/organization.rb` | libs/core、libs/auth | 逻辑已完成 | 应用于活动日志与 API 日志查询（`activity_logs_query.rb`、`api_logs_query.rb`）；与 v1.55.0 一样不能通过 API 更新 |
| 每个客户的最大钱包数设置 | `app/models/organization.rb` | libs/billing | 逻辑已完成 | |
| 每个组织允许的认证方式（邮箱密码、Google、Okta、Entra ID） | `app/models/organization.rb`、`app/services/organizations/update_service.rb` | libs/auth、apps/server | 已完成 | Okta 与 Entra ID 成为 OIDC 连接；新增通用 OIDC（`oidc`）作为 Tanvo 扩展。修改需要持有 `authentication_methods:update` 的用户；v1.55.0 定义了该权限（`config/permissions.yml`），但服务端并不检查（`app/graphql/types/organizations/update_organization_input.rb:8`）；REST API 不接受该字段，因此拒绝 API 密钥 |
| 认证方式变更时通知管理员的邮件 | `app/mailers/organization_mailer.rb` | libs/mail | 逻辑已完成 | |
| 每个组织用于 webhook 签名的 HMAC 密钥 | `app/models/organization.rb` | libs/webhooks | 计划中 | 由每个端点的 Standard Webhooks 密钥取代（§3） |
| 首次启动时初始化组织 | `config/initializers/`（`LAGO_CREATE_ORG`、`LAGO_ORG_USER_*`） | apps/server | 计划中 | 以 `tanvo admin bootstrap` 命令代替环境变量 |
| 按组织选择事件存储（PostgreSQL 或 ClickHouse） | `app/models/organization.rb`（`clickhouse_events_store`） | libs/eventstore | 计划中 | 安装级设置，可按组织覆盖 |

### 1.3 计费主体

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 计费主体的增删改查（法律身份、地址、标志、货币、时区、语言） | `app/models/billing_entity.rb`、`app/services/billing_entities/` | libs/billing | 进行中 | 创建、查询、列表与更新位于 `libs/billing/billingentities`，附移植的 spec。不提供归档：v1.55.0 从不归档（其删除 mutation 不起作用，`app/graphql/mutations/billing_entities/destroy.rb:17-20`）。创建时也接受税、发票自定义段落与催收活动；标志需要 blob 存储，服务器尚未接入 |
| 每个组织的默认计费主体及按客户指定 | `app/models/billing_entity.rb`、`app/services/customers/` | libs/core、libs/billing | 进行中 | 最先创建的有效主体成为默认主体（`app/models/organization.rb` `default_billing_entity`）；客户在创建或更新时取按 ID 或 `code:` 指定的主体，否则取默认主体；已归档的主体返回 `billing_entity_not_found`（`upsert_from_api_service.rb:154`） |
| 每个组织的计费主体数量（按套餐为 1、2 或不限） | `app/models/organization.rb`（`MULTI_ENTITIES_MAX`） | libs/billing | 已完成 | 核心中不设上限：去掉了 `app/services/billing_entities/create_service.rb:20` 的套餐检查 |
| 按客户或按计费主体编号文档 | `app/services/billing_entities/change_invoice_numbering_service.rb` | libs/billing | 逻辑已完成 | |
| 文档编号前缀 | `app/models/billing_entity.rb` | libs/billing | 逻辑已完成 | |
| 发票宽限期、付款条款、发票页脚 | `app/models/billing_entity.rb` | libs/billing | 逻辑已完成 | |
| 开票日期锚点与调整设置 | `app/models/billing_entity.rb` | libs/billing | 逻辑已完成 | |
| 零金额发票定稿设置 | `app/models/billing_entity.rb` | libs/billing | 逻辑已完成 | |
| 欧盟税务管理开关与自动欧盟税 | `app/services/billing_entities/`、`app/services/taxes/auto_generate_service.rb` | libs/billing | 已完成 | 在欧盟主体上启用时，在同一事务中创建或更新欧盟税（`tanvo_eu_*`，自动生成） |
| 在支持的国家（法国、德国）启用电子发票 | `app/models/billing_entity.rb` | libs/documents | 逻辑已完成 | |
| 邮件设置（哪些文档通过邮件发送）与发件地址 | `app/models/billing_entity.rb`（`email_settings`） | libs/mail | 进行中 | |
| 应用于计费主体的税（应用、移除、管理） | `app/services/billing_entities/taxes/` | libs/billing | 已完成 | 通过 `tax_ids`（ID 或 `code:`）整体替换，并在同一事务中标记该主体的草稿发票待刷新。代码精确匹配；v1.55.0 不区分大小写（`manage_taxes_service.rb:31-32`） |
| 每个计费主体所应用的催收活动 | `app/services/billing_entities/update_applied_dunning_campaign_service.rb` | libs/billing | 已完成 | 变更时重置回退到该主体活动的客户的尝试次数 |
| 计费主体 API | `app/controllers/api/v1/billing_entities_controller.rb` | apps/server | 已完成 | `BillingEntityService` 的 Create、Get、List、Update；ArchiveBillingEntity 未实现（见增删改查）。草稿发票的开票日期在更新的事务中调整，而不是在任务中（`update_invoice_issuing_date_settings_service.rb:21-22`） |

### 1.4 用户、成员资格、角色与邀请

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 跨组织的用户与成员资格（有效、已撤销） | `app/models/user.rb`、`app/models/membership.rb` | libs/auth、apps/server | 已完成 | `MemberService` 列出成员资格（最新在前，附管理员数量，可按邮箱搜索、按角色过滤）并读取单个成员资格（`memberships_resolver.rb`） |
| 多角色成员资格：权限为各角色权限的并集，至少一个角色 | `app/models/membership.rb`、`app/models/membership_role.rb` | libs/core、libs/store、libs/auth | 逻辑已完成 | `membership_roles` 表，行软删除；`Membership.RoleIDs`、`access.AnyGrants` |
| 预定义角色 admin、finance、manager | `app/models/role.rb`、`config/permissions.yml` | libs/auth | 逻辑已完成 | |
| 带权限集的自定义角色 | `app/services/roles/` | libs/auth、apps/server | 已完成 | `MemberService` 的 CreateRole、GetRole、ListRoles、UpdateRole 与 DeleteRole（`auth.Roles`），记录 `role.*` 安全日志；角色以 ID 或 `code:<code>` 引用；预定义角色不可修改 |
| 删除自定义角色：软删除，拒绝删除预定义角色和有效成员使用中的角色 | `app/services/roles/destroy_service.rb`、`app/models/role.rb` | libs/core、libs/store、libs/auth | 逻辑已完成 | `auth.Roles.Delete`，记录 `role.deleted` 安全日志；已删除的角色不再出现在查找、列表与成员中，其代码可以复用 |
| 权限目录（每种资源与操作） | `config/permissions.yml` | libs/auth、apps/server | 已完成 | 目录位于 `libs/core/permission`；角色与 API 密钥使用其中的权限字符串。没有列出它的 RPC（v1.55.0 也没有） |
| 成员资格规则：不能撤销自己的成员资格、不能移除最后一个管理员、只有管理员能授予管理员 | `app/services/memberships/` | libs/auth、apps/server | 已完成 | `auth.Memberships`（UpdateRoles、Revoke），记录 `user.role_edited` 与 `user.deleted` 安全日志，由 `MemberService.UpdateMembership` 与 `RevokeMembership` 提供；撤销已撤销的成员会成功且不改变成员 |
| 带令牌与角色的邀请，支持接受、撤销、更新 | `app/models/invite.rb`、`app/services/invites/` | libs/auth、apps/server | 已完成 | 每个邀请可带多个角色（`role_ids`），以 ID 而非代码存储。`MemberService` 创建（仅此一次返回接受链接）、列出、更新与撤销邀请；`AuthService` 接受邀请 |
| 通过邮件重置密码（30 分钟令牌） | `app/services/password_resets/` | libs/auth、apps/server | 已完成 | `AuthService.RequestPasswordReset` 与 `ResetPassword`；未知邮箱也返回成功且不发邮件，而不是以 `user` `not_found` 失败（`app/services/password_resets/create_service.rb:14`），因此该 RPC 不会暴露账户是否存在 |
| 自托管安装的注册开关 | `LAGO_DISABLE_SIGNUP`、`app/services/users_service.rb` | apps/server | 已完成 | `TANVO_DISABLE_SIGNUP` |

### 1.5 认证与会话

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 邮箱密码登录与注册 | `app/services/users_service.rb`、`app/graphql/mutations/` | libs/auth、apps/server | 已完成 | argon2id 哈希；以不透明会话代替 JWT（§3）。`AuthService`（注册、密码与 OIDC 登录、邀请、当前用户、登出）；会话在 `X-Tanvo-Organization` 请求头指定的组织中操作（[架构 §4.4](architecture.zh-CN.md#44-认证与授权)）；过期会话由每小时的 `clock.purge_sessions` 任务清理 |
| Google 登录、注册与接受邀请 | `app/services/auth/google_service.rb` | libs/auth | 逻辑已完成 | 经由 OIDC 身份提供者 |
| Okta 登录（authorize、token、userinfo、state 校验） | `app/services/auth/okta/` | libs/auth | 逻辑已完成 | OIDC 连接；一次性 state 存储在 PostgreSQL（`oidc_states`，10 分钟而非 90 秒） |
| 带租户与附加域名的 Entra ID 登录 | `app/services/auth/entra_id/` | libs/auth | 逻辑已完成 | OIDC 连接 |
| Okta 与 Entra ID 集成配置（客户端、域名、主机） | `app/models/integrations/okta_integration.rb`、`entra_id_integration.rb` | libs/core、libs/auth | 逻辑已完成 | `integration.AuthSettings`（另有必填的默认角色）；域名跨组织且不区分大小写地路由（`IntegrationRepository.ListByDomain`、`auth.IntegrationConnections`）。`SsoConnectionService`（`auth.SSOConnections`）移植 Okta 与 Entra ID 的创建、更新、删除服务：创建时启用对应方式；删除时停用该方式，若不再剩下其他方式则以 `enabled_authentication_methods_required` 拒绝；空白或掩码（`••••••••…`）密钥保留已存的密钥；每个提供者认领的域名必须唯一。每次变更都写入 `integration.*` 安全日志。客户端密钥用 AES-256-GCM 加密（密钥由 `TANVO_SECRET_KEY` 派生，并绑定组织），永不返回。该服务需要 `TANVO_SECRET_KEY`，未设置时这些 RPC 返回 Unimplemented |
| 通用 OIDC 提供者（发现 issuer，支持任意标准 OpenID 提供者） | 无（超出 v1.55.0 基线的 Tanvo 扩展，由用户决定） | libs/core、libs/auth、apps/server | 逻辑已完成 | 提供者 `oidc`、认证方式 `oidc`、回调 `/auth/oidc/callback`。同一个基于标准的客户端服务 Google、Okta 与 Entra ID 预设以及通用连接。以 `issuer_url` 配置：发现文档报告的 issuer 必须与之完全相同，开发模式之外必须使用 https，并被缓存（1 小时，重新获取失败时保留旧文档）；JWKS 缓存 1 小时，遇到未知 `kid` 时重新获取，至多每分钟一次。对提供者的请求经由防 SSRF 的客户端（除非设置 `TANVO_OIDC_ALLOW_PRIVATE`，否则拒绝私有与回环地址），超时 10 秒，响应至多 1 MiB。ID 令牌：仅接受 RS、ES、PS 系列与 EdDSA（从不接受 `none` 或 HMAC）；校验 `iss`、`aud`、多受众时的 `azp`、`exp`、`nbf`、`iat`（容差 30 秒）与 nonce。每个连接可配置：客户端 ID 与加密的密钥、令牌端点认证方式（`client_secret_basic` 或 `client_secret_post`，默认取自发现文档）、scope（默认 `openid email profile`）、邮箱与姓名声明、除非 `allow_unverified_email` 否则要求 `email_verified`、附加授权参数（拒绝协议参数）、认领的域名与默认角色。userinfo 补全缺失的声明。创建与更新时获取一次发现文档，失败时返回 `issuer_url` / `https_required`、`issuer_unreachable`、`issuer_mismatch`、`discovery_incomplete`，或 `token_endpoint_auth_method` / `unsupported_by_provider` |
| 拒绝移除最后一种已启用的登录方式 | `app/services/integrations/okta/destroy_service.rb` | libs/auth | 逻辑已完成 | |
| 已知设备与新设备安全日志记录 | `app/models/user_device.rb`、`app/services/user_devices/` | libs/core、libs/store、libs/auth、apps/server | 逻辑已完成 | `access.UserDevice`；apps/server 在每次登录时把 user agent 解析为浏览器、操作系统和设备类型（`Utils::DeviceInfo`）；安全日志尚未接入 |
| GoCardless 连接的 OAuth 回调 | `app/services/payment_providers/gocardless_service.rb`（`LAGO_OAUTH_PROXY_URL`） | providers/gocardless | 进行中 | 回调由 `apps/server` 提供；不经代理。适配器部分已完成：`gocardless.(*Gateway).AuthorizeURL` 与 `ExchangeCode`（客户端凭据放在请求体中，scope 为 `read_write`，使用 live 或 sandbox Connect 服务器）；被拒的授权码为带 OAuth 错误码的 `payment.KindInvalidRequest`。`apps/server` 以安装的合作伙伴应用（`TANVO_GOCARDLESS_CLIENT_ID`、`TANVO_GOCARDLESS_CLIENT_SECRET`、`TANVO_GOCARDLESS_SANDBOX`）将其适配为 `integration.OAuthGateway`（`gocardlessOAuth`，访问令牌存为 `access_token` 密钥）；它已注册到集成服务，由 `StartIntegrationOauth` 与 `CompleteIntegrationOauth` 连接 GoCardless |

### 1.6 API 密钥与 API 约定

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 每个组织多个具名 API 密钥 | `app/models/api_key.rb`、`app/services/api_keys/` | libs/auth、apps/server | 已完成 | 以 SHA-256 哈希存储，只显示一次。`ApiKeyService`（仅限会话）；读取与列表只返回有效密钥，列表按创建时间从早到晚排列（`api_keys_resolver.rb:18`） |
| 按资源（读、写）设置每个密钥的权限 | `app/models/api_key.rb`（`RESOURCES`） | libs/auth、apps/server | 已完成 | 与角色使用相同的权限字符串；不设授权即为完全访问 |
| 密钥过期与轮换 | `app/services/api_keys/rotate_service.rb` | libs/auth、apps/server | 已完成 | 轮换时旧密钥在一个重叠窗口内仍然有效；`RotateApiKey` 返回新密钥 |
| 拒绝删除最后一个不过期的密钥 | `app/services/api_keys/destroy_service.rb` | libs/auth、apps/server | 已完成 | `DeleteApiKey` 使密钥立即过期 |
| 最近使用时间跟踪 | `app/jobs/clock/api_keys/track_usage_job.rb` | libs/auth | 已完成 | API 角色在内存中收集使用记录，每分钟记录到 `api_key_uses`（lago 在每个请求中写入 Rails 缓存，`app/controllers/api/base_controller.rb:59`）；每小时 :15 的 clock 任务为每个有使用记录的组织入队一个任务，把记录移到 `last_used_at`（从不回退） |
| 密钥创建、轮换或删除时发送邮件 | `app/mailers/api_key_mailer.rb` | libs/mail | 逻辑已完成 | 由 `mail.send` 任务（apps/server）发送给组织管理员 |
| 写请求的幂等性 | `app/models/idempotency_record.rb`、`app/services/idempotency_records/` | apps/server | 已完成 | 所有有副作用的 RPC 支持 `Idempotency-Key` 请求头；响应保留 24 小时（[架构 §4.5](architecture.zh-CN.md#45-幂等)）。Lago 的记录是少数服务内部使用的键；客户端请求头是 Tanvo 新增的 |
| 健康与就绪端点 | `config/routes.rb`（`/health`、`/ready`） | apps/server | 已完成 | `/healthz`、`/readyz`；就绪检查 PostgreSQL、事件存储，以及使用时的 ClickHouse 与 Kafka |
| Prometheus 指标端点 | `config/initializers/yabeda.rb` | apps/server | 已完成 | OpenTelemetry（默认 OTLP）与可选的 `/metrics` 读取器（`TANVO_METRICS_ENDPOINT`） |
| 当前版本端点 | `app/graphql/resolvers/version_resolver.rb` | apps/server | 已完成 | `ServerService.GetServerInfo`：构建中的版本与修订 |
| 对第三方 API 调用的限流 | `config/initializers/throttling.rb` | providers/* | 计划中 | 按提供者设定上限（HubSpot 110 次/10 秒，Xero 60 次/分钟与 5000 次/天，NetSuite 10 次/秒，Anrok 9 次/秒，Avalara 10 次/秒） |

### 1.7 客户

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 按外部 ID 创建或更新客户（upsert） | `app/services/customers/upsert_from_api_service.rb` | libs/billing、apps/server | 已完成 | libs/billing/customers 之上的 `CustomerService`：`CreateCustomer` 只创建（外部 ID 已被有效客户使用时返回 `external_id` `value_already_exist`），`UpdateCustomer` 按 ID 或 `external_id:` 更新，带 `allow_missing` 时执行 upsert（lago 的 POST 即 upsert，`upsert_from_api_service.rb:21`）；用字段存在性和 `update_mask` 区分未提供与清除。去除空字节并清理电子邮件。客户已不可编辑时对 `external_id` 或 `account_type` 的修改被忽略，与 lago 相同（`update_service.rb:103-109`、`upsert_from_api_service.rb:81`）。时区、宽限期与合作伙伴账户不受高级版限制。在提供者中同步客户有待提供者客户端（§3） |
| 客户类型（公司、个人）与账户类型（客户、合作伙伴） | `app/models/customer.rb` | libs/core、apps/server | 已完成 | |
| 合作伙伴账户与自开票发票 | `app/models/customer.rb`、`app/services/invoices/` | libs/billing | 逻辑已完成 | |
| 组织内的顺序编号与 slug | `app/models/customer.rb` | libs/billing | 已完成 | 与 lago 一样按组织编号，并计入已删除的客户（`app/models/customer.rb:140-141`）；slug 前缀取组织的文档编号前缀（`app/models/customer.rb:424-430`，默认值见 `app/models/organization.rb:319-320`） |
| 账单地址与收货地址 | `app/models/customer.rb` | libs/core、apps/server | 已完成 | 国家代码转为大写 |
| 税号、法定名称与编号 | `app/models/customer.rb` | libs/core、apps/server | 已完成 | |
| 默认货币：新计费对象的偏好，而不是约束 | `app/services/customers/update_currency_service.rb` | libs/core | 逻辑已完成 | 为空时由第一个计费对象设置；显式更新可随时修改 |
| 每个客户多种货币：订阅取其套餐的货币；钱包、已应用的优惠券和一次性发票取各自的货币；发票按货币拆分 | `app/services/subscriptions/organization_billing_service.rb`（`group_by_currency`）、`app/services/wallets/create_service.rb`、`app/services/applied_coupons/create_service.rb`、`app/services/invoices/create_one_off_service.rb` | libs/billing | 逻辑已完成 | 钱包和固定金额优惠券只抵扣同货币的发票；催收按货币计数尝试次数（§1.35） |
| 时区，回退到计费主体与组织 | `app/models/customer.rb` | libs/period | 计划中 | 各处一律按客户 → 计费主体 → 组织回退（§4） |
| 宽限期、付款条款、文档语言及其回退 | `app/models/customer.rb` | libs/billing | 逻辑已完成 | |
| 零金额发票定稿：继承、跳过、定稿 | `app/models/customer.rb` | libs/billing | 逻辑已完成 | |
| 开票日期锚点与调整的覆盖 | `app/models/customer.rb` | libs/billing | 逻辑已完成 | |
| 客户元数据（最多 5 个，键 20 字符，值 100 字符，可显示在发票上） | `app/models/metadata/customer_metadata.rb` | libs/core、apps/server | 已完成 | 随客户返回；列表过滤 `metadata.<key>` |
| 有订阅或已开票后的可编辑规则 | `app/models/customer.rb`（`editable?`） | libs/core、libs/billing | 已完成 | 有订阅、发票、钱包或固定金额的已应用优惠券时客户不可编辑 |
| 客户列表过滤（搜索词、元数据、国家、州、邮编、货币、客户与账户类型、计费主体、是否有税号） | `app/queries/customers_query.rb` | libs/store、apps/server | 已完成 | 两种存储均已实现查询，移植的规格用例在 porttest 中；`ListCustomers` 可按 `external_id`、`billing_entity_id`（ID 或 `code:`，不存在则返回未找到）、`customer_type`、`has_customer_type`、`account_type`、`currency`、`billing_address.country`/`state`/`zipcode`、`has_tax_identification_number`、`metadata.<key>`、`deleted`（即 GraphQL 的 `with_deleted`）与 `active_subscriptions_count_from`/`_to`（GraphQL 参数，`customers_query.rb:148-162`：上下界相等时取该数量，两个界取闭区间，只有一个界时不含边界）过滤，支持 `search`，默认按最新优先 |
| 删除客户并终止其关联 | `app/services/customers/destroy_service.rb`、`app/jobs/customers/terminate_relations_job.rb` | libs/billing、apps/server | 已完成 | `DeleteCustomer` 归档客户并排队 `customers.terminate_relations`，该任务终止其活跃与待生效订阅、已应用的优惠券和活跃钱包，并在 5 分钟后排队定稿其草稿。该任务需注册到计费任务分发（`customers.Service.HandleJob`）。与 lago 相同，不取消待定的 VIES 检查。发票搜索词在查询时由客户信息计算，因此 lago 的 `Customers::RefreshInvoicesSearchTermsJob`（`update_service.rb:158-160`）无需刷新 |
| 每个客户的支付提供者连接（提供者客户 ID、与提供者同步） | `app/services/payment_provider_customers/create_or_update_batch_service.rb` | libs/billing、apps/server | 进行中 | 通过支付集成使用 `UpsertCustomerConnection`、`ListCustomerConnections` 与 `DeleteCustomerConnection`：每个客户一个支付连接（`only_one_connection_allowed`，与 v1.55.0 的批量服务一致，`create_or_update_batch_service.rb:23-30`），且始终为默认连接；删除即作废。不提供手动连接代码与设为默认的变更；在提供者客户端接入之前不会在提供者中创建客户 |
| 集成客户（会计、CRM、税务）的批量创建与更新 | `app/services/integration_customers/create_or_update_batch_service.rb` | libs/billing、apps/server | 已完成 | 同样的 RPC 用于会计、税务或 CRM 集成：每个客户可有多个，某类别的第一个为默认，`is_default` 可移动默认（`set_as_default_service.rb`）。合作伙伴账户的连接保持不变，没有外部客户 ID 且不同步的连接不会创建，两者都成功返回且无连接（`create_or_update_batch_service.rb:16`、`create_connection_service.rb:18-19`）；同一提供者的第二个连接为 `customer_id` `value_already_exist`，第二个税务连接为 `integration_id` `tax_integration_exists`（`base_customer.rb:42, 44`）。更新只赋值非空的外部客户 ID 与 HubSpot 的 `targeted_object`，Anrok 与 Salesforce 两者都不赋值（`update_service.rb:18-23`）；同步标志与子公司在创建时设定。提供者返回的联系人邮箱保存在连接上。多个客户可链接同一个外部客户。一个 RPC（`UpsertCustomerConnection`，以客户与集成为键）同时服务 REST 与控制台客户端，并像批量服务一样把提供者同步加入队列（`create_or_update_batch_service.rb:48-60`），而控制台的 `CreateConnectionService` 同步调用（`netsuite_service.rb:22-23`）。连接代码随计费对象连接规划（“按计费对象选择连接”一行） |
| 用于收集支付方式的结账 URL | `app/services/customers/generate_checkout_url_service.rb` | libs/billing、apps/server | 进行中 | `GenerateCheckoutUrl` 检查支付连接（`no_linked_payment_provider`），在提供者客户端接入之前返回 Unimplemented（`customers.WithCheckout`） |
| 客户门户 URL | `app/services/customer_portal/generate_url_service.rb` | libs/auth、apps/server | 已完成 | `GeneratePortalUrl` 返回 URL 及其过期时间（12 小时）；`app.NewPortalTokens` 由 `TANVO_SECRET_KEY` 派生签名密钥；已删除的客户返回 `customer_not_found`；未配置密钥时该 RPC 返回 Unimplemented |
| 串行化每个客户操作的锁（预付额度、支付方式、贷项通知单、优惠券、计费调度） | `app/services/customers/lock_service.rb` | libs/store | 逻辑已完成 | PostgreSQL 咨询锁 |
| 按客户选择或跳过发票自定义段落 | `app/services/customers/manage_invoice_custom_sections_service.rb` | libs/billing、apps/server | 已完成 | 跳过会清空所选段落，选择段落会取消跳过，二者同时提供返回 `skip_sections_and_selected_ids_sent_together`；与 lago 的 `where` 一样忽略未知段落 |
| 排除在催收之外与所应用的催收活动 | `app/models/customer.rb` | libs/billing、apps/server | 已完成 | 指定催收活动会取消排除，排除会清除催收活动；合作伙伴始终被排除；任何变化都会重置尝试次数（`update_service.rb`） |
| x402 代理地址 | `app/models/customer.rb`（`x402_agent_address`） | libs/core、libs/billing/customers、apps/server | 已完成 | 见 §1.36。EVM 地址（以 EIP-55 校验和形式存储）或 Solana 地址，在组织内未删除的客户中唯一；否则为 `invalid_format`。超出 v1.55.0 基线的 Tanvo 扩展，用户已于 2026-10-24 批准：可通过 `CustomerService` 设置和返回，v1.55.0 不这样做（v1.55.0 中没有代码写入它；`check_service.rb:118` 只读取它），以便已知代理能够存在 |

### 1.8 计费指标

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 计费指标的增删改查（名称、代码、描述、字段名） | `app/models/billable_metric.rb`、`app/services/billable_metrics/` | libs/billing、apps/server | 已完成 | `BillableMetricService`，RPC 指南的参考服务。路径接受 ID 或 `code:<code>`（lago 只接受代码），这样修改代码后 ID 依然可用。列表按创建时间倒序，并可按 `aggregation_type`、`recurring`、`plan_id` 过滤、按名称与代码搜索（lago `BillableMetricsQuery`，仅在 GraphQL 中提供），供控制台使用。`weighted_sum_agg` 指标的加权区间必须为 `seconds`，其他指标保留该值且不校验，与 lago 一致（`app/models/billable_metric.rb:59-61`）。按 gRPC 状态模型，校验失败返回 `InvalidArgument`（HTTP 400）而非 422，被拒绝的删除返回 `FailedPrecondition` 而非 405 |
| 聚合 `count_agg` | `app/services/billable_metrics/aggregations/count_service.rb` | libs/metering | 逻辑已完成 | |
| 聚合 `sum_agg` | `app/services/billable_metrics/aggregations/sum_service.rb` | libs/metering | 逻辑已完成 | |
| 聚合 `max_agg` | `app/services/billable_metrics/aggregations/max_service.rb` | libs/metering | 逻辑已完成 | |
| 带增加与移除操作的聚合 `unique_count_agg` | `app/services/billable_metrics/aggregations/unique_count_service.rb` | libs/metering | 逻辑已完成 | |
| 在加权区间上的聚合 `weighted_sum_agg` | `app/services/billable_metrics/aggregations/weighted_sum_service.rb` | libs/metering | 逻辑已完成 | |
| 聚合 `latest_agg` | `app/services/billable_metrics/aggregations/latest_service.rb` | libs/metering | 逻辑已完成 | |
| 跨计费周期结转的循环指标（count、max、latest 不允许） | `app/models/billable_metric.rb`、`app/services/billable_metrics/aggregations/` | libs/metering | 逻辑已完成 | |
| 按比例求和 | `app/services/billable_metrics/prorated_aggregations/sum_service.rb` | libs/metering | 逻辑已完成 | |
| 按比例唯一计数 | `app/services/billable_metrics/prorated_aggregations/unique_count_service.rb` | libs/metering | 逻辑已完成 | |
| 按比例求和与唯一计数的明细 | `app/services/billable_metrics/breakdown/` | libs/metering | 逻辑已完成 | |
| 按定价分组键分组聚合 | `app/services/billable_metrics/aggregations/base_service.rb` | libs/metering | 逻辑已完成 | |
| 预付计费项的逐事件聚合 | `app/services/billable_metrics/aggregations/`（`per_event_aggregation`） | libs/metering | 逻辑已完成 | |
| 聚合值的舍入（round、floor、ceil，带精度） | `app/services/billable_metrics/aggregations/apply_rounding_service.rb` | libs/metering | 逻辑已完成 | |
| 指标上的表达式 | `app/models/billable_metric.rb`（`expression`） | libs/expression | 逻辑已完成 | |
| 表达式求值 API（用样例事件测试表达式） | `app/services/billable_metrics/evaluate_expression_service.rb` | libs/billing、apps/server | 已完成 | `POST /v1/billable_metrics:evaluate_expression`，与 lago 一样需要写权限（`spec/requests/api/v1/billable_metrics_controller_spec.rb:307`）。数字按 Rails 渲染 BigDecimal 的方式输出（`2.0`）；`event.timestamp` 是 Ruby 绑定以数字传入的整数秒（`1710496800.0`）。属性值按 Ruby `to_s` 的方式转换为文本（`evaluate_expression_service.rb:26`）：`nil` 为空，对象与数组按 `inspect` 输出（`{"a" => 1}`），浮点数遵循 `Float#to_s`（`1.0e+16`）。JSON 数字到达服务端时是双精度浮点数，因此小于 10^16 的整数值按整数输出，对象的键按排序后的顺序输出 |
| 指标被套餐使用后的字段锁定 | `app/services/billable_metrics/update_service.rb` | libs/billing | 已完成 | |
| 删除指标：移除计费项与告警、刷新草稿 | `app/services/billable_metrics/destroy_service.rb` | libs/billing | 已完成 | 指标被软删除（`deleted_at`，`destroy_service.rb:33`）：读取时会排除它，它的代码在存活的指标中可以重新使用（部分唯一索引）。事件以及优惠券与钱包目标随它保留（§2）。它的告警在同一事务中被软删除，阈值保留（`destroy_service.rb:35`）。它的计费项及其计费项过滤器在同一事务中被软删除；它的指标过滤器由 `billing.destroy_metric_filters` 任务软删除（`BillableMetricFilters::DestroyAllJob`，`destroy_service.rb:41`），该指标的摄取缓存会失效。草稿刷新标记在同一事务中设置。当有产品过滤器取值选中它的某个过滤器时，删除被拒绝（`billable_metric referenced_by_product_filter`，`destroy_service.rb:20-23`） |
| 指标过滤器（键及允许的值）与批量更新 | `app/services/billable_metric_filters/create_or_update_batch_service.rb` | libs/billing | 已完成 | 移除一个值时同步裁剪依赖它的计费项过滤器。重复的取值与 lago 一样被去重（`create_or_update_batch_service.rb:42`）；空的取值列表与 lago 一样被拒绝（`billable_metric_filter.rb:17`），空的取值也被拒绝。草稿发票的刷新标记在同一事务中设置（`create_or_update_batch_service.rb:67`），因此标记与它所反映的变更一起提交。若替换会移除被产品过滤器取值选中的过滤器或取值，则被拒绝（`filters referenced_by_product_filter`，`create_or_update_batch_service.rb:20,32,85-96`）；选中整个过滤器的取值只阻止移除该过滤器 |

### 1.9 表达式

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 小数运算（+、-、*、/）与括号 | lago-expression `expression-core` | libs/expression | 逻辑已完成 | |
| 事件字段：`event.timestamp`、`event.code`、`event.properties.<name>` | lago-expression `expression-core` | libs/expression | 逻辑已完成 | |
| 字符串字面量与 `concat` | lago-expression `expression-core` | libs/expression | 逻辑已完成 | |
| 带精度的 `round`、`ceil`、`floor` | lago-expression `expression-core` | libs/expression | 逻辑已完成 | |
| `least`、`greatest` | lago-expression `expression-core` | libs/expression | 逻辑已完成 | |
| 创建指标时校验表达式 | `app/services/billable_metrics/create_service.rb` | libs/expression | 逻辑已完成 | |
| 摄取时求值以得到聚合字段 | `app/services/events/calculate_expression_service.rb` | libs/expression | 逻辑已完成 | 在 wazero 上的 WASM 中批量求值 |
| 浏览器中使用同一求值器做预览 | lago-expression `expression-js` | libs/expression | 计划中 | wasm-bindgen 构建 |

### 1.10 套餐

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 套餐的增删改查（名称、代码、描述、发票显示名称、元数据） | `app/models/plan.rb`、`app/services/plans/` | libs/billing、apps/server | 已完成 | `plans.Service` 上的 `PlanService`（Create、Get、List、Update、Delete），含用量阈值与最低承诺。路径接受 ID 或 `code:<code>`；列表包含父套餐（含待删除的），按最新优先，并搜索名称与代码。元数据整体替换。REST 的计数（`active_subscriptions_count`、`draft_invoices_count`）按 lago GraphQL 的方式计算（`app/models/plan.rb:124-149`），而非 0（`app/serializers/v1/plan_serializer.rb`）。没有高级版或渐进计费的限制（§19）；产品目录的旧字段限制被去掉，因为只有一个目录（`app/services/plans/create_service.rb:24-31`）。`update_subscriptions_amount` 返回 `Unimplemented`：v1.55.0 没有该参数 |
| 间隔：每周、每月、每季度、每半年、每年 | `app/models/plan.rb` | libs/period | 逻辑已完成 | |
| 订阅费金额与货币 | `app/models/plan.rb` | libs/billing | 逻辑已完成 | |
| 订阅费预付或后付 | `app/models/plan.rb`（`pay_in_advance`） | libs/billing | 逻辑已完成 | |
| 以天为单位的试用期 | `app/models/plan.rb` | libs/period | 逻辑已完成 | |
| 年度、半年度与季度套餐按月计费计费项 | `app/models/plan.rb`（`bill_charges_monthly`） | libs/period | 逻辑已完成 | |
| 按月计费固定计费项 | `app/models/plan.rb`（`bill_fixed_charges_monthly`） | libs/period | 逻辑已完成 | |
| 应用于套餐的税 | `app/services/plans/apply_taxes_service.rb` | libs/billing | 已完成 | 套餐及其最低承诺上的 `tax_ids`，按 ID 或 `code:<code>`；未知的税返回未找到 |
| 用于区分升级与降级的年度金额 | `app/models/plan.rb`（`yearly_amount_cents`） | libs/billing | 逻辑已完成 | |
| 按订阅覆盖套餐（子套餐） | `app/services/plans/override_service.rb` | libs/billing | 已完成 | `plans.Service.Override` 复制套餐但不含其税与元数据，覆盖每个计费项与固定计费项，并创建给定的用量阈值与最低承诺；不发送 webhook |
| 套餐有订阅后的字段锁定 | `app/services/plans/update_service.rb` | libs/billing | 已完成 | 对有订阅的套餐修改代码、间隔、预付、货币、试用期或按月计费设置时，以 `value_is_immutable` 拒绝，而不是忽略（`app/services/plans/update_service.rb:39-49`） |
| 把套餐变更级联到被覆盖的子套餐 | `app/services/plans/update_service.rb`（`cascade_updates`）、`app/jobs/plans/` | libs/billing | 已完成 | `cascade_updates` 为每个仍是旧金额的子套餐在计费队列上加入 `billing.update_plan_amount` 任务 |
| 更新现有订阅的金额 | `app/jobs/plans/update_amount_job.rb` | libs/billing | 已完成 | `plans.Service.UpdateAmount` 跳过金额已不是预期值的套餐。金额变更会取消已变成升级的待生效降级，并按激活规则升级那些前一套餐年度金额不高于新金额的待生效订阅 |
| 删除套餐：先准备（待删除），再删除并终止订阅 | `app/services/plans/prepare_destroy_service.rb`、`app/services/plans/destroy_service.rb` | libs/billing | 逻辑已完成 | `plans.Service`（PrepareDestroy、Destroy、`billing.destroy_plan` 任务）：所有已删除的套餐都以 `deleted` 状态保留，其代码可再次使用；权益被软删除；待生效订阅被取消且不记录原因（领域模型 §7.2）。`PlanService.DeletePlan` 执行 PrepareDestroy 并返回待删除的套餐 |
| 套餐 webhook `plan.created`、`plan.updated`、`plan.deleted` | `app/services/webhooks/plans/` | libs/billing | 已完成 | 在变更的事务内发送；更新还会把套餐订阅的草稿发票标记为待刷新 |

### 1.11 计费项与计费模型

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 计费模型 standard（单价） | `app/services/charge_models/standard_service.rb` | libs/pricing | 逻辑已完成 | |
| 计费模型 graduated（各档分别计价，每档可有固定费用） | `app/services/charge_models/graduated_service.rb` | libs/pricing | 逻辑已完成 | |
| 计费模型 package（包大小、免费单位） | `app/services/charge_models/package_service.rb` | libs/pricing | 逻辑已完成 | |
| 计费模型 percentage（费率、每个事件的固定金额、按事件数与按总量的免费单位、每笔交易的最小与最大值） | `app/services/charge_models/percentage_service.rb` | libs/pricing | 逻辑已完成 | |
| 计费模型 volume（全部用量按达到的档位计价） | `app/services/charge_models/volume_service.rb` | libs/pricing | 逻辑已完成 | |
| 计费模型 graduated percentage | `app/services/charge_models/graduated_percentage_service.rb` | libs/pricing | 逻辑已完成 | |
| 计费模型 dynamic（价格来自事件的 `precise_total_amount_cents`） | `app/services/charge_models/dynamic_service.rb` | libs/pricing | 逻辑已完成 | |
| 分组计费项（每个定价分组一个金额） | `app/services/charge_models/grouped_service.rb` | libs/pricing | 逻辑已完成 | |
| 逐事件增量计算预付金额 | `app/services/charges/apply_pay_in_advance_charge_model_service.rb` | libs/pricing | 逻辑已完成 | |
| 周期结束时的预计金额 | `app/services/charge_models/`（预测） | libs/pricing | 逻辑已完成 | |
| 用于展示的按档与按单位金额明细 | `app/services/charge_models/`（`amount_details`） | libs/pricing | 逻辑已完成 | |
| 档位区间校验（区间相邻，最后一档开放） | `app/services/charges/validators/` | libs/pricing | 逻辑已完成 | 只有一种区间约定；不保留 Lago v1 的包含式 +1 区间（§3） |
| 各计费模型的默认属性 | `app/services/charge_models/build_default_properties_service.rb` | libs/pricing | 逻辑已完成 | |
| 预付计费项 | `app/models/charge.rb`（`pay_in_advance`） | libs/billing | 逻辑已完成 | |
| 预付计费项的可开票开关（不开发票的费用） | `app/models/charge.rb`（`invoiceable`） | libs/billing | 逻辑已完成 | |
| 已付的预付费用在下一张发票上重新汇总 | `app/models/charge.rb`（`regroup_paid_fees`） | libs/billing | 逻辑已完成 | |
| 计费项最低金额及补差费用 | `app/models/charge.rb`（`min_amount_cents`）、`app/services/fees/create_true_up_service.rb` | libs/billing | 逻辑已完成 | |
| 按比例计算的计费项（循环指标） | `app/models/charge.rb`（`prorated`） | libs/pricing | 逻辑已完成 | |
| 可以指向某个钱包的计费项 | `app/models/charge.rb`（`accepts_target_wallet`） | libs/billing | 已完成 | 已存储并级联到覆盖项；计费项的用量按事件的 `target_wallet_code` 分组，每组的费用只由客户中该代码的钱包支付（未指明钱包的组的费用遵循普通钱包规则）。没有部分索引：Tanvo 中没有查询按该标志过滤 |
| 计费项代码生成 | `app/services/charges/generate_code_service.rb` | libs/billing | 已完成 | 创建或更新套餐、以及把新计费项复制到覆盖套餐时，缺少的代码由指标代码生成，后缀取套餐原始计费项中最大数字后缀加一；计费项 API 与 `charge.rb:50` 一样要求代码 |
| 定价分组键（按键聚合） | `app/models/charge.rb`（`pricing_group_keys`） | libs/pricing | 逻辑已完成 | |
| 展示分组键（最多两个，只用于展示） | `app/models/charge.rb`（`presentation_group_keys`） | libs/billing | 已完成 | 存储在计费项属性中，即使覆盖项的价格已被自定义也会级联；过滤器属性会去掉它们，与 `charge_filters/create_or_update_batch_service.rb` 一致 |
| 计费项的发票显示名称 | `app/models/charge.rb` | libs/billing | 逻辑已完成 | |
| 每个计费项的税 | `app/services/charges/apply_taxes_service.rb` | libs/billing | 已完成 | `tax_ids` 替换计费项的税；未知的税返回未找到。计费项覆盖项初始不带税，因为 `charges/override_service.rb` 不复制税 |
| 嵌套在套餐与订阅下的计费项 API | `app/controllers/api/v1/plans/charges_controller.rb`、`app/controllers/api/v1/subscriptions/charges_controller.rb` | apps/server | 已完成 | `ChargeService` 带 `plan_id` 或 `subscription_id`；计费项与套餐按 ID 或代码查找。更新使用字段掩码：除非模型改变，未设置的属性保持不变。订阅上的更新会把订阅移到覆盖套餐并更新其计费项覆盖项（`subscriptions/update_or_override_charge_service.rb`）；创建或删除订阅的计费项未实现，因为 v1.55.0 在那里只有 index、show 与 update。`accepts_target_wallet` 会存储并返回 |

### 1.12 计费项过滤器

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 带有自身属性的计费项过滤器 | `app/models/charge_filter.rb`、`app/services/charge_filters/` | libs/metering | 逻辑已完成 | |
| 全部取值通配符（`__ALL_FILTER_VALUES__`） | `app/models/charge_filter_value.rb` | libs/metering | 逻辑已完成 | |
| 过滤器优先级（最具体的过滤器胜出） | `app/services/events/billing_period_filters/matching_and_ignored_service.rb` | libs/metering | 逻辑已完成 | |
| 事件与过滤器匹配，未匹配事件归入默认桶 | `app/services/charge_filters/event_matching_service.rb` | libs/metering | 逻辑已完成 | |
| 为计费周期查询最小化被忽略的过滤器 | `app/services/charge_filters/`（`MinimizeIgnoredFilters`） | libs/metering | 逻辑已完成 | |
| 过滤器代码（slug 加哈希） | `app/models/charge_filter.rb` | libs/billing | 已完成 | 新过滤器取其取值在该计费项上空闲的代码；保留的过滤器保留其代码。计费项过滤器 API 由 `ChargeService` 在套餐与订阅上提供，包括 `GetChargeFilter`（`config/routes/plan_nested_api.rb:7`、`shared_api.rb:68`） |
| 把过滤器变更级联到子套餐 | `app/services/charge_filters/create_or_update_batch_service.rb` | libs/billing | 已完成 | 只保留带代码的路径（§2）：每个被创建、更新或销毁的过滤器一个任务，按代码找到覆盖项中的副本；价格已自定义的副本保留价格，只接收定价分组键（`charge_filters/cascade_service.rb`）。计费项的创建与更新级联选出有活跃或待生效订阅的子项，每 20 个入队一个批处理任务（`charges/create_children_job.rb`、`update_children_job.rb`）；更新批处理在计费队列上运行，不加咨询锁 |

### 1.13 固定计费项与附加项

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 附加项的增删改查（金额、货币、发票显示名称、税） | `app/models/add_on.rb`、`app/services/add_ons/` | libs/billing、apps/server | 已完成 | 由基于 libs/billing/addons 的 `AddOnService` 提供；税通过 `tax_ids`（ID 或 `code:`）设置。删除附加项会一并删除它及其固定费用；v1.55.0 是软删除两者（`destroy_service.rb:17-18`）。列表可按名称或代码搜索，与 GraphQL 解析器一致 |
| 套餐上的固定计费项（基于附加项，带单位数） | `app/models/fixed_charge.rb`、`app/services/fixed_charges/` | libs/billing、apps/server | 已完成 | `plans.Service` 提供创建、获取、列表（仅父项，最新在前）、更新与删除。更新是部分更新：未设置的字段保持原值，而 v1.55.0 会按参数重置单位数与属性（`update_service.rb:31-38`）。API 创建要求提供代码；套餐创建或更新时由附加项代码生成（`generate_code_service.rb`） |
| 固定计费项的计费模型 standard、graduated、volume | `app/services/fixed_charges/` | libs/pricing | 逻辑已完成 | |
| 预付固定计费项 | `app/services/invoices/create_pay_in_advance_fixed_charges_service.rb` | libs/billing | 已完成 | `invoicing.Service.InAdvanceFixedCharges` 为活跃或受限订阅在同一时刻立即生效的单位数开票，没有费用时也创建发票；受限订阅的发票保持 open 并发起支付，不发 invoice.created、不生成文档，金额为零时满足支付规则并激活订阅（`lifecycle.Service.ResolveSubscriptionStatus`） |
| 按比例计算的固定计费项 | `app/models/fixed_charge.rb`（`prorated`） | libs/period | 逻辑已完成 | |
| 按订阅覆盖单位数 | `app/services/fixed_charges/override_service.rb`、`app/services/subscriptions/fixed_charge_units_overrides/write_service.rb` | libs/billing、libs/store | 已完成 | 原始套餐上的订阅只改单位数时，记为一行 `fixed_charge_units_overrides`；其他改动会把订阅移到覆盖套餐，并提升其单位数覆盖（`update_or_override_fixed_charge_service.rb`） |
| 单位数变更立即生效或在下一周期生效 | `app/models/fixed_charge.rb`（`apply_units_immediately`） | libs/billing | 逻辑已完成 | |
| 记录单位数随时间变化的固定计费项事件 | `app/models/fixed_charge_event.rb`、`app/services/fixed_charges/emit_events_service.rb` | libs/billing | 已完成 | 事件记录变更时刻及是否立即生效，费用据此推出生效时间，而 v1.55.0 存储的是推迟后的时间戳（`emit_events_service.rb`）。订阅创建时的 `Subscriptions::EmitFixedChargeEventsService` 属于 §1.15 |
| 把固定计费项变更级联到子套餐 | `app/jobs/fixed_charges/` | libs/billing | 已完成 | 由任务在仍有活跃订阅的子套餐上创建、更新、删除副本，每批 20 个。子套餐已有（或缺少）副本时跳过，而 v1.55.0 以 `not_found` 失败（`cascade_child_plan_update_service.rb:28`） |
| 每个固定计费项的税 | `app/services/fixed_charges/` | libs/tax | 已完成 | 通过 `tax_ids`（ID 或 `code:`）设置；覆盖项不继承父项的税，与 v1.55.0 一致（`override_service.rb`） |
| 嵌套在套餐与订阅下的固定计费项 API | `app/controllers/api/v1/plans/fixed_charges_controller.rb`、`app/controllers/api/v1/subscriptions/fixed_charges_controller.rb` | apps/server | 已完成 | 由 `FixedChargeService` 提供；订阅的列表与获取显示订阅自己的单位数。订阅范围的创建与删除返回 Unimplemented（订阅控制器只有 index、show 与 update）。没有 premium 或旧版计费限制（`Api::ForbidsLegacyBilling`）；删除返回空响应 |

### 1.14 产品目录（v2）

Lago 有两套目录：上面的套餐/计费项目录，以及受 `product_catalog` 开关控制的较新的产品目录。Tanvo 只实现一套目录，其模型同时覆盖两者（§3）：本节与 §1.10–§1.13 的每一行都在范围内。

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 产品类别 | `app/models/product_category.rb` | libs/core、libs/billing/products、apps/server | 已完成 | `ProductService` 的类别 RPC。已挂到套餐时拒绝删除；删除会软删除（`deleted_at`）类别及其产品、产品的过滤器与取值、费率卡与费率，与 `product_categories/destroy_service.rb` 一致；代码只在未删除的行中唯一，按 ID 查找仍返回已软删除的行。返回 `products_count` |
| 产品（计量型、固定型） | `app/models/product.rb` | libs/core、libs/billing/products、apps/server | 已完成 | 计量型产品销售一个指标，固定型不销售指标；类型不可变。挂到套餐后代码与类别被锁定；删除会软删除产品及其过滤器、过滤器取值、费率卡与费率。返回 `filters_count` |
| 产品过滤器与过滤器取值 | `app/models/product_filter.rb` | libs/core、libs/billing/products、apps/server | 已完成 | 取值选中一个指标过滤器键及其一个取值，或整个过滤器（不带取值）；按创建时间从旧到新列出，支持 lago 的名称或代码搜索。被产品过滤器选中的指标过滤器或取值不能被移除（§1.8）。取值变化时软删除现有取值并插入新取值；删除会软删除过滤器及其取值、限定于它的费率卡与费率。合同为过滤器定价后取值被锁定（`attached_to_subscriptions`）（§1.22） |
| 费率卡（计费时机、按比例计算、发票展示、重新汇总、定价单位、锁定） | `app/models/rate_card.rb` | libs/core、libs/billing/products、apps/server | 已完成 | `RateCardService`。列表可按产品、产品过滤器、类别以及无类别筛选；返回 `rates_count` 与 `active_rate`，与 `v2/rate_card_serializer.rb` 一致。删除会软删除费率卡及其费率。合同直接或通过其套餐应用该费率卡后，待生效以外的费率被冻结（§1.22） |
| 时间线上的费率卡费率（`effective_from`） | `app/models/rate_card_rate.rb`、`app/services/rate_card_rates/create_service.rb` | libs/core、libs/billing/products、apps/server | 已完成 | 有效费率之后只能追加；`effective_from` 不早于组织时区的今天，后付费率卡上为 UTC 午夜。每个费率带有其 `RateStatus`（待生效、生效中、已终止：`rate_card_rate.rb:76-89`）；`ListRates` 分页。只有待生效的费率可以删除；删除为软删除 |
| 费率模型及其与产品类型的兼容性 | `app/services/rate_card_rates/`（`ModelCompatibility`） | libs/core、libs/billing/products | 已完成 | |
| 每个费率的计费间隔数与单位 | `app/models/rate_card_rate.rb` | libs/core、libs/billing/products | 已完成 | |
| 区间规范化 | `app/services/rate_card_rates/`（`NormalizeRanges`） | libs/billing/products | 已完成 | 边界为精确小数，因此 lago 按浮点比较的边界会被接受；区间错误报告在 `properties` 下 |
| 按比例累进费率按事件填充阶梯（允许小数阶梯边界；移除先清空最高阶梯） | `app/services/charge_models/prorated_adjacent_graduated_service.rb` | libs/pricing | 已完成 | |
| 目录套餐与套餐费率卡 | `app/models/catalog_plan.rb`、`app/models/plan_rate_card.rb` | libs/core、libs/billing/products、apps/server | 已完成 | `PlanRateCardService`。lago 的目录套餐即 Tanvo 的套餐（§3）；套餐费率卡即 lago 的已应用费率卡。被合同使用的套餐被锁定（`plan_locked`，`CatalogPlan#attached_to_contracts?`）（§1.22）。删除会软删除套餐费率卡及其阶段；删除套餐会软删除其套餐费率卡（`catalog_plans/destroy_service.rb`）。套餐费率卡带有其 `rate_phases`（GraphQL `PlanAppliedRateCard`）与 `rate_phases_count`。应用了费率卡的套餐不能更改币种（`currency not_editable_with_applied_rate_cards`，`catalog_plans/update_service.rb:30-34`） |
| 费率阶段（爬坡）及其不变量 | `app/models/rate_phase.rb` | libs/billing/products、apps/server | 已完成 | 套餐费率卡上的阶段 RPC，以及创建与更新时按位置替换的嵌套 `rate_phases`；嵌套错误报告在 `rate_phases` 下。替换整个爬坡会软删除旧阶段；单个阶段原地创建、移动或删除，删除为软删除并为其后的阶段重新编号 |
| 费率覆盖 | `app/models/rate_override.rb` | libs/billing/products、apps/server | 已完成 | 属于其阶段；错误报告在 `rate_override` 下。无法发送结构性字段，因此不会出现 `not_overridable` |
| 目录代码格式 | `app/models/concerns/`（`CatalogCodeFormat`） | libs/core | 已完成 | |
| 带游标分页的目录 API | `app/controllers/api/v2/` | apps/server | 已完成 | 各处一律游标分页；没有 beta 请求头。删除 RPC 返回被删除的记录。产品类别、产品、产品筛选器与费率卡记录活动日志（费率的变更记为其费率卡的 `rate_card.updated`） |

### 1.15 定价单位

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 定价单位（名称、代码、最多 3 个字符的简称、描述） | `app/models/pricing_unit.rb`、`app/services/pricing_units/` | libs/billing、apps/server | 已完成 | 公开 API，而不只供管理界面使用：由基于 libs/billing/pricingunits 的 `PricingUnitService` 提供，没有 v1.55.0 的高级版检查（[架构 §14](architecture.zh-CN.md#14-企业版ee)）。代码不可修改，也没有删除操作，与 v1.55.0 一致；列表可按名称和代码搜索（`search`，即 `pricing_units_resolver.rb` 中 lago 的 `search_term`） |
| 以定价单位计价并带换算率的计费项 | `app/models/applied_pricing_unit.rb` | libs/pricing | 逻辑已完成 | |
| 在费用上记录定价单位用量 | `app/models/pricing_unit_usage.rb` | libs/billing | 逻辑已完成 | |

### 1.16 权益

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 特性（代码、名称、描述） | `app/models/entitlement/feature.rb`、`app/services/entitlement/` | libs/core、libs/billing、apps/server | 已完成 | `entitlements.Service` 与 `FeatureService`。仓储按 `feature_destroy_service.rb:22-27` 的做法软删除特性（`deleted_at`）。列表按创建时间倒序，搜索代码、名称和描述（`features_query.rb:18-30`）。更新按代码合并给出的特权（`partial: true`，`features_controller.rb:65-69`） |
| 整数、字符串、布尔、选择类型的特权 | `app/models/entitlement/privilege.rb` | libs/core、libs/billing | 已完成 | 仓储在删除特性时、或特性更新不再列出某特权时软删除该特权（`deleted_at`，`feature_update_service.rb:99-111`）。选择项为 `select_options`（lago 为 `config`）；取值类型不可更改。`DELETE /features/:code/privileges/:code`（`features/privileges_controller.rb`）对应 `FeatureService.DeleteFeaturePrivilege` |
| 套餐权益与特权取值 | `app/services/entitlement/plan_entitlements_update_service.rb` | libs/core、libs/billing | 已完成 | 取值为字符串，按类型校验；布尔值存为 `true`/`false`。错误为 `<code>_privilege_value` 的 `value_is_invalid` 或 `value_not_in_select_options`，与 lago 相同 |
| 订阅的权益覆盖与移除 | `app/services/entitlement/subscription_entitlements_update_service.rb` | libs/core、libs/billing | 已完成 | 子套餐的订阅覆盖以父套餐为准，与 lago 相同 |
| 订阅解析后的权益 | `app/services/entitlement/` | libs/core、libs/billing | 已完成 | 每个取值带 `source`（套餐或订阅），取代 lago 的 `plan_value` 与 `override_value` |
| 特性 webhook `feature.created`、`feature.updated`、`feature.deleted` | `app/services/webhooks/features/` | libs/billing、libs/webhooks | 已完成 | 特性更新与删除还会为每个授予该特性的套餐发送 `plan.updated`，订阅权益变更发送 `subscription.updated`，与 lago 相同 |
| 特性与权益 API | `app/controllers/api/v1/features_controller.rb`、`app/controllers/api/v1/plans/entitlements_controller.rb`、`app/controllers/api/v1/subscriptions/entitlements_controller.rb` | apps/server | 已完成 | `FeatureService` 与 `EntitlementService`。套餐权益用 `PUT` 整体替换（lago 为 `POST`），并逐个特性 upsert（lago 的 `PATCH` 接收特性映射）。订阅按 ID（任意状态）或 `external_id:`（活跃）查找；不暴露 lago 的 `subscription_status` 筛选。订阅权益没有单条读取和整体替换，与 lago 相同（对订阅调用 `GetEntitlement` 与 `ReplaceEntitlements` 返回 Unimplemented）。删除不返回正文，删除特权返回该条权益（lago 返回整个集合） |

### 1.17 事件摄取

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 单个事件摄取 | `app/services/events/create_service.rb` | libs/ingest | 已完成 | `EventService.IngestEvent`、`POST /v1/events`；apps/server 中有端到端测试 |
| 批量摄取（最多 100 个事件） | `app/services/events/create_batch_service.rb` | libs/ingest | 逻辑已完成 | 由 `EventService.IngestEvents` 提供；尚无端到端测试 |
| 事件时间戳解析（秒、带小数的秒，默认取当前时间） | `app/services/events/common_factory.rb` | libs/ingest | 逻辑已完成 | |
| 事件字段：交易 ID、外部订阅 ID、代码、属性、精确总金额 | `app/models/event.rb` | libs/ingest | 逻辑已完成 | JSON 属性中的数字保持精确，小数亦然（lago 的 JSON 解析使整数精确、小数转为浮点数）；二进制 protobuf 与 gRPC 客户端发送 double，精确值以字符串发送（[架构 §4.2](architecture.zh-CN.md#42-经由-vanguard-的-rest)） |
| 摄取和事件过滤中 `external_contract_id` 作为 `external_subscription_id` 的别名 | `app/controllers/api/v1/events_controller.rb` | libs/ingest | 逻辑已完成 | 两者都发送时以显式的 `external_subscription_id` 为准 |
| 按组织、外部订阅 ID 与交易 ID 去重 | `app/services/events/create_service.rb`、`app/services/events/validate_creation_service.rb`、`db/structure.sql`（`index_unique_transaction_id`） | libs/ingest、libs/store、libs/eventstore | 逻辑已完成 | 所有存储使用同一个键；Lago 的 ClickHouse 存储在查询时去重，并且还以代码和时间戳为键，而 Tanvo 把在另一时间戳的重发视为重复 |
| 事件上的请求元数据（user agent、IP） | `app/services/events/create_service.rb` | libs/ingest | 已完成 | PostgreSQL 路径上存于 `public.events.metadata`，Kafka 路径上存于 ClickHouse 的 `events_raw`，由原始消息与富化消息携带；lago 的 Kafka 消息不含元数据（`app/services/events/kafka_producer_service.rb:36`），其 ClickHouse 事件也没有（`app/models/clickhouse/events_raw.rb:20`）。以事件的 `user_agent` 与 `ip_address` 返回（lago 仅在 GraphQL 中提供，`app/graphql/types/events/object.rb:20`）；地址按 `request.remote_ip`（`app/controllers/api/v1/events_controller.rb:219`）解析，受信任代理为 ActionDispatch::RemoteIp 的默认集合加上 `TANVO_TRUSTED_PROXIES`，不同之处在于仅当对端受信任时才读取转发头，且与 `X-Forwarded-For` 矛盾的 `Client-Ip` 会被忽略而不是使请求失败（actionpack `remote_ip.rb:151`） |
| 事件校验（指标代码、订阅、数值字段） | `app/services/events/validate_creation_service.rb` | libs/ingest | 进行中 | |
| 摄取后的事件校验与 `events.errors` webhook | `app/services/events/post_validation_service.rb` | libs/billing, libs/store | 已完成 | `billing.Engine.ValidateEvents` 每小时 :05 为有 webhook 端点且上一个整点小时内创建过事件的组织运行；一个 `events.errors` webhook（对象类型 `events_errors`）按 `db/views/last_hour_events_mv_v04.sql` 的规则列出代码无效、聚合属性缺失或非数值、过滤器取值无效的事务 ID（代码只对应已弃用指标的事件跳过）。以 `events (organization_id, created_at)` 上的直接查询取代物化视图；v1.55.0 从不填写的 `missing_group_key` 不保留。与 lago 只校验存入 PostgreSQL 的事件一致（`app/services/events/create_service.rb:37`），Kafka 管道的事件不做后置校验。`TANVO_DISABLE_EVENTS_VALIDATION`（lago `LAGO_DISABLE_EVENTS_VALIDATION`）将其移出调度。摄取仍同步拒绝能同步检查的内容 |
| 获取单个事件与列出事件 | `app/controllers/api/v1/events_controller.rb`、`app/queries/events_query.rb` | libs/ingest、apps/server | 已完成 | `EventService.GetEvent`（`GET /v1/events/{transaction_id}`）与 `ListEvents`（`GET /v1/events`）；过滤器可限定 `code`、`external_subscription_id`（=）与 `timestamp`（>=、<=），`timestamp_from_started_at` 从最新订阅的开始时间起算。`GetEvent` 另可接收 `external_subscription_id`（超出 lago），用于在共享同一交易 ID 的事件中选择；未提供时返回最先摄入的事件（`events_controller.rb:55`）。PostgreSQL 列表按事件 ID 打破并列，lago 按交易 ID（`events_query.rb:22`）；ClickHouse 上列表返回每个事件的当前副本（lago 读取 `events_raw` 不去重，`events_query.rb:24,50`），且 ClickHouse 事件没有 ID |
| 列出富化事件 | `app/controllers/api/v1/events_controller.rb`（`events_enriched`） | apps/server | 计划中 | |
| 预付事件的费用估算 | `app/services/fees/estimate_pay_in_advance_service.rb` | libs/billing、apps/server | 已完成 | `EventService.EstimateFees`（`POST /v1/events:estimate_fees`），实现于 libs/billing/estimates。缺少 `external_subscription_id` 时由请求校验拒绝（400），lago 返回 404（`estimate_pay_in_advance_service.rb:16`）。响应为估算费用（计费项、过滤器、金额、税额、数量、单价、事件数、分组），而非 lago 带适用税的完整费用序列化（`events_controller.rb:160`）；税务提供者失败会使估算失败 |
| 即时费用估算与批量即时估算 | `app/services/fees/estimate_instant/` | libs/billing、apps/server | 已完成 | `EventService.EstimateInstantFees`（`POST /v1/events:estimate_instant_fees`）以批量形式同时承担 lago 的两个端点。单价以货币单位表示，lago 的 `precise_unit_amount` 以分表示（`estimate_instant/base_service.rb:39,78`）；数量不是数字时返回校验错误，lago 抛出异常（`base_service.rb:28`）；表达式求值失败与 lago 一样被忽略 |
| 富化：订阅、指标、表达式、聚合值、分组键、精确金额；查找计费项只为路由预付事件 | `app/services/events/enrich_service.rb`、events-processor `processors/` | libs/ingest | 逻辑已完成 | 富化行上没有计费项或过滤器；过滤器在查询或聚合到桶时根据原始属性匹配 |
| 带操作类型（增加/移除）的富化事件 | `app/models/enriched_event.rb` | libs/ingest | 逻辑已完成 | |
| 目录变更后重新富化 | `app/services/events/re_enrich_all_service.rb` | libs/ingest | 计划中 | |
| 后处理：用量监控、钱包刷新、目标钱包、预付计费项 | `app/services/events/post_process_service.rb` | libs/ingest | 进行中 | 用量监控、钱包刷新与预付计费项已在两种模式下装配：PostgreSQL 模式在摄取事务中完成；Kafka 模式由写入消费者为其写入的事件入队预付任务，`usage.refresh` 消费者把信号交给计费。Lago 从单独的主题在 15 秒后为预付事件计费（`app/consumers/events_charged_in_advance_consumer.rb`、`app/services/events/stores/clickhouse_store.rb:11`） |
| 指向特定钱包的事件 | `app/services/events/`（`target_wallet_code`） | libs/ingest、libs/billing | 已完成 | 当事件所属套餐在该指标上的计费项接受目标钱包时，`target_wallet_code` 属性对事件用量分组（富化、用量桶、预付与后付费用、进行中余额、预付额度分配）；事件把它作为仅输出字段返回。未移植针对未知钱包的 `event.error` webhook 和实时警告 |
| 由事件创建预付计费项费用，按来源去重 | `app/services/fees/create_pay_in_advance_service.rb`、`app/services/events/pay_in_advance_service.rb` | libs/billing | 逻辑已完成 | 除非指标按事件计数，或事件带有该指标的字段，否则事件不计费用（`events/pay_in_advance_service.rb:69-74`；自定义聚合未移植） |
| 带消费者的 Kafka 摄取模式 | events-processor `processors/` | libs/ingest | 逻辑已完成 | 内置于 `consumer` 角色：富化与 ClickHouse 写入消费者组已装配；尚无端到端测试 |
| 无法处理事件的死信主题 | events-processor `processors/dlq` | libs/ingest | 逻辑已完成 | |
| HTTP、SQS、Kinesis 来源连接器 | events-processor 连接器 | libs/ingest | 计划中 | 也支持 Kafka |
| 消费者中的目录缓存 | events-processor（Badger 缓存、Debezium） | libs/ingest、apps/server | 已完成 | 以变更通知代替 CDC（§3）。对指标、计费项或订阅的每次已提交变更都会在写入事务内调用 `pg_notify('tanvo_catalog_changes', …)`，因此只在提交时送达；每个 API 与消费者进程都 LISTEN 并逐出该组织的缓存条目，且每次（重新）连接时清空整个缓存，因为期间可能漏掉通知；写入进程在提交后也会立即逐出。TTL 作为兜底保留。所有摄取模式都从 PostgreSQL 读取目录，因此 ClickHouse 与 Kafka 无需额外处理。lago 对指标的 Segment 跟踪未移植（核心不含产品分析） |

### 1.18 事件存储

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| PostgreSQL 事件存储（事件与富化事件分区） | `app/services/events/stores/postgres_store.rb` | libs/eventstore | 逻辑已完成 | 原生分区（§3） |
| 带 FINAL 去重与重试的 ClickHouse 事件存储 | `app/services/events/stores/clickhouse_store.rb` | libs/eventstore | 逻辑已完成 | |
| 存储查询：计数、求和、最大值、最新值、唯一计数、加权求和、分组、按比例、明细、最后一个事件、不同取值 | `app/services/events/stores/base_store.rb` | libs/eventstore | 逻辑已完成 | |
| 存储查询上的计费周期过滤 | `app/services/events/stores/` | libs/eventstore | 逻辑已完成 | |
| ClickHouse 原始表、富化表与死信表 | `db/clickhouse_migrate/` | libs/eventstore | 计划中 | |
| 预聚合用量桶（Lago 中为 15 分钟） | `app/models/clickhouse/usage_bucket.rb`、`app/services/events/stores/usage_bucket_store.rb` | libs/eventstore | 逻辑已完成 | 按维度取值分桶的 15 分钟桶，在 PostgreSQL 与 ClickHouse 上于事件写入时维护；窗口边界精确地从事件读取；未声明的过滤值被折叠；预付、按比例、周期性和目标钱包计费项读取事件；`RepairBuckets` 每 10 分钟从上次运行的开始时间（保存在 ClickHouse 中）起运行；`BucketStats` 每小时报告一次（直方图，以及单个桶超过 10 000 行时的警告）；删除行数过多的定义尚未自动化 |
| 循环指标的缓存聚合 | `app/models/cached_aggregation.rb` | libs/metering | 已完成 | 由开票保存（每个预付事件一行，循环状态每个计费项、分组和时刻至多一行），由费用计算和当前用量读取（领域 §10.3）。查找仅在设置了计费项过滤器时才按其过滤，并在为事件计价时排除没有事件交易号的状态（`app/services/billable_metrics/aggregations/base_service.rb:223-224`）；聚合值可为空。没有 `current_amount`（未移植自定义聚合） |

### 1.19 用量

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 订阅的当前用量 | `app/services/invoices/customer_usage_service.rb` | libs/billing | 逻辑已完成 | |
| 到周期结束的预计用量 | `app/services/invoices/customer_usage_service.rb`（`with_projection`）、`app/services/fees/projection_service.rb` | libs/billing | 逻辑已完成 | 由每笔当前用量费用推导：standard、graduated、package 和 volume 对预测单位数重新定价，其他模型缩放金额；比例基于计费项周期（[领域模型 §10.3](domain.zh-CN.md#103-用量)） |
| 按周期的历史用量 | `app/queries/past_usage_query.rb` | libs/billing | 逻辑已完成 | |
| 用量过滤（按指标、按过滤器、完整用量） | `app/services/invoices/customer_usage_service.rb` | libs/billing | 逻辑已完成 | |
| 计费项级的用量缓存 | `app/services/subscriptions/charge_cache_service.rb` | libs/billing | 计划中 | 惰性校验：条目携带最新已摄取事件，存在更新事件时重新计算，摄取时从不删除；全量用量有单独的键 |
| 实时用量刷新 | `app/services/subscriptions/`（`realtime_usage`） | libs/billing | 计划中 | 有了用量桶，这成为默认行为 |
| 按小时的用量明细 | `app/graphql/resolvers/subscriptions/hourly_usage_resolver.rb` | libs/metering | 逻辑已完成 | 序列由计费项过滤器构建（`HourlySeriesFor`），其他取值归入 `__other__`；窗口不早于订阅开始 |
| 生命周期用量（历史、已开票、当前） | `app/models/lifetime_usage.rb`、`app/services/lifetime_usages/` | libs/billing | 逻辑已完成 | |
| 外部历史用量金额 | `app/controllers/api/v1/subscriptions/lifetime_usages_controller.rb` | libs/billing | 逻辑已完成 | |
| 客户用量 API：当前、预测与历史用量 | `app/controllers/api/v1/customers/usage_controller.rb`、`app/controllers/api/v1/customers/projected_usage_controller.rb` | apps/server | 已完成 | 基于 libs/billing/subscriptions 的 `UsageService.GetCurrentUsage`（以 `include_projection` 代替预测路由，不受 `projected_usage_enabled` 限制，`projected_usage_controller.rb:7`）与 `ListPastUsage`。分组用量位于各过滤器之下（lago 将过滤器嵌套在分组之下，`app/serializers/v1/customers/charge_usage_serializer.rb`），过滤器取值以逗号连接。`filter_by` 与 `group_by_attributions` 为 Unimplemented（`app/controllers/concerns/api/v1/customers/usage_filters.rb`、`shared_api.rb:25`）；lago 的 `full_usage`、`charge_id`/`charge_code`、`skip_grouping`、`filter_by_presentation` 参数及展示拆分未暴露。`GetHourlyUsage` 为 Unimplemented（v1.55.0 仅有 GraphQL，`app/graphql/types/query_type.rb:130`），`ListDailyUsage` 亦然（v1.55.0 无每日用量 API） |
| 生命周期用量 API | `app/controllers/api/v1/subscriptions/lifetime_usages_controller.rb`、`app/serializers/v1/lifetime_usage_serializer.rb` | apps/server | 已完成 | `SubscriptionService.GetLifetimeUsage` 与 `UpdateLifetimeUsage`；`external_id:` 引用选取活跃订阅。金额为主币单位的小数，精度超过货币最小单位时拒绝；阈值给出 `remaining_amount`（lago 为 `completion_ratio`），以 `refreshed_at` 代替 `from_datetime`/`to_datetime`。订阅关闭渐进计费时阈值为空（lago 的 `has_progressive_billing?` 忽略该标志）。`include_metrics` 无对应项，忽略 |
| 生命周期用量刷新 | `app/jobs/clock/refresh_lifetime_usages_job.rb` | libs/billing | 已完成 | 与 lago 相同，扫描只处理已开票用量被标记的记录（`app/jobs/clock/refresh_lifetime_usages_job.rb:9-19`）；当前用量由用量信号刷新 |
| 按指标细分的生命周期用量 | `app/services/lifetime_usages/` | libs/billing | 计划中 | |
| 带差值与历史回填的每日用量 | `app/models/daily_usage.rb`、`app/services/daily_usages/` | libs/billing | 已完成 | 每小时扫描进入其时区新一天的组织，并分散在 `TANVO_DAILY_USAGE_SCHEDULING_JITTER`（默认 30 分钟）内执行；每个订阅每个本地日计算一次（`app/services/daily_usages/compute_all_service.rb:142-148`）；从发票回填时只计该订阅自己的费用（`app/services/daily_usages/fill_from_invoice_service.rb:61-62`）；历史重建（`lib/tasks/daily_usages.rake`）是 `billing.rebuild_daily_usage_history` 任务，可选择先以每批 10,000 行删除已存储的行，再入队回填；快照与 lago 一样会给出已丢弃的计量指标与计费项过滤器（`app/models/fee.rb:18,33`）；无高级版限制 |
| 订阅活动处理（哪些订阅发生了变化） | `app/services/usage_monitoring/process_subscription_activity_service.rb` | libs/billing | 已完成 | 以每个订阅每分钟一个延迟任务代替 `subscription_activities` 表：运行期间到达的信号落入下一分钟的任务，而 lago 的记录只在处理后删除，会吞掉该信号（`app/services/usage_monitoring/track_subscription_activity_service.rb:23-26`）；失败会立即重试，共四次（`app/jobs/usage_monitoring/process_subscription_activity_job.rb`） |

### 1.20 订阅

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 状态 pending、active、terminated、canceled、incomplete | `app/models/subscription.rb` | libs/core | 计划中 | |
| 计费时间：自然周期或周年 | `app/models/subscription.rb` | libs/period | 逻辑已完成 | |
| 订阅开始日期（过去或未来）与结束日期 | `app/services/subscriptions/create_service.rb` | libs/billing | 逻辑已完成 | |
| 订阅名称与外部 ID | `app/models/subscription.rb` | libs/core | 计划中 | |
| 创建订阅时覆盖套餐 | `app/services/subscriptions/create_service.rb`、`app/services/plans/override_service.rb` | libs/billing、apps/server | 已完成 | 创建、更新与换套餐时的 `plan_overrides`。创建时，新订阅仅覆盖单位数的覆盖写入单位数覆盖而非子套餐（`create_service.rb:171-174, 284-294`），同一套餐上的当前订阅保留其套餐。更新时，子套餐原地更新：其货币与试用期保持不变（`app/services/plans/update_service.rb:40-49`），保留每个固定计费项并叠加给出的值，不属于子套餐的固定计费项键为未找到（`app/services/subscriptions/update_service.rb:260-288`），给出的计费项替换子套餐的计费项，未知的键创建计费项，未给出的被删除（`plans/update_service.rb:245-276`）。父套餐则被覆盖，订阅的单位数覆盖并入子套餐。带覆盖的换套餐覆盖所请求的套餐，由其金额决定升级或降级（`create_service.rb:136-148`）；将来开始的订阅直接换到所请求的套餐（`plan_upgrade_service.rb:77-80`、`plan_downgrade_service.rb:82-84`）。没有高级版或旧计费的限制（`create_service.rb:37-41`）。激活时在开始后一秒记录每个固定计费项生效的单位数（含订阅自身的单位数），从未完成状态激活时除外（`app/services/subscriptions/activate_service.rb:38,74,104,130,174-179`、`emit_fixed_charge_events_service.rb`）；回溯创建亦然（`create_service.rb:201-207`） |
| 每个订阅的计费项、过滤器与固定计费项端点 | `app/controllers/api/v1/subscriptions/` | apps/server | 已完成 | ChargeService 与 FixedChargeService 的 `subscription_id` 路径：列出、读取与更新（首次修改时覆盖套餐），过滤器增删改查；v1.55.0 没有创建与删除端点，返回 `unimplemented` |
| 按年度金额区分升级与降级（升级立即生效，降级在周期结束时生效） | `app/services/subscriptions/plan_upgrade_service.rb` | libs/billing | 逻辑已完成 | |
| 终止时选择贷项通知单方式（额度、跳过、退款、抵销） | `app/services/subscriptions/terminate_service.rb` | libs/billing | 逻辑已完成 | 仅当存在计费基础费用的记录（subscription starting、periodic、terminating）时，预付基础费用才算已开票；lago 按周期边界匹配任何记录，包括预付费用和渐进式计费记录（`app/services/subscriptions/terminate_service.rb:183`） |
| 终止时选择是否开发票（生成、跳过） | `app/services/subscriptions/terminate_service.rb` | libs/billing | 逻辑已完成 | 恰好位于 `terminated_at` 的事件不属于已终止订阅的任何周期（领域 §10.2）；lago 会对其计费（`app/models/event.rb:18`） |
| 税额待定时拒绝终止 | `app/services/subscriptions/terminate_service.rb` | libs/billing | 逻辑已完成 | |
| 取消 pending 状态的订阅 | `app/services/subscriptions/terminate_service.rb` | libs/billing | 逻辑已完成 | 不记录取消原因；被取消的订阅发出 `subscription.terminated`，被取消的下一个订阅不发出 webhook（领域模型 §9.2） |
| 取消原因：付款失败、超时、手动 | `app/models/subscription.rb` | libs/core | 计划中 | |
| 每个订阅的支付方式 | `app/models/subscription.rb` | libs/billing | 逻辑已完成 | |
| 跨订阅合并发票 | `app/models/subscription.rb` | libs/billing | 逻辑已完成 | |
| 按订阅关闭渐进式计费 | `app/models/subscription.rb` | libs/billing | 逻辑已完成 | |
| 采购订单号 | `app/models/subscription.rb` | libs/core | 计划中 | |
| 按订阅覆盖计费主体 | `app/models/subscription.rb` | libs/core | 计划中 | |
| 跳过每日用量计算 | `app/models/subscription.rb` | libs/billing | 已完成 | |
| 激活规则：要求付款，可设超时 | `app/models/subscription/activation_rule.rb`、`app/services/subscriptions/activation_rules/` | libs/billing | 逻辑已完成 | 门控 open 发票的付款状态变为成功或失败时，`billing.resolve_payment_gate` 任务解析该规则（`Invoices::UpdateService#handle_payment_gated_activation`、`payment/resolve_service.rb`）：成功时定稿发票（`invoice.created`、文档），所有规则满足后激活订阅；失败时关闭发票并以 `payment_failed` 取消订阅。由手动付款与提供者付款更新触发；激活过期或被拒绝时取消待处理的提供者付款（`payments.cancel_payment`） |
| 激活规则状态（inactive、pending、satisfied、not applicable、declined、failed、expired） | `app/models/subscription/activation_rule.rb` | libs/core | 计划中 | |
| 激活前要求发票已付的门槛 | `app/services/subscriptions/activate_service.rb` | libs/billing | 逻辑已完成 | |
| 创建订阅时预授权付款（Stripe） | `app/services/payment_providers/stripe/payments/authorize_service.rb` | providers/stripe | 计划中 | |
| 在开始日期激活 pending 订阅 | `app/jobs/clock/activate_subscriptions_job.rb` | libs/billing | 逻辑已完成 | 按订阅调度（§3） |
| 在结束日期终止 | `app/jobs/clock/terminate_ended_subscriptions_job.rb` | libs/billing | 逻辑已完成 | |
| incomplete 订阅过期 | `app/jobs/clock/expire_incomplete_subscriptions_job.rb` | libs/billing | 已完成 | 激活规则的过期时间早于或等于当前时刻时过期 |
| 结束日期前的终止提醒（默认 15 天与 45 天） | `app/jobs/clock/subscriptions_to_be_terminated_job.rb` | libs/billing | 已完成 | 天数由 `TANVO_SUBSCRIPTION_TERMINATION_ALERT_DAYS` 设置；每个订阅每个 UTC 日最多一次提醒 |
| 跨升级保留试用期（保留最初的开始日期） | `app/models/subscription.rb`（`trial_end_date`） | libs/period | 逻辑已完成 | |
| 试用结束检测与免费试用计费 | `app/jobs/clock/free_trial_subscriptions_biller_job.rb` | libs/billing | 逻辑已完成 | |
| 周期计费选择（今天哪些订阅需要计费） | `app/services/subscriptions/organization_billing_service.rb` | libs/billing | 逻辑已完成 | 按订阅调度的任务加对账（§3） |
| 同一张发票上一起计费的订阅分组 | `app/services/subscriptions/organization_billing_service.rb` | libs/billing | 逻辑已完成 | |
| 订阅列表过滤（状态、套餐、客户、是否被覆盖） | `app/queries/subscriptions_query.rb` | libs/store | 逻辑已完成 | 两种存储均已实现查询，移植的规格用例在 porttest 中；套餐过滤排除已删除的套餐；最近排序依次为 subscription_at、created_at（均降序），再按 id 升序；由 `ListSubscriptions` 提供 |
| 订阅 API | `app/controllers/api/v1/subscriptions_controller.rb`、`app/serializers/v1/subscription_serializer.rb` | apps/server | 已完成 | 基于 libs/billing/subscriptions 的 `SubscriptionService`：创建（以 `external_id:` 引用的不存在客户会以默认计费主体创建）、读取、列表、更新、换套餐、终止、取消。对已有外部 ID 换套餐是独立的 RPC（lago 复用创建，`create_service.rb:59`）。列表默认只含活跃订阅（`app/controllers/concerns/subscription_index.rb:11`）；按 `external_id:` 的 `GetSubscription` 依次取活跃、未完成、待生效的订阅，没有 lago 的 `status` 参数。按 `external_id:` 终止取活跃订阅，`CancelSubscription` 取待生效、否则未完成的订阅（lago 以 `status=pending` 调用 `terminate`，`subscriptions_controller.rb:151`）。创建时接受终止选项（lago 的 `create_params` 不允许）。将 `subscription_at` 移到过去不发出预付固定费用事件（lago `EmitFixedChargeEventsService`）。支持套餐覆盖与用量阈值（§1.20、§1.31）。费率卡与计费锚定日属于合同，由 `ContractService` 提供（§1.22）：不带套餐的创建被拒绝，通过此 API 找不到合同。计费片段没有 API（v1.55.0 无路由）。连接与 Stripe 预授权（`subscriptions_controller.rb:23-71`）参数未暴露 |
| 订阅 webhook `subscription.started`、`updated`、`terminated`、`canceled`、`incomplete`、`termination_alert`、`trial_ended`、`usage_threshold_reached` | `app/services/webhooks/subscriptions/` | libs/webhooks | 计划中 | |

### 1.21 计费周期与日期

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 每周周期边界，自然周期与周年 | `app/services/subscriptions/dates/weekly_service.rb` | libs/period | 逻辑已完成 | |
| 每月周期边界，自然周期与周年（月末周年日） | `app/services/subscriptions/dates/monthly_service.rb` | libs/period | 逻辑已完成 | |
| 每季度周期边界 | `app/services/subscriptions/dates/quarterly_service.rb` | libs/period | 逻辑已完成 | |
| 每半年周期边界 | `app/services/subscriptions/dates/semiannual_service.rb` | libs/period | 逻辑已完成 | |
| 每年周期边界 | `app/services/subscriptions/dates/yearly_service.rb` | libs/period | 逻辑已完成 | |
| 计费项周期边界，包括较长套餐上的按月计费项 | `app/services/subscriptions/dates_service.rb`（`charges_from_datetime`、`charges_to_datetime`） | libs/period | 逻辑已完成 | 连续的半开区间（§4） |
| 固定计费项周期边界 | `app/services/subscriptions/dates_service.rb` | libs/period | 逻辑已完成 | |
| 对边界应用时区 | `app/services/subscriptions/dates_service.rb` | libs/period | 逻辑已完成 | IANA 时区，显式的 `*time.Location` |
| 单日价格与周期天数 | `app/services/subscriptions/dates_service.rb` | libs/period | 逻辑已完成 | |
| 终止当日的边界 | `app/services/subscriptions/dates_service.rb` | libs/period | 逻辑已完成 | |
| 订阅费按比例计算（首个周期、终止、升级、完整周期） | `app/services/fees/subscription_service.rb` | libs/period | 逻辑已完成 | |
| 发票订阅的计费周期边界 | `app/models/billing_period_boundaries.rb` | libs/period | 逻辑已完成 | |
| 跨时区的日期差 | `app/services/utils/datetime.rb`（`date_diff_with_timezone`） | libs/period | 逻辑已完成 | |
| 上一个与下一个计费日期 | `app/services/subscriptions/dates_service.rb` | libs/period | 逻辑已完成 | |

### 1.22 合同与计费片段

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 状态为 pending、active、terminated、canceled 的合同 | `app/models/contract.rb`、`app/services/contracts/` | libs/core、libs/billing/contracts | 已完成 | 合同是标记为 `contract`、由费率卡定价、套餐可选的订阅（[领域模型 §21.4](domain.zh-CN.md#214-合同产品目录)）。由于事件按 `external_id` 路由，合同与订阅共享同一个生效中的 `external_id` 命名空间 |
| 合同费率卡 | `app/models/contract_rate_card.rb`、`app/services/contract_rate_cards/` | libs/billing/contracts | 已完成 | 合同待生效时可创建、读取、列出、更新和删除（否则为 `contract_locked`）；每个产品切片一张费率卡（`product_already_priced`）；费率卡币种须与套餐币种一致，无套餐时与客户币种一致（`currency_does_not_match`） |
| 合同计费数学：间隔、日历、天数、周期、阶段、片段、周期遍历器、计划表 | `app/services/billing/` | libs/period/schedule | 已完成 | 片段的 `billing_at` 在预付时为其开始，在后付时为其（不含的）结束（`Billing::Terms#billing_at_for`） |
| 合同激活 | `app/jobs/clock/activate_contracts_job.rb` | libs/billing/contracts | 已完成 | 重新播种按其他开始日播种的费率卡，重置其时钟，并调度客户的片段；已有同一外部 ID 的有效合同时为 `active_contract_exists`。激活失败只记录日志并留待下一次时钟触发，lago 也从不重试 |
| 创建计费片段 | `app/services/billing_segments/`、`app/jobs/clock/create_billing_segments_job.rb` | libs/billing/contracts | 已完成 | 在客户的计费锁下进行；重叠的片段以 `billing_segment overlapping_periods` 失败且不存储任何内容；预付计量片段以 `processing` 存储 |
| 处理计费片段并排除重叠 | `app/jobs/clock/process_billing_segments_job.rb`、`app/services/billing_segments/process_service.rb` | libs/billing/invoicing | 已完成 | 按本地计费日、币种、计费主体、支付方式与采购订单号每组一张发票（合同不合并发票时每个片段一张），每组在客户片段锁下各自一个事务；先固定后计量的产品费用、本地税、无优惠券与抵扣；重试时完成已处理片段的生成中发票。在该组片段范围内，若合同没有事件到达某个非累计计量片段的过滤器分组，则该片段不经聚合直接定价（`filtered_aggregations`，`fees/charge_service.rb:492-505`）。与 lago 不同，发票将每个合同关联为发票订阅，其费用带有该合同（[领域模型 §24](domain.zh-CN.md)） |
| 合同 API | `app/controllers/api/v2/contracts_controller.rb`、`app/controllers/api/v2/contract_rate_cards_controller.rb` | apps/server、libs/proto | 已完成 | `ContractService`：合同按 `external_id`（读取接受可选状态），费率卡按 `rate_card_code`，费率阶段按 ID。`billing_entity_id` 为 UUID |
| 预付合同在创建时计费 | `app/services/contracts/create_service.rb`、`app/jobs/billing_segments/schedule_job.rb` | libs/billing/contracts | 已完成 | 订阅的计费任务随订阅一起入队（[领域模型 §21.4](domain.zh-CN.md#214-合同产品目录)） |
| 终止有效合同、取消待生效合同，绝不删除 | `app/services/contracts/terminate_service.rb` | libs/billing/contracts | 已完成 | 订阅状态转换（领域模型 §9.2） |
| 有效合同的编辑白名单（`contract_locked`） | `app/services/contracts/update_service.rb` | libs/billing/contracts | 已完成 | 适用于由费率卡定价的订阅 |
| 合同结束日期是其费率卡的唯一结束 | `app/models/contract_rate_card.rb` | libs/billing/contracts | 已完成 | 订阅的 `ending_at` |
| 生效计费锚点日期 | `app/models/contract.rb` | libs/core | 已完成 | 显式锚点，否则为客户时区下的开始日；以 `effective_billing_anchor_date` 返回 |
| 费率阶段复制到合同费率卡上，待生效时可编辑 | `app/services/contracts/materialize_rate_cards_service.rb`、`app/services/rate_phases/` | libs/billing/contracts | 已完成 | |
| 已应用费率卡的更新：单位数和锚点，锚点会重置计费时钟 | `app/services/contract_rate_cards/update_service.rb`、`app/services/contract_rate_cards/reset_billing_clock_service.rb` | libs/billing/contracts | 已完成 | 重新计算 `next_billing_at` |
| 合同的发票自定义段落 | `app/services/invoice_custom_sections/` | libs/billing | 已完成 | 取合同的选择，否则取计费主体的，除非合同跳过它们 |
| 可恢复的费率卡计划表（从最新持久化的周期恢复，片段去重） | `app/services/billing/rate_cards/cycle_walker.rb`、`app/services/billing/rate_cards/build_schedule_service.rb` | libs/period/schedule、libs/billing/contracts | 已完成 | |
| 重试时按边界标识复用费用（发票、费率卡、微秒精度的 UTC 边界） | `app/services/fees/charge_service.rb`、`app/models/billing_period_boundaries.rb` | libs/billing/fees | 已完成 | |
| 预付计量片段按事件定价 | `app/services/events/pay_in_advance_billing_segment_resolver.rb`、`app/services/events/pay_in_advance_service.rb`、`app/services/fees/create_pay_in_advance_service.rb`、`app/services/invoices/create_pay_in_advance_charge_service.rb` | libs/ingest、libs/billing/contracts、libs/billing/fees、libs/billing/invoicing | 已完成 | 绝不在计费时钟触发时定价。没有订阅匹配的事件，若其外部 ID 的活跃合同在该指标的计量产品上有预付费率卡，摄入时即被标记；随后任务按产品与产品过滤器解析出在事件本地日期生效的费率卡（较新的后付费率卡会取代它），以及其计划中覆盖该事件的片段，该片段不从已存储的周期续建，也不存储。每张费率卡每笔交易计一笔费用：片段用量含该事件的价格减去不含它的价格，且仅当事件最佳匹配的产品过滤器（键最多，并列时按创建顺序）就是该费率卡的过滤器。count 不需要属性，其他聚合需要其字段。在发票上显示的费率卡为其费用单独开票（片段的币种，合同的计费主体、采购订单号与自定义区块，优惠券、税与抵扣）；其他费率卡连同税存储费用。作为有意的修正，聚合只保留该费率卡产品过滤器分组内的事件：lago 在此聚合该指标的全部事件，并以 TODO 标注（`charges/pay_in_advance_aggregation_service.rb:57-69`）。与 lago 不同，发票像片段发票一样将合同关联为发票订阅 |

### 1.23 发票

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 发票类型 subscription | `app/services/invoices/subscription_service.rb` | libs/billing | 逻辑已完成 | |
| 发票类型 credit（钱包充值） | `app/services/invoices/paid_credit_service.rb` | libs/billing | 逻辑已完成 | 支付后的入账由 `wallets.Service.HandleCreditInvoicePayment` 完成（lago `Invoices::PrepaidCreditJob`），已由手动付款（`payments.Service.CreateManual`）调用；提供者付款仍等待支付编排中的付款状态更新（`app/services/invoices/update_service.rb:113-117`）。偏差：作废发票会使待处理的购买失败，之后的支付不再入账；lago 让其保持 pending，作废发票被支付后仍发放额度（`app/services/invoices/void_service.rb:30-48`、`app/jobs/invoices/prepaid_credit_job.rb:20-25`） |
| 发票类型 one-off（附加项） | `app/services/invoices/create_one_off_service.rb`、`app/controllers/api/v1/invoices_controller.rb`（`create`） | libs/billing、apps/server | 已完成 | `InvoiceService.CreateInvoice`；附加项、税、计费主体与自定义栏目按 ID 或代码引用。请求接受采购订单号（`create_one_off_service.rb:129`、`invoices_controller.rb:18`）；应用的自定义栏目存储在发票上（`applied_invoice_custom_sections`，`Invoices::ApplyInvoiceCustomSectionsService`），以 `applied_invoice_custom_sections` 返回。不接受 lago 创建时的 `metadata`。订阅、渐进计费、预付（费用与固定费用）、预收费用与付费额度发票也在 lago 的调用处存储其栏目（`subscription_service.rb:61`、`progressive_billing_service.rb:36`、`advance_charges_service.rb:102`、`create_pay_in_advance_charge_service.rb:32`、`create_pay_in_advance_fixed_charges_service.rb:41`、`paid_credit_service.rb:29`）：由订阅的选择与跳过决定，与 `apply_invoice_custom_sections_service.rb` 一致；刷新草稿时移除并重新应用客户的栏目（`refresh_draft_service.rb:63`、`:129`）。钱包与钱包交易尚不能选择栏目，因此付费额度发票采用客户的栏目。待定 VIES 定稿会再次应用客户的栏目（`finalize_pending_vies_invoice_service.rb:25`） |
| 发票类型 advance charges | `app/services/invoices/advance_charges_service.rb` | libs/billing | 逻辑已完成 | |
| 发票类型 progressive billing | `app/services/invoices/progressive_billing_service.rb` | libs/billing | 逻辑已完成 | |
| 预付计费项发票 | `app/services/invoices/create_pay_in_advance_charge_service.rb` | libs/billing | 逻辑已完成 | |
| 状态 draft、finalized、voided、failed、pending、generating、open、closed | `app/models/invoice.rb` | libs/core | 计划中 | |
| 付款状态（pending、succeeded、failed）与税务状态 | `app/models/invoice.rb` | libs/core | 计划中 | |
| 计算得出的金额（费用、优惠券、税、贷项通知单、预付额度、渐进式扣减、合计、应付、已付） | `app/models/invoice.rb`、`app/services/invoices/compute_amounts_from_fees.rb` | libs/billing | 逻辑已完成 | |
| 澳大利亚、阿联酋、新西兰、印度尼西亚、新加坡的税务发票标签 | `app/models/invoice.rb` | libs/documents | 逻辑已完成 | |
| 按客户编号（`PREFIX-CCC-NNN`） | `app/models/invoice.rb`（编号） | libs/billing | 逻辑已完成 | |
| 按计费主体编号（`PREFIX-YYYYMM-NNN`） | `app/models/invoice.rb`（编号） | libs/billing | 逻辑已完成 | |
| 草稿编号（`PREFIX-DRAFT`） | `app/models/invoice.rb` | libs/billing | 逻辑已完成 | |
| 带开票原因的发票订阅 | `app/models/invoice_subscription.rb` | libs/billing | 逻辑已完成 | |
| 一次计费运行中的费用计算顺序 | `app/services/invoices/calculate_fees_service.rb` | libs/billing | 逻辑已完成 | |
| 按零金额设置转为最终状态 | `app/services/invoices/transition_to_final_status_service.rb` | libs/billing | 逻辑已完成 | |
| 重试生成失败的订阅发票 | `app/services/invoices/retry_service.rb`、`app/jobs/clock/retry_generating_subscription_invoices_job.rb` | libs/billing | 已完成 | 周期计费先提交生成中的发票，再在独立事务中生成它：失败时以同一张发票重试一次（计费日 5 分钟后，否则 3 秒后），重试失败记录 `invoice_generation_error`，瞬时错误交回任务的重试策略（`app/jobs/bill_subscription_job.rb:16-60`） |
| 开票日期规则（锚点与调整矩阵） | `app/services/invoices/`（开票日期） | libs/billing | 逻辑已完成 | |
| 由净付款期限（天数）得出付款到期日，按 客户 → 计费主体 解析并快照到发票上 | `app/models/customer.rb`（`applicable_net_payment_term`）、`app/services/invoices/create_generating_service.rb`、`app/services/invoices/refresh_draft_and_finalize_service.rb`、`app/services/invoices/provider_taxes/pull_taxes_and_apply_service.rb`、`app/services/invoices/regenerate_from_voided_service.rb` | libs/billing | 逻辑已完成 | 不建模未被读取的 `payment_term` 和 `payment_term_source` 列（[领域模型 §24](domain.zh-CN.md#24-设计决定)，第 12 条）；定稿、计税完成和重新生成时读取当前期限；客户或计费主体的期限变化时，在更新所在的事务中重算其草稿的到期日（`update_invoice_payment_due_date_service.rb`），API 路径同样如此（lago 仅在 GraphQL 中，`update_service.rb:96-98`） |
| 发票上的采购订单号（来自归并的订阅、钱包交易或钱包、一次性请求），重新生成时沿用或替换；同一次计费中采购订单号不一致时拒绝 | `app/services/invoices/create_generating_service.rb`、`app/services/invoices/paid_credit_service.rb`、`app/services/invoices/regenerate_from_voided_service.rb` | libs/billing | 逻辑已完成 | 重新生成时沿用已作废发票的采购订单号，除非请求给出新值（空白即清除）；周期运行和套餐变更按采购订单号拆分 |
| 带草稿发票的宽限期 | `app/services/invoices/subscription_service.rb` | libs/billing | 逻辑已完成 | |
| 刷新草稿 | `app/services/invoices/refresh_draft_service.rb`、`app/jobs/clock/refresh_draft_invoices_job.rb` | libs/billing | 逻辑已完成 | |
| 宽限期结束时自动定稿 | `app/jobs/clock/finalize_invoices_job.rb` | libs/billing | 逻辑已完成 | 由定时扫描定稿的草稿以扫描时的本地日期开具，并按扫描时的月份编号，即使其任务延迟执行；lago 使用执行任务时的时钟重新设定日期并编号（`app/services/invoices/refresh_draft_and_finalize_service.rb:77`、`app/models/invoice.rb:738`），两者始终一致 |
| 手动定稿与定稿全部草稿 | `app/services/invoices/refresh_draft_and_finalize_service.rb`、`app/services/invoices/finalize_batch_service.rb` | libs/billing、apps/server | 已完成 | `FinalizeInvoice`（非草稿视为不存在，同 `invoices.draft.find_by`），`FinalizeAllInvoices` 为每张草稿排入一个定稿任务并返回数量；`RefreshInvoice` 刷新可见的订阅发票 |
| 带额度与退款金额的作废 | `app/services/invoices/void_service.rb` | libs/billing、apps/server | 已完成 | `InvoiceService.VoidInvoice` |
| 作废时退回优惠券、钱包与贷项通知单 | `app/services/invoices/void_service.rb` | libs/billing | 逻辑已完成 | |
| 作废时作废提供者的税 | `app/services/invoices/provider_taxes/void_service.rb` | libs/billing | 逻辑已完成 | |
| 从已作废的发票重新生成，带预览 | `app/services/invoices/regenerate_from_voided_service.rb`、`app/services/invoices/build_regeneration_preview_service.rb` | libs/billing、apps/server | 已完成 | `InvoiceService.RegenerateInvoice`，对应 lago 的 `regenerateFromVoided` mutation（`app/graphql/mutations/invoices/regenerate_from_voided.rb`）。只重新生成列出的费用，列出两次的费用生成两次（`regenerate_from_voided_service.rb:196-226`）：`fee_id` 复制该费用，状态为待处理，税与优惠券清零，`original_fee_id` 指向链的根，数量不变时复制其展示分项；没有或未知的 `fee_id` 时，按 `subscription_id`、`charge_id` 与 `charge_filter_id` 构建费用（否则 `subscription`、`charge` 或 `charge_filter` 不存在）。数量按计费模型重新定价，百分比或按比例累进计费项只给数量不给价格时为 `invalid_charge_model`，已调整过的费用为 `adjusted_fee` `already_exists`；计费项费用的显示名称总取自调整。`fixed_charge_id`、`add_on_id` 与 `description` 被接受并忽略，与 lago 一致。复制发票订阅，作废发票的订阅标记 `regenerated_invoice_id`；渐进计费发票复制其已应用的用量阈值；应用自定义栏目。`validate_only` 即 lago 的 `Invoices::BuildRegenerationPreviewService`：按当前规则重新计税的作废发票原样预览，重算合计，不存储，忽略费用与采购订单号。不做外部同步检查（草稿从不同步） |
| 重试失败的提供者税额计算 | `app/services/invoices/provider_taxes/pull_taxes_and_apply_service.rb`、`app/services/invoices/retry_service.rb` | libs/billing | 逻辑已完成 | `invoicing.Service.Retry` 重新打开失败的发票（Tanvo 没有 pending 状态，故为 open），将其税设为待计算并排队 `billing.pull_provider_taxes`（`retry_service.rb:13-41`）。`PullTaxes` 为税处于待计算的 open 或草稿发票应用税额；服务商不计税的客户返回 `integration_customer` 未找到。它保留发票暂扣时已应用的渐进式计费抵扣与优惠券，只计算税额与合计，再应用贷项通知单与预付额度（草稿与一次性发票除外）；渐进式贷项通知单在发票暂扣时开具。草稿仍为草稿并发出 `invoice.ready_to_finalize`；被门控的发票请求草稿税额且只创建付款，合计为零时满足其付款规则。服务商错误将其代码与消息存入 `tax_error` 明细（`tax_error`、`tax_error_message`，`pull_taxes_and_apply_service.rb:164-178`）。消息表明达到 API 限额或资源争用的服务商错误（`kernel.ErrTaxContention`，`app/services/integrations/aggregator/taxes/base_service.rb:68,113`）保持发票不变并使作业失败，作业以多项式递增的间隔重试 6 次；作废税额时亦然。`RetryAllInvoices`（`POST /v1/invoices:retry_all`）将全部失败发票放入一个 `billing.retry_invoices` 作业并返回数量，作业逐一重试，遇到第一个失败即停止（`retry_batch_service.rb`、`Invoices::RetryAllJob`）。每 15 分钟，`billing.retry_tax_failed_invoices` 重试 `tax_error_message` 包含 `API limit`（不区分大小写）的失败发票，忽略重试失败（`app/jobs/clock/retry_failed_invoices_job.rb`）。发票的服务商税额延迟计算：在事务之外调用税务服务商（独立预付费用、预览与当前用量如 lago 一样直接调用），在第二个事务中应用其结果；若期间费用发生变化则重新询问；lago 会应用先前的结果（`app/services/invoices/provider_taxes/pull_taxes_and_apply_service.rb:40`） |
| 定稿因 VIES 检查而挂起的发票 | `app/services/invoices/finalize_pending_vies_invoice_service.rb`、`app/jobs/invoices/finalize_pending_vies_invoice_job.rb` | libs/billing、apps/server | 已完成 | 检查成功后，`customers.vies_check` 通过 `customers.VIESInvoiceFinalizer` 为该客户每张 `tax_status` 为 pending 的 open 发票排入 `billing.finalize_pending_vies_invoice`（`app/jobs/customers/vies_check_job.rb:46-57`）；开票服务实现该接口，服务端用 `customers.WithVIESInvoiceFinalizer` 接入。任务在发票锁下运行：发票不是税待定的 open 发票、客户由税务服务商计税、或客户的检查再次进行中时不做处理；否则再次应用客户的自定义栏目（`kernel.ApplyCustomSections`，不带 ID 与资源，`finalize_pending_vies_invoice_service.rb:25`），保留暂扣时已应用的渐进式计费抵扣与优惠券，只计算税额与合计（`finalize_pending_vies_invoice_service.rb:24`；创建时已计税的费用，即一次性明细的显式税，保留其税），再应用贷项通知单与预付额度，重定日期并定稿，发出 `invoice.created` 或 `invoice.one_off_created`、生成文档并发起付款。金额为零的付款门控发票满足其付款规则；门控发票保持 open，只发起付款 |
| 渐进式计费扣减与超额贷项通知单 | `app/services/invoices/progressive_billing_service.rb` | libs/billing | 逻辑已完成 | |
| 发票预览：提案、预测、终止、更换套餐 | `app/services/invoices/preview_service.rb`、`app/services/invoices/preview_context_service.rb`、`app/services/invoices/preview/`、`app/controllers/api/v1/invoices_controller.rb` | libs/billing、apps/server | 已完成 | `InvoiceService.PreviewInvoice`。模拟的变更（临时或被覆盖属性的客户及其集成客户、终止、更换套餐、按属性构建的优惠券以及已应用的优惠券）写入随后回滚的事务，而不是保存在内存中。不带 `customer_id` 的请求预览一个尚不存在的客户；`coupon_ids`（指向不存在优惠券的引用会被忽略）是在按属性给出的 `coupons` 之外新增的字段。接受的差异：校验错误使用请求字段名（`subscription_ids` 和 `plan_id`，而不是 `subscriptions` 和 `base`；`integration_not_found`，而不是 `anrok_integration_not_found`），集成通过 ID 或代码引用，不需要 `integration_type`；模拟的记录带有生成的 ID，而 lago 的新记录没有 ID（生成的外部 ID 不会显示）；没有高级版检查（`app/services/invoices/preview_service.rb:19`、`:22`） |
| 争议败诉 | `app/services/invoices/lose_dispute_service.rb` | libs/billing、apps/server | 已完成 | `InvoiceService.LoseInvoiceDispute`：仅限已定稿或已作废；已有的败诉时间保持不变；发出 `invoice.payment_dispute_lost` 并作废提供者的税。不排入 Hubspot 发票更新；支付提供方报告的败诉（`app/services/payments/lose_dispute_service.rb`）随提供方适配器一同实现 |
| 删除草稿发票 | `app/services/invoices/delete_service.rb` | libs/billing、apps/server | 已完成 | `InvoiceService.DeleteInvoice`：仅限草稿（`not_deletable`），且未同步到外部系统（`invoice_synced_to_external_system`，存在集成资源）；其草稿贷项通知单进入终态且不可见的 `deleted` 状态（`CreditNotes::DeleteService`，`delete_service.rb:41-49`），存在其他贷项通知单时拒绝删除（`credit_note_not_deletable`）；发票进入终态且不可见的 `deleted` 状态，保留其费用与订阅周期（`Invoice#mark_as_deleted`），因此该周期不再计费；发出 `invoice.deleted` |
| 更新付款状态与元数据（最多 5 个，键 20 字符，值 140 字符） | `app/services/invoices/update_service.rb` | libs/billing、apps/server | 已完成 | `InvoiceService.UpdateInvoice` 基于 `payments.Service.UpdateInvoice`：费用采用发票的付款状态；付款成功时清除逾期标记、重置催收活动请求的催收计数并使结账链接过期；credit 发票结算其钱包购买（`update_service.rb:113-117`）。不排入 Hubspot 更新与订阅门控处理 |
| 把发票标记为逾期 | `app/services/invoices/payments/mark_overdue_service.rb` | libs/billing | 已完成 | 已定稿、未支付、超过到期日且没有败诉争议的发票 |
| 发票条目上的条目元数据（最多 50 个键） | `app/models/`（`item_metadata`） | libs/billing | 计划中 | |
| 发票结清（付款、贷项通知单） | `app/models/invoice_settlement.rb` | libs/billing | 计划中 | |
| 发票列表过滤与搜索 | `app/queries/invoices_query.rb` | libs/store、apps/server | 已完成 | 包括采购订单号，不区分大小写；两种存储均已实现查询，移植的规格用例在 porttest 中；`ListInvoices` 以 AIP 过滤表达式接收这些条件，支持搜索，并以客户为父资源 |
| 发票自定义段落（手动、系统生成）及其解析规则 | `app/models/invoice_custom_section.rb`、`app/services/invoice_custom_sections/` | libs/billing、apps/server | 进行中 | 公开 API，而不只供管理界面使用：`InvoiceCustomSectionService` 基于 libs/billing/customsections，创建、获取（ID 或代码）、更新、删除手动段落并按名称列出，与 v1.55.0 的 GraphQL 变更和解析器一致（`app/graphql/resolvers/invoice_custom_sections_resolver.rb`）。代码仍可修改，与 `update_input.rb` 一致；删除会软删除段落（`deleted_at`；其代码可再次使用），并将其从计费主体、客户和订阅的选择中移除（`destroy_service.rb`、`deselect_all_service.rb`）；段落带有不打印的 `description`；列表不接受过滤和搜索，与 v1.55.0 一致。解析规则尚未实现 |
| 下载 PDF 与 XML | `app/services/invoices/generate_pdf_service.rb` | libs/documents | 逻辑已完成 | `DownloadInvoice` 返回已定稿发票渲染好的 PDF，否则排入渲染任务；XML 渲染尚未排入 |
| 重新发送发票邮件 | `app/services/emails/resend_service.rb` | libs/mail | 逻辑已完成 | |
| 发票的付款 URL | `app/services/invoices/payments/generate_payment_url_service.rb` | libs/billing | 已完成 | `invoicing.Service.PaymentURL` 校验已关联提供者、不是 GoCardless、发票可付款，然后返回发票付款意向的结账链接（`payments.Service`，由 `apps/server` 绑定）；`GenerateInvoicePaymentUrl` 返回该链接。`RetryInvoicePayment`（`app/services/invoices/payments/retry_service.rb`）已完成，`payment_method_type` 与 `payment_method_id` 覆盖（`invoices_controller.rb:327-334`）随付款任务传递；`RetryInvoice`（`app/services/invoices/retry_service.rb`）重新打开失败的发票，将其税设为待计算并排队拉取（见重试失败的提供者税额计算） |
| 在发票上存储 Salesforce ID | `app/controllers/api/v1/invoices_controller.rb`（`sync_salesforce_id`） | libs/billing、apps/server | 已完成 | `SetInvoiceSalesforceId` 将 Salesforce ID 记为发票在该 Salesforce 集成（ID 或代码）下的已同步资源；已记录的发票保留其记录（`invoices/sync_salesforce_id_service.rb:16-22`） |
| 发票 webhook `invoice.created`、`one_off_created`、`paid_credit_added`、`generated`、`drafted`、`ready_to_finalize`、`voided`、`deleted`、`resynced` | `app/services/webhooks/invoices/` | libs/webhooks | 计划中 | |
| 发票付款 webhook `invoice.payment_status_updated`、`payment_overdue`、`payment_failure`、`payment_dispute_lost` | `app/services/webhooks/invoices/`、`app/services/webhooks/payment_providers/` | libs/webhooks | 计划中 | |

### 1.24 费用

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 费用类型：计费项、附加项、订阅、额度、承诺消费、固定计费项、产品 | `app/models/fee.rb` | libs/core | 计划中 | |
| 预付费用的付款状态（pending、succeeded、failed、refunded） | `app/models/fee.rb` | libs/core | 已完成 | 通过 `FeeService.UpdateFee` 设置未关联发票的费用；记录新状态的时刻并清除其他时刻（`app/services/fees/update_service.rb:45-62`） |
| 订阅费用 | `app/services/fees/subscription_service.rb` | libs/billing | 逻辑已完成 | |
| 计费项费用（包括过滤器与分组） | `app/services/fees/charge_service.rb` | libs/billing | 逻辑已完成 | |
| 固定计费项费用 | `app/services/fees/fixed_charge_service.rb` | libs/billing | 逻辑已完成 | |
| 附加项与一次性费用 | `app/services/fees/one_off_service.rb` | libs/billing | 逻辑已完成 | |
| 钱包充值的额度费用 | `app/services/fees/paid_credit_service.rb` | libs/billing | 逻辑已完成 | 单价舍去钱包汇率中不足一分的部分，与 lago 的 bigint `unit_amount_cents` 一致（`app/models/wallet_transaction.rb:99`）；精确单价即该单价（`paid_credit_service.rb:40`）。作废 credit 发票会使购买失败（偏差，见 credit 发票一行） |
| 承诺消费费用 | `app/services/fees/commitments/` | libs/billing | 逻辑已完成 | |
| 草稿发票上的调整费用（单位数、单价、显示名称） | `app/models/adjusted_fee.rb`、`app/services/adjusted_fees/` | libs/billing、apps/server | 已完成 | 调整只作用于计费项（或固定费用）边界相同的费用，与 lago 的 `matching_charge_boundaries` 一致（`app/models/adjusted_fee.rb:35-39`）。`FeeService.AdjustFee`（`POST /v1/invoices/{invoice_id}/fees:adjust`）和 `FeeService.DeleteFeeAdjustment`（`POST /v1/fees/{fee_id}:delete_adjustment`）对应 lago 的 GraphQL `CreateAdjustedFee` 和 `DestroyAdjustedFee`。调整模式显式给出，lago 则根据是否提供单位数和单价推断（`app/services/adjusted_fees/create_service.rb:35-36`）；没有高级版限制（`create_service.rb:84`）；非草稿发票返回 `not_draft`，lago 返回 `forbidden`。未指定费用时，调整以发票订阅的边界保存且不关联费用，由刷新生成该费用，lago 则先创建一条空费用（`create_service.rb:97-157`）；调整不会重新关联到重新生成的费用，而是按键匹配 |
| 费用的税 | `app/models/fee/applied_tax.rb` | libs/tax | 计划中 | |
| 费用的展示明细 | `app/models/`（`presentation_breakdowns`） | libs/billing | 进行中 | 存储在费用上（lago 使用 `presentation_breakdowns` 表），并在发票上打印（支持 null 值）；计费尚未计算它们 |
| 费用 API（列出、获取、更新付款状态、删除未关联的费用） | `app/controllers/api/v1/fees_controller.rb`、`app/queries/fees_query.rb` | apps/server | 已完成 | `FeeService`（`/v1/fees`）。列表过滤支持 `fee_type`、`payment_status`、`currency`、`customer_id` 和 `subscription_id`（ID 或 `external_id:<id>`）、`billable_metric_code`、`event_transaction_id`，以及 `created_at`、`succeeded_at`、`failed_at`、`refunded_at` 的范围；列表先按创建时间升序、再按 ID 排序（`app/queries/fees_query.rb:33`）。删除费用会软删除（`deleted_at`）并返回该费用（`app/services/fees/destroy_service.rb:17`）；所有费用读取都跳过已软删除的费用，与默认作用域一致（`app/models/fee.rb:9`）。已开票的费用返回 `invoiced_fee` 前置条件失败（400），lago 返回 405 |
| 费用 webhook `fee.created`（预付） | `app/services/webhooks/fees/pay_in_advance_created_service.rb` | libs/webhooks | 计划中 | |

### 1.25 税

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 税的增删改查（名称、代码、税率、描述） | `app/models/tax.rb`、`app/services/taxes/` | libs/billing、apps/server | 已完成 | 由基于 libs/billing/taxes 的 `TaxService` 提供；修改或删除税会标记受影响的草稿发票待刷新。列表按名称排序，可按 `auto_generated` 和 `applied_to_organization` 过滤并可搜索（`taxes_query.rb`）。删除会软删除税并移除其关联，与 v1.55.0 一致。税带有 `applied_to_organization` 与 `applied_to_billing_entities_codes`（`serializers/v1/tax_serializer.rb`）；删除时返回移除关联之前加载的值（`taxes/destroy_service.rb:25`） |
| 计费主体、客户、套餐、计费项、固定计费项、附加项、承诺消费上的税 | `app/models/` 中的关联表 | libs/tax | 计划中 | |
| 税的解析顺序（显式、附加项、计费项、固定计费项、承诺消费、套餐、客户、计费主体） | `app/services/fees/apply_taxes_service.rb` | libs/tax | 逻辑已完成 | |
| 费率卡的税覆盖套餐、客户和计费主体的税 | `app/models/rate_card.rb`（`rate_cards_taxes`） | libs/tax | 计划中 | 在统一目录中，费率卡的税就是其计费项的税 |
| 发票上应用的税，带计税基础与分摊 | `app/services/invoices/apply_taxes_service.rb` | libs/tax | 逻辑已完成 | 服务商的计税基础存储在发票已应用税上并打印在发票上（`app/models/invoice/applied_tax.rb:31-41`） |
| 贷项通知单上应用的税 | `app/services/credit_notes/apply_taxes_service.rb` | libs/tax | 逻辑已完成 | |
| 提供者计算的税（Anrok、Avalara），带特殊代码与基础税率 | `app/services/invoices/apply_provider_taxes_service.rb` | libs/tax | 逻辑已完成 | |
| 各国欧盟增值税税率及例外 | `app/services/taxes/`（ibericode 税率） | libs/tax | 逻辑已完成 | |
| 为组织自动生成欧盟税 | `app/services/taxes/auto_generate_service.rb` | libs/tax | 逻辑已完成 | |
| 按客户的欧盟税判定（境内、欧盟内 B2B 反向征税、B2C、欧盟外） | `app/services/customers/eu_auto_taxes_service.rb` | libs/tax | 逻辑已完成 | |
| 带重试与退避的 VIES 增值税号检查 | `app/services/customers/vies_check_service.rb`、`app/jobs/customers/vies_check_job.rb` | libs/tax、libs/billing | 已完成 | `tax.VIESClient` 按 valvat 2.0.1 的方式调用 VIES SOAP 服务（格式错误的税号与 GB 税号不检查，重定向视为错误）。需要检查的客户写入会保存一行 `pending_vies_checks` 并在 default 队列上排队 `customers.vies_check`；特殊地区或无需 VIES 的决定会删除该行。任务应用所决定的 EU 税（税不存在时不应用），标记草稿待刷新并删除该行；VIES 失败时记录尝试，并在 5、5、10、20、40 分钟后重试，之后每小时一次。未知故障或无法连接 VIES 时任务失败。重试按尝试次数设唯一键，因为队列在任务运行期间保持其唯一性 |
| 在待定的 VIES 检查完成前挂起发票 | `app/models/pending_vies_check.rb`、`app/services/invoices/ensure_completed_vies_check_service.rb` | libs/billing | 已完成 | 待定检查已存储（`pending_vies_checks`，每个客户一行，以组织与客户为键）。在 lago 调用 `Invoices::ComputeTaxesAndTotalsService` 之处（订阅发票及其刷新与定稿、预付费用与预付固定费用、一次性、渐进计费与重新生成的发票），若客户不由税务服务商计税、其开票主体管理 EU 增值税且有待定检查，发票的费用不计税，以 `tax_status` pending 等待：定稿时为 open（无论是否门控），否则为草稿。不拉取其税，也不发布；新草稿仍发出 `invoice.drafted`。一次性明细的显式税立即应用到其费用，与 lago 在创建费用时应用一致。预览、预付费用账单与独立费用不挂起，与 lago 一致 |
| VIES webhook `customer.vies_check` | `app/services/webhooks/customers/vies_check_service.rb` | libs/billing、libs/webhooks | 已完成 | 负载为带 `vies_check` 的客户：VIES 查询结果，或 `valid: false`、`valid_format`，失败时还有 `error` |
| 税务提供者错误 webhook `customer.tax_provider_error`、`fee.tax_provider_error` | `app/services/webhooks/integrations/taxes/` | libs/webhooks | 计划中 | |

### 1.26 优惠券

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 固定金额或百分比优惠券的增删改查 | `app/models/coupon.rb`、`app/services/coupons/`、`app/controllers/api/v1/coupons_controller.rb`、`app/queries/coupons_query.rb` | libs/billing、apps/server | 已完成 | `CouponService`。删除即丢弃优惠券（`deleted_at`，`coupon.rb:67`）：保留其状态，读取与应用都忽略它，其代码可再次使用。列表按 `status` 过滤，按名称和代码搜索，生效的在前，再按过期设置排序（`coupon.rb:68-76`）。代码冲突为 `code` 上的 `value_already_exist`。优惠券带有 `limited_plans`、`limited_billable_metrics` 以及其父套餐与存活指标的代码（`serializers/v1/coupon_serializer.rb`） |
| 频率：一次、循环（带持续期）、永久 | `app/models/coupon.rb` | libs/billing | 已完成 | |
| 可重复使用的优惠券 | `app/models/coupon.rb` | libs/billing | 已完成 | 默认可复用（`coupons/create_service.rb:22`） |
| 限定于套餐或计费指标 | `app/models/coupon_target.rb` | libs/billing | 已完成 | 按 ID 或 `code:` 指定；缺失的对象为 `plans_not_found` 或 `billable_metrics_not_found`。更新只在适用对象出现在更新掩码中（或无掩码时已设置）时修改它，而 lago 每次更新都替换两个列表（`coupons/update_service.rb:68-69、101-120`） |
| 按日期过期与终止任务 | `app/jobs/clock/terminate_coupons_job.rb` | libs/billing | 逻辑已完成 | |
| 已应用的优惠券，可覆盖金额、百分比与频率 | `app/services/applied_coupons/create_service.rb`、`app/controllers/api/v1/applied_coupons_controller.rb`、`app/queries/applied_coupons_query.rb` | libs/billing、apps/server | 已完成 | `ApplyCoupon` 与 `ListAppliedCoupons`，也可经 `/v1/customers/{customer_id}/applied_coupons` 访问。列表排除已删除的客户，保留已删除的优惠券，按 `status` 和 `coupon_id`（ID 或 `code:`）过滤，最新的在前。两个路由上找不到的 `customer_id` 都返回未找到。已应用的优惠券带有其优惠券的代码、名称、描述、状态与删除时间、客户的外部 ID 以及优惠券的过期时间；列表包含其抵扣记录及发票的支付状态（`serializers/v1/applied_coupon_serializer.rb`，`includes: %i[credits]`），不含抵扣的 `item`，因为它就是该优惠券本身 |
| 发票上的应用顺序与金额计算 | `app/services/credits/applied_coupons_service.rb`、`app/services/credits/applied_coupon_service.rb` | libs/billing | 逻辑已完成 | |
| 作废时退回优惠券 | `app/services/applied_coupons/recredit_service.rb` | libs/billing | 逻辑已完成 | |
| 终止已应用的优惠券 | `app/services/applied_coupons/terminate_service.rb`、`app/controllers/api/v1/customers/applied_coupons_controller.rb` | libs/billing、apps/server | 已完成 | `TerminateAppliedCoupon` 按已应用优惠券 ID 终止，路径中不含客户 |

### 1.27 额度与应用顺序

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 把优惠券、贷项通知单与渐进式计费关联到发票的额度表 | `app/models/credit.rb` | libs/billing | 逻辑已完成 | |
| 发票上的贷项通知单额度 | `app/services/credits/credit_note_service.rb` | libs/billing | 逻辑已完成 | |
| 按钱包优先级与允许的费用类型分配的钱包预付额度 | `app/services/credits/applied_prepaid_credits_service.rb` | libs/billing | 逻辑已完成 | |
| 渐进式计费额度 | `app/services/credits/progressive_billing_service.rb` | libs/billing | 逻辑已完成 | |
| 应用顺序：渐进式计费、优惠券、税、贷项通知单、预付额度 | `app/services/invoices/subscription_service.rb`、`app/services/invoices/calculate_fees_service.rb` | libs/billing | 逻辑已完成 | |

### 1.28 贷项通知单

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 创建带额度、退款与抵销金额的贷项通知单 | `app/services/credit_notes/create_service.rb` | libs/billing | 逻辑已完成 | |
| 额度状态（available、consumed、voided）与退款状态（pending、succeeded、failed） | `app/models/credit_note.rb` | libs/core | 计划中 | |
| 原因：重复收费、产品不满意、订单变更、订单取消、欺诈收费、其他 | `app/models/credit_note.rb` | libs/core | 计划中 | |
| 状态 draft、finalized | `app/models/credit_note.rb` | libs/core | 计划中 | |
| 编号 `INVOICE-CNnnn` | `app/models/credit_note.rb` | libs/billing | 逻辑已完成 | |
| 按费用的贷项通知单条目 | `app/models/credit_note_item.rb` | libs/billing | 逻辑已完成 | |
| 带分摊与舍入调整的税 | `app/services/credit_notes/apply_taxes_service.rb` | libs/tax | 逻辑已完成 | |
| 校验可贷记与可退款金额 | `app/services/credit_notes/validate_service.rb` | libs/billing | 逻辑已完成 | 执行所有检查，包括 Lago 从未调用的检查（§4） |
| 结清发票的抵销 | `app/services/credit_notes/create_service.rb`、`app/models/invoice_settlement.rb` | libs/billing | 逻辑已完成 | |
| 通过支付提供者退款 | `app/services/credit_notes/refunds/` | libs/billing | 逻辑已完成 | 见 §1.35 |
| 由订阅终止产生的贷项通知单 | `app/services/credit_notes/create_from_termination.rb` | libs/billing | 逻辑已完成 | |
| 由渐进式计费超额产生的贷项通知单 | `app/services/credit_notes/create_from_progressive_billing_invoice.rb` | libs/billing | 逻辑已完成 | |
| 估算贷项通知单 | `app/services/credit_notes/estimate_service.rb` | libs/billing | 逻辑已完成 | |
| 作废贷项通知单（剩余额度） | `app/services/credit_notes/void_service.rb` | libs/billing | 逻辑已完成 | |
| 更新退款状态 | `app/services/credit_notes/update_service.rb` | libs/billing | 逻辑已完成 | |
| 随发票刷新草稿贷项通知单 | `app/services/credit_notes/refresh_draft_service.rb` | libs/billing | 逻辑已完成 | |
| 发票作废时退回额度 | `app/services/credit_notes/recredit_service.rb` | libs/billing | 逻辑已完成 | |
| 贷项通知单元数据 | `app/models/metadata/` | libs/billing | 逻辑已完成 | |
| 贷项通知单列表过滤，包括发票的采购订单号 | `app/queries/credit_notes_query.rb` | libs/store | 已完成 | 两种存储均已实现查询，移植的规格用例在 porttest 中；`ListCreditNotes` 可按贷项、退款状态和原因、货币、计费主体、自开票、发票号和采购订单号、类型、客户（ID 或外部 ID）过滤，按开票日期和金额（最小单位）范围过滤，并支持搜索 |
| 贷项通知单 API | `app/controllers/api/v1/credit_notes_controller.rb` | apps/server | 进行中 | `CreditNoteService` 的 Create、Estimate（附带 `types/credit_notes/estimate.rb` 的最大可贷记、可退款和可抵销金额）、Get、List、Update（退款状态、元数据）、Void 和 Download，基于 libs/billing/creditnotes；只提供已定稿的贷项通知单。Download 会排入文档任务，在 PDF 生成前返回空响应，之后返回其路径而不带签名 URL。ResendCreditNote 与 RetryCreditNoteSync 尚未提供（邮件与集成尚未接入） |
| 贷项通知单 webhook `credit_note.created`、`credit_note.generated`、`credit_note.provider_refund_failure` | `app/services/webhooks/credit_notes/` | libs/webhooks | 计划中 | 只用一个事件名（§4） |

### 1.29 钱包

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 钱包的增删改查（名称、代码、货币、兑换率、过期时间） | `app/models/wallet.rb`、`app/services/wallets/` | libs/billing | 逻辑已完成 | 货币属于钱包自身，默认取客户的货币 |
| 钱包、钱包交易和周期充值规则上的采购订单号，复制到额度发票 | `app/services/wallets/create_service.rb`、`app/services/wallet_transactions/create_from_params_service.rb`、`app/models/recurring_transaction_rule.rb` | libs/billing | 逻辑已完成 | 空白即无；规则执行时取规则的，否则取钱包的 |
| 每个客户多个钱包（默认最多 6 个），优先级 1–50 | `app/models/wallet.rb` | libs/billing | 逻辑已完成 | |
| 充值的最小与最大值 | `app/models/wallet.rb` | libs/billing | 逻辑已完成 | |
| 每个钱包允许的费用类型 | `app/models/wallet.rb`（`allowed_fee_types`） | libs/billing | 逻辑已完成 | |
| 钱包目标（钱包为哪些计费指标付款） | `app/models/wallet_target.rb` | libs/billing | 逻辑已完成 | |
| 可追溯钱包（消耗与资金来源） | `app/models/wallet_transaction_consumption.rb` | libs/billing | 逻辑已完成 | |
| 钱包交易：付费、赠送、作废；状态 pending、settled、failed；来源 manual、interval、threshold | `app/models/wallet_transaction.rb`、`app/services/wallet_transactions/` | libs/billing | 逻辑已完成 | |
| 定向作废一笔入账赠送（`voided_transaction_id`） | `app/services/wallet_transactions/void_service.rb`、`app/services/wallet_transactions/validate_service.rb` | libs/billing | 逻辑已完成 | 仅限可追踪钱包；全部剩余额度或有上限的金额 |
| 发放额度前要求发票付款成功 | `app/models/wallet_transaction.rb` | libs/billing | 逻辑已完成 | |
| 循环充值规则：按间隔或阈值，固定或目标，赠送部分按目标充值 | `app/models/recurring_transaction_rule.rb`、`app/services/wallets/recurring_transaction_rules/` | libs/billing | 逻辑已完成 | |
| 按间隔充值 | `app/jobs/clock/create_interval_wallet_transactions_job.rb` | libs/billing | 逻辑已完成 | 在钱包锁内重新读取规则；lago 使用定时任务列出的规则计算充值（`app/services/wallets/create_interval_wallet_transactions_service.rb:8`） |
| 带退避的按阈值充值 | `app/services/wallets/threshold_top_up_service.rb` | libs/billing | 逻辑已完成 | |
| 终止已过期的规则 | `app/jobs/clock/terminate_recurring_transaction_rules_job.rb` | libs/billing | 逻辑已完成 | |
| 由当前用量得出的实时余额，跨钱包级联 | `app/services/wallets/balance/refresh_ongoing_usage_service.rb`、`app/services/wallets/balance/allocate_ongoing_usage_by_wallets_service.rb` | libs/billing | 逻辑已完成 | 按应用顺序，每个钱包最多取到其余额，溢出由最后一个承担；有阈值规则的钱包承担全部 |
| 实时余额刷新 | `app/jobs/clock/refresh_wallets_ongoing_balance_job.rb`、`app/services/wallets/realtime_refresh_service.rb` | libs/billing | 逻辑已完成 | 也由用量刷新信号触发，信号在用量桶存储之后发出；5 分钟的全量扫描仍是权威来源；用量在不持有锁的情况下计算，与 lago 相同（`app/jobs/customers/refresh_wallet_job.rb:47`），但余额在客户的 `prepaid_credits` 锁内写入，若期间有发票消耗了额度则重新计算用量；lago 写入时不加锁 |
| 钱包过期 | `app/jobs/clock/terminate_wallets_job.rb` | libs/billing | 逻辑已完成 | |
| 钱包交易的付款 URL | `app/services/wallet_transactions/payments/generate_payment_url_service.rb` | libs/billing | 进行中 | 校验（带发票的待结算购买交易）已完成（`wallets.Service.PaymentURLInvoice`）；在支付提供方的发票付款 URL 实现之前（`app/services/invoices/payments/generate_payment_url_service.rb`），`GenerateWalletTransactionPaymentUrl` 校验通过后返回 Unimplemented |
| 钱包与交易元数据（最多 15 个键） | `app/models/metadata/` | libs/billing | 逻辑已完成 | |
| 钱包 webhook `wallet.created`、`updated`、`terminated`、`depleted_ongoing_balance` | `app/services/webhooks/wallets/` | libs/webhooks | 计划中 | |
| 钱包交易 webhook `wallet_transaction.created`、`updated`、`payment_failure` | `app/services/webhooks/wallet_transactions/` | libs/webhooks | 计划中 | |
| 钱包与钱包交易 API | `app/controllers/api/v1/wallets_controller.rb`、`app/controllers/api/v1/customers/wallets_controller.rb`、`app/controllers/api/v1/wallet_transactions_controller.rb`、`app/controllers/concerns/wallet_actions.rb` | apps/server | 进行中 | `WalletService` 的 Create、Get、List（`currency` 与 `billing_entity_id` 过滤）、Update、Terminate、CreateWalletTransaction、GetWalletTransaction、ListWalletTransactions（`transaction_type`、`transaction_status`、`status` 与 `metadata.<key>` 过滤）、ListWalletTransactionConsumptions（入账交易的消耗、出账交易的资金来源）和 ReplaceRecurringTransactionRules。列表按从旧到新排列；Lago 按从新到旧排列。钱包附带其当前生效的规则。尚未提供：钱包元数据（未建模）、创建时的 `transaction_name`、`transaction_metadata`、`transaction_priority` 与 `purchase_order_number`、交易的 `voided_transaction_id`、`priority` 与 `purchase_order_number`、发票自定义区块与连接（无请求字段） |

### 1.30 承诺消费

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 每个套餐的最低承诺消费，可按订阅覆盖 | `app/models/commitment.rb`、`app/services/commitments/` | libs/billing | 逻辑已完成 | |
| 用量低于承诺消费时的补差费用 | `app/services/commitments/minimum/calculate_true_up_fee_service.rb` | libs/billing | 逻辑已完成 | |
| 预付与后付的承诺消费周期 | `app/services/commitments/` | libs/billing | 逻辑已完成 | |
| 不完整周期的按比例系数 | `app/services/commitments/` | libs/billing | 逻辑已完成 | |
| 发票预览中的承诺消费 | `app/services/invoices/preview_service.rb` | libs/billing | 逻辑已完成 | |
| 承诺消费的税 | `app/services/commitments/apply_taxes_service.rb` | libs/tax | 计划中 | |

### 1.31 渐进式计费与用量阈值

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 套餐或订阅上的用量阈值（二者互斥） | `app/models/usage_threshold.rb`、`app/services/usage_thresholds/` | libs/billing、apps/server | 已完成 | 创建与更新时的 `Subscription.usage_thresholds` 替换订阅自身的阈值，丢弃子套餐的阈值，并标记生命周期用量待重算（`app/services/subscriptions/update_usage_thresholds_service.rb`）；与 `plan_overrides.usage_thresholds` 同时给出时以 `incompatible_params` 拒绝。读取时为适用的阈值（lago `applicable_usage_thresholds`，`app/models/subscription.rb:305-309`） |
| 最多一个循环阈值 | `app/services/usage_thresholds/update_service.rb` | libs/billing | 逻辑已完成 | |
| 已应用的用量阈值 | `app/models/applied_usage_threshold.rb` | libs/billing | 逻辑已完成 | |
| 按订阅覆盖阈值 | `app/services/usage_thresholds/override_service.rb` | libs/billing | 逻辑已完成 | |
| 用量刷新时检查阈值是否越过 | `app/services/lifetime_usages/check_thresholds_service.rb` | libs/billing | 逻辑已完成 | |
| 越过阈值时开具渐进式计费发票 | `app/services/invoices/progressive_billing_service.rb` | libs/billing | 逻辑已完成 | |

### 1.32 用量监控告警

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 告警类型：当前用量金额 | `app/models/usage_monitoring/alert.rb` | libs/billing | 已完成 | |
| 告警类型：计费指标当前用量金额与单位数 | `app/models/usage_monitoring/` | libs/billing | 已完成 | |
| 告警类型：生命周期用量金额与计费指标生命周期用量单位数 | `app/models/usage_monitoring/` | libs/billing | 已完成 | 无高级版限制 |
| 告警类型：钱包余额金额、额度余额、实时余额金额、实时额度余额 | `app/models/usage_monitoring/` | libs/billing | 已完成 | 每次钱包余额变化后评估；无高级版限制 |
| 每个告警最多 20 个阈值，可有循环阈值 | `app/models/usage_monitoring/alert.rb` | libs/billing | 已完成 | |
| 已触发的告警 | `app/models/usage_monitoring/triggered_alert.rb` | libs/billing | 已完成 | |
| 根据订阅活动评估告警 | `app/services/usage_monitoring/process_subscription_activity_service.rb` | libs/billing | 已完成 | 持有告警的行锁，编辑告警时也取同一把锁。订阅的活动是由用量信号入队的每分钟一个的延迟任务（按订阅与分钟唯一），代替每分钟扫描的 `subscription_activities` 表（`app/services/usage_monitoring/track_subscription_activity_service.rb:23-32`、`app/jobs/clock/process_all_subscription_activities_job.rb`）；由于没有付费版门槛，每个订阅都会被跟踪 |
| 订阅与钱包上的告警 API，支持批量创建 | `app/controllers/api/v1/subscriptions/alerts_controller.rb`、`app/controllers/api/v1/customers/wallets/alerts_controller.rb` | apps/server | 进行中 | `AlertService` 的 Create、BatchCreate（全部成功或全部失败，错误位于 `alerts[i]` 下）、Get、List、Update、Delete 和 DeleteAlerts（所有者的全部告警，按 ID 顺序加锁，再删除其阈值并软删除告警；不发送 webhook），基于 libs/billing/alerts；告警以 ID 或 `code:` 引用，订阅以 ID 或 `external_id:` 引用，钱包以 ID 引用。Delete 软删除告警（`deleted_at`）并删除其阈值，已触发的告警保留。递减（钱包）告警从钱包的当前值开始。列表按从旧到新排列；Lago 按从新到旧排列 |
| webhook `alert.triggered`（高优先级） | `app/services/webhooks/usage_monitoring/alert_triggered_service.rb` | libs/webhooks | 逻辑已完成 | 每次触发时发出；尚无投递优先级 |

### 1.33 支付

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 每个组织带代码与名称的支付提供者 | `app/models/payment_providers/base_provider.rb` | libs/core、libs/billing、apps/server | 已完成 | 每个提供者可有多个连接，由 `IntegrationService` 管理（§1.37）；代码在组织的未删除支付提供者中唯一（`index_payment_providers_on_code_and_organization_id`） |
| 把提供者状态映射为 pending、processing、succeeded、failed | `app/models/payment_providers/*.rb`（`determine_payment_status`） | providers/* | 进行中 | Stripe 已完成（`stripe.StatusOf`，对应 lago `StripeProvider` 的状态列表）；`payment.PaymentResult` 还带有提供者状态与 `requires_action` 的下一步 URL；缺少 `next_action.type` 时按 `redirect_to_url` 处理 |
| 成功后的跳转 URL | `app/models/payment_providers/base_provider.rb` | libs/core | 逻辑已完成 | 各适配器的连接设置 `success_redirect_url`，未设置时使用其默认页面（Stripe 为 `stripe.DefaultSuccessRedirectURL`）；结账与设置链接都跳转到该地址 |
| 创建提供者客户 | `app/services/payment_providers/create_customer_factory.rb` | providers/* | 已完成 | `payments.Service.SyncProviderCustomer`（`payments.sync_provider_customer`，由客户服务通过 `kernel.EnqueueConnectionSync` 入队）：在提供者处创建客户，发出 `customer.payment_provider_created`，然后生成结账 URL（`customer.checkout_url_generated`）并记录提供者的默认支付方式；带提供者客户 ID 的更新会推送客户信息（`PaymentProviderCustomers::UpdateService`）。失败时发出 `customer.payment_provider_error`。供应商没有适配器时，任务以警告结束 |
| 删除提供者（解除客户关联） | `app/services/payment_providers/destroy_service.rb` | libs/billing | 已完成 | `DeleteIntegration` 在一个事务中软删除支付提供者及其未删除的提供者客户（`destroy_service.rb:19-22`）；其代码可以复用 |
| 每个客户多个提供者与集成连接，每个类别一个默认连接 | `app/services/payment_provider_customers/`、`app/services/integration_customers/` | libs/billing | 进行中 | 集成连接：每个类别可有多个，其中一个为默认。支付连接：与 v1.55.0 一样每个客户一个（`payment_provider_customers/create_or_update_batch_service.rb:23-30`） |
| 按计费对象选择连接（指定、跳过、继承） | `app/services/billing_object_connections/` | libs/billing | 计划中 | |
| 支付方式（来自提供者、默认、详情、删除） | `app/services/payment_methods/` | libs/billing | 进行中 | 提供者侧已完成：providers/stripe 中的 `payment.MethodGateway`（列出、默认、详情、设为默认、解绑、设置 URL）。解绑会在 Stripe 上解绑；lago 只丢弃记录（`app/services/payment_methods/destroy_service.rb`） 删除（软删除，清除默认）与设为默认已在 `payments.Service` 中完成；从提供者同步支付方式尚未完成。 |
| 支付方式优先级（显式覆盖、订阅、钱包交易、规则、钱包、客户默认） | `app/services/payment_methods/determine_service.rb` | libs/billing | 已完成 | `determineMethod`（`determine_service.rb`）：任务的显式覆盖；订阅、预付费用与渐进计费发票取第一个订阅的选择；充值发票取购买交易、规则或钱包的选择；否则取客户默认支付方式 |
| 手动支付方式跳过自动扣款 | `app/services/payment_methods/determine_service.rb` | libs/billing | 已完成 | `manual` 支付方式类型解析为无支付方式，扣款被跳过（`determine_service.rb`，`create_service_spec.rb:354`） |
| 付款类型 provider、manual、x402 | `app/models/payment.rb` | libs/core | 已完成 | 类型 `x402` 会存储并校验；v1.55.0 中没有路径创建它 |
| 带参考号（最多 40 字符）的手动付款 | `app/services/payments/manual_create_service.rb` | libs/billing | 已完成 | `payments.Service.CreateManual`。`created_at` 取 `paid_at`（`manual_create_service.rb:34`）。已接受的偏差：收据在付款的事务中开具，而非由 `PaymentReceipts::CreateJob` 开具。 |
| 带在途保护与尝试计数的发票自动付款 | `app/services/invoices/payments/create_service.rb` | libs/billing | 已完成 | `payments.Service.CreateInvoicePayment`（`payments.create_invoice_payment`，遇到 `payment.ErrRetryable` 时按 HTTP 类多项式重试，对应 lago 的限流与连接错误）：尝试计数 `invoices.payment_attempts`，复用待处理的提供者付款，在事务之外扣款，在发票锁内应用结果；期间已付清的发票会删除未使用的付款（`AlreadyPaidError`）。失败按 `payment.ErrorKind` 分类（`prepare_failed_result`）；值得在会话内重试的认证以 `payments.ErrRetry` 结束。提供者已不存在的支付方式会被丢弃。每个应付对象只有一笔在途的提供者付款 |
| 零金额发票标记为已付 | `app/services/invoices/payments/create_service.rb` | libs/billing | 已完成 | |
| 有未关闭的付款链接时延迟自动付款 | `app/services/invoices/payments/create_service.rb`（`CHECKOUT_AUTO_PAYMENT_DELAY`） | libs/billing | 已完成 | 在使用过付款意向的组织中，首次尝试延迟 `payments.CheckoutPaymentDelay`（10 分钟），付款门控的订阅发票与周期性钱包充值除外。lago 的功能开关 `skip_credit_invoice_auto_payment_delay`（`create_service.rb:134,141-143`）在此是计费实体设置 `SkipCreditInvoicePaymentDelay`，在计费实体 API 中为 `skip_credit_invoice_payment_delay` |
| 付款参考文字 | `app/services/invoices/payments/create_service.rb` | libs/billing | 已完成 | `<计费实体名称> - Invoice <编号>`，门控订阅发票使用发票 ID（`create_service.rb:244-250`）；门控发票付清并编号后，`payments.update_payment_reference` 尽力把编号写回提供者付款（`resolve_service.rb:48-53`，`update_payment_reference_service.rb`） |
| 付款意向（带过期与复用的付款链接） | `app/models/payment_intent.rb`、`app/services/payment_intents/` | libs/billing | 已完成 | `payments.Service.InvoicePaymentURL` 与 `RequestPaymentURL`：复用未过期的活跃意向，否则创建提供者链接并连同过期时间保存（`payment_intents/fetch_service.rb`）；`ExpireCheckout` 在提供者处使单个链接过期，`ExpireConnectionCheckouts` 使一个连接的全部链接过期。发票与钱包的付款 URL RPC 使用它们 |
| 重试发票付款与全部重试 | `app/services/invoices/payments/retry_service.rb`、`retry_batch_service.rb` | libs/billing | 已完成 | `RetryAllInvoicePayments`（`POST /v1/invoices:retry_all_payments`）将付款待处理或失败且可进行付款处理的全部已定稿发票放入一个 `billing.retry_invoice_payments` 作业并返回数量；作业用 `RetryPayment` 逐一重试付款，遇到第一个失败即停止（`app/services/invoices/payments/retry_batch_service.rb`、`Invoices::Payments::RetryAllJob`）。与 `FinalizeAllInvoices` 一样，两个全部重试 RPC 返回排队数量而非发票 |
| 取消需要操作而被放弃的付款 | `app/services/invoices/payments/cancel_abandoned_service.rb` | libs/billing | 已完成 | 每小时的时钟 `payments.cancel_abandoned_payments` 为 `payment.AbandonedRange` 内等待跳转的每笔 Stripe 发票付款入队一个 `payments.cancel_abandoned_payment`，每批 100 个、间隔一分钟（`cancel_abandoned_payments_job.rb:7-8`）；在提供者处读取付款，取消卡片意向，失败的付款会解锁其发票，除非发票已付清 |
| 付款成功的效果：已付合计、发票付款状态、会计同步 | `app/services/invoices/payments/*_service.rb` | libs/billing | 已完成 | 已付合计、发票与费用的付款状态、逾期与催收状态、充值发票结算（`wallets.HandleCreditInvoicePayment`）与付款收据。会计同步经由 `payments.PaymentSync`（`Payment#should_sync_payment?`），由会计集成实现；接入之前不同步任何付款 |
| 付款失败 webhook（发票、钱包交易、付款请求） | `app/services/invoices/payments/deliver_error_webhook_service.rb` | libs/webhooks | 已完成 | `invoice.payment_failure`、`wallet_transaction.payment_failure`（充值发票）与 `payment_request.payment_failure`，带提供者的错误码与消息（`deliver_error_webhook_service.rb`）；保持待处理或需要认证的应付对象不发出（`create_service.rb:222-229`） |
| 付款 webhook `payment.succeeded`、`payment.requires_action` | `app/services/webhooks/payments/` | libs/webhooks | 已完成 | |
| 提供者 webhook `payment_provider.error`、`customer.payment_provider_created`、`customer.payment_provider_error`、`customer.checkout_url_generated` | `app/services/webhooks/payment_providers/` | libs/webhooks | 已完成 | webhook 注册失败时发出 `payment_provider.error`（`payment_provider.register_webhook`）；`customer.payment_provider_created`、`customer.payment_provider_error` 与 `customer.checkout_url_generated` 来自提供者客户同步 |
| 付款 API（列出、获取、创建手动付款）与按客户的付款 | `app/controllers/api/v1/payments_controller.rb` | apps/server | 已完成 | 金额是以主单位表示的十进制字符串，与其他 API 中的 `Money` 一致。过滤条件 `customer_id`、`invoice_id` 与 `currency`，以及 `search`（`payments_query.rb`：支付服务商付款 ID、参考号、付款 ID、无发票过滤时的发票编号、无客户过滤时客户的名称、名、姓、外部 ID 与邮箱）。按创建时间从新到旧、再按 ID 排序（`base_query.rb:111`）。客户路由的 `customer_id` 列出该客户的付款，未知客户返回 NotFound（`customers/payments_controller_spec.rb:13`），Connect 与 REST 行为一致；`/v1/payments` 通过过滤条件 `customer_id` 限定客户，未知客户返回空列表（`payments_query_spec.rb:200`）。 |
| 支付方式 API（列出、删除、设为默认） | `app/controllers/api/v1/customers/payment_methods_controller.rb` | apps/server | 已完成 | 删除返回空响应（lago 返回该支付方式）。 |

### 1.34 支付提供者

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| Stripe 连接（密钥），自动注册 webhook 端点 | `app/services/payment_providers/stripe_service.rb`、`app/jobs/payment_providers/stripe/register_webhook_job.rb` | providers/stripe | 逻辑已完成 | 设置项 `success_redirect_url`、`webhook_endpoint_id`（保存但不返回）、`supports_3ds`、`require_terms_of_service_consent`；webhook 签名密钥保存在连接的 secrets 中，而非设置（lago 保存在设置中，`base_provider.rb:30`）。端点按连接区分（`/webhooks/stripe/{connection_id}`），使用 stripe-go 固定的 API 版本，不支持按调用覆盖版本（`register_webhook_service.rb:8`）；额外订阅两个事件（`payment_intent.requires_action`、`payment_method.attached`）。按连接与密钥模式的客户端令牌桶限流；stripe-go 在进程内重试连接错误与 429 `lock_timeout`，其余错误与 lago 一样由调用方的任务重试（`create_service.rb:50-56`）。未移植的 spec 用例及原因见 providers/stripe/SKIPPED.md |
| Stripe 3DS 支持与服务条款同意 | `app/models/payment_providers/stripe_provider.rb` | providers/stripe | 逻辑已完成 | `supports_3ds` 与 `require_terms_of_service_consent` 通过 `IntegrationService` 设置与更新（`stripe_service.rb:20-48`）。同意设置变化时经 `kernel.EnqueuePaymentConnectionUpdated` 入队 `payments.expire_connection_checkouts`（`ExpirePaymentIntentsJob`），新连接经 `kernel.EnqueuePaymentConnectionCreated` 注册其 webhook（`payments.register_provider_webhook`）；`IntegrationService` 尚未调用它们。代码变化时不重新注册（`RefreshWebhookJob`）：端点 URL 使用连接 ID，代码变化不影响它 |
| 带幂等与邮箱回退的 Stripe 客户 | `app/services/payment_provider_customers/stripe_service.rb` | providers/stripe | 逻辑已完成 | 没有邮箱的客户在幂等冲突后不做邮箱查找回退（lago 会用空邮箱查找，`stripe_service.rb:168`） |
| Stripe 支付方式类型（卡、SEPA 借记、美国银行账户、BACS 借记、Link、Boleto、加密货币、客户余额） | `app/models/payment_provider_customers/stripe_customer.rb` | providers/stripe | 逻辑已完成 | |
| setup 模式的 Stripe 结账 | `app/services/payment_provider_customers/stripe_service.rb` | providers/stripe | 逻辑已完成 | |
| 把 Stripe 银行转账资金说明作为系统发票段落 | `app/services/payment_provider_customers/stripe/sync_funding_instructions_service.rb` | providers/stripe | 逻辑已完成 | 网关调用已完成；发票段落属于 libs/documents |
| Stripe 付款意向（off session、幂等、按货币的银行转账） | `app/services/payment_providers/stripe/payments/create_service.rb` | providers/stripe | 逻辑已完成 | 元数据键为 `tanvo_*`；Stripe 已不存在的支付方式通过 `StaleMethodID` 报告，由调用方丢弃 |
| Stripe 共享付款令牌 | `app/services/payment_providers/stripe/payments/create_service.rb` | providers/stripe | 逻辑已完成 | 偏离：没有 `stripe_shared_payment_token` 功能开关（lago `create_service.rb:149`）；功能开关不属于核心 |
| Stripe 付款链接（payment 模式的结账）及其过期 | `app/services/invoices/payments/stripe_service.rb` | providers/stripe | 逻辑已完成 | 发票结账的元数据同样带有付款对象类型与 ID，与 lago 的付款请求链接一致，使成功 webhook 以同一方式解析两者 |
| Stripe 预授权 | `app/services/payment_providers/stripe/payments/authorize_service.rb` | providers/stripe | 逻辑已完成 | 支付方式的选择（`authorize_service.rb` `find_provider_method_id`）与延迟取消由调用方负责 |
| 付款上的 Stripe 卡信息 | `app/services/payments/set_payment_method_data_service.rb` | providers/stripe | 逻辑已完成 | |
| Stripe webhook：setup intent 成功、payment intent 成功、失败、取消、支付方式解绑、退款更新、客户更新、争议关闭、现金余额到账 | `app/services/payment_providers/stripe/handle_event_service.rb` | providers/stripe | 逻辑已完成 | `ParseWebhook` 返回带 `payment.Refs` 的类型化 `payment.Event`；记录查找留在 apps/server。未知的付款对象类型返回 `payment.ErrUnknownPayable`（拒绝类）且不产生事件，无法映射的退款状态返回 `payment.ErrUnknownStatus`；处理器对二者都记录警告后确认，不更新任何记录，与 lago 的 NameError 和校验失败一样不做更新（`webhooks/base_service.rb` `payment_service_klass`） |
| Adyen 连接（API 密钥、商户账户、live 前缀、HMAC 密钥） | `app/models/payment_providers/adyen_provider.rb` | providers/adyen | 逻辑已完成 | 基于 adyen-go-api-library（Checkout v71）。设置 `merchant_account`、`live_prefix`、`success_redirect_url`；密钥 `api_key`、`hmac_key`。`live_prefix` 非空即选用 live 环境（lago 按 Rails 环境选择，`adyen_provider.rb:20-26`，无法同时存在测试与 live 连接）。按连接和环境做客户端令牌桶限流（lago 没有）。未移植的 spec 用例及原因：providers/adyen/SKIPPED.md |
| 以零金额预授权链接实现的 Adyen 结账 | `app/services/payment_provider_customers/adyen_service.rb` | providers/adyen | 逻辑已完成 | `SetupURL` 以客户外部 ID 作为 shopper reference，币种取客户或组织的币种；Adyen 没有客户对象，因此 `CreateCustomer` 返回 `payment.ErrUnsupported`，shopper reference 即提供者客户 ID。链接还带有 `tanvo_customer_id` 元数据。保存的卡以 `payment.PaymentMethodAttached`（设为默认）到达，被拒的以 `payment.PaymentMethodSetupFailed` 到达 |
| 使用已存支付方式的 Adyen 付款 | `app/services/payment_providers/adyen/payments/create_service.rb` | providers/adyen | 逻辑已完成 | 幂等键 `payment-<id>`；reference 截取末尾 80 个字符。仅在调用方未指定支付方式时列出已存方式（`create_service.rb:69-82`），没有则返回 `payment.ErrNoPaymentMethod`。5xx、429 和连接失败可重试，而不是使付款失败（`create_service.rb:29,44-45`）；401/403 为认证错误 |
| Adyen 付款链接 | `app/services/invoices/payments/adyen_service.rb` | providers/adyen | 逻辑已完成 | 发票与付款请求链接（`payment_requests/payments/adyen_service.rb`）；发票链接与付款请求链接一样带有可支付对象的类型和 ID，并以付款意向为键 |
| Adyen webhook：授权、取消、退款、退款失败、拒付 | `app/services/payment_providers/adyen/handle_event_service.rb` | providers/adyen | 逻辑已完成 | 对每个通知条目做 HMAC-SHA256 校验（lago 只校验第一个，`handle_incoming_webhook_service.rb:30`）并解析每个条目（lago 只处理第一个，`webhooks_controller.rb`）；处理器通过 `payment.WebhookAcknowledger` 回复 `[accepted]`。未知事件码不产生事件（`handle_event_service.rb:24-28`） |
| 通过 OAuth 的 GoCardless 连接 | `app/services/payment_providers/gocardless_service.rb` | providers/gocardless | 逻辑已完成 | 基于 gocardless-pro-go v6。设置 `success_redirect_url`（默认 `https://gocardless.com/`，最多 1024 个字符，须为有效 URL）；密钥 `access_token`、`webhook_secret`。缺少 `webhook_secret` 时 `RegisterWebhook` 生成 50 个字符的密钥（`gocardless_service.rb:33`），保存在密钥而非设置中。以 `live_` 开头的访问令牌选用 live 环境（lago 按 Rails 环境选择，`gocardless_provider.rb:24-30`）。按连接做客户端令牌桶限流；关闭客户端自身的重试，由任务重试。未移植的 spec 用例及原因：providers/gocardless/SKIPPED.md |
| GoCardless 客户，通过 billing request 设置授权 | `app/services/payment_provider_customers/gocardless_service.rb` | providers/gocardless | 逻辑已完成 | `CreateCustomer` 发送邮箱、公司名与名、姓，以调用方的幂等键为键（lago 不发送）；冲突时返回已有客户。`UpdateCustomer` 与 lago 一样不做任何事。`SetupURL` 创建 bacs billing request 及其 flow，重定向和退出 URL 均为成功 URL。授权列为支付方式，类型为其 scheme（lago 记为 `card`，`create_from_provider_service.rb`）；默认取最新的授权。`SetDefaultPaymentMethod` 与 `DetachPaymentMethod` 返回 `payment.ErrUnsupported`（lago 只丢弃本地记录） |
| 针对最新有效授权的 GoCardless 付款 | `app/services/payment_providers/gocardless/payments/create_service.rb` | providers/gocardless | 逻辑已完成 | 使用调用方指定的授权，否则取列出的第一个有效授权；没有则返回带 `no_mandate_error` 码的 `payment.ErrNoPaymentMethod`。幂等键 `payment-<id>`（`create_service.rb:99`）；冲突时获取已有付款。元数据 `tanvo_customer_id`、`tanvo_invoice_id`、`invoice_issuing_date`，或付款请求的可支付对象类型和 ID。所用授权作为 `PaymentResult.ProviderMethodID` 返回。5xx、429 和连接失败可重试；校验错误使付款失败（`create_service.rb:43-46`）。错误码取第一个错误的 reason，而非 lago 报告的 HTTP 状态 |
| GoCardless webhook：付款、退款、授权 | `app/services/payment_providers/gocardless/handle_event_service.rb` | providers/gocardless | 逻辑已完成 | 用连接的 webhook 密钥校验十六进制 HMAC-SHA256 的 `Webhook-Signature`。解析投递中的每个事件：付款动作 `paid_out`、`failed`、`cancelled`、`customer_approval_denied`、`charged_back` 为 `payment.PaymentEvent`；退款动作为 `payment.RefundEvent`；新建的授权会被获取并报告为 `payment.PaymentMethodAttached`（设为默认），通过 API 取消的报告为 `payment.PaymentMethodDetached`。标识符也从资源元数据读取。未知可支付类型的事件记录警告后跳过（lago 使整个投递失败）；获取授权失败且不是未找到时使投递失败（lago 吞掉错误，`mandate_created_service.rb`） |
| Cashfree 连接与付款链接 | `app/services/invoices/payments/cashfree_service.rb` | providers/cashfree | 逻辑已完成 | 基于 net/http，与 lago 一样固定 API 版本 2023-08-01（官方 Go SDK 通过可变的包级变量固定了更新的版本）。密钥 `client_id`、`client_secret`（必填）；设置 `success_redirect_url`（最多 1024 个字符，须为有效 URL，默认 `https://cashfree.com/`）与 `sandbox`（lago 按 Rails 环境选择环境）。`CreatePaymentURL` 为发票或付款请求创建付款链接，不允许部分付款、不发提醒、不由 Cashfree 发送通知，备注写明客户与应付单据并标记为一次性；金额是按币种小数位的精确小数（lago 为 `cents / 100.to_f`）；链接在意图到期时失效，未设置时 10 分钟后失效。Cashfree 不支持商户发起的扣款，因此 `CreatePayment`、客户、已保存的支付方式、退款与取消均返回 `ErrUnsupported`。按连接的客户端令牌桶限流。未移植的规格用例及原因见 providers/cashfree/SKIPPED.md |
| 带签名校验的 Cashfree webhook（付款链接已付） | `app/services/payment_providers/cashfree/` | providers/cashfree | 逻辑已完成 | `VerifyWebhook` 用 client secret 校验时间戳加正文的 base64 HMAC-SHA256，先读 `X-Cashfree-*` 再读 `X-Webhook-*` 请求头。`ParseWebhook` 把写明发票或付款请求的 PAID `PAYMENT_LINK_EVENT` 转为已成功、一次性的 `payment.PaymentEvent`，带已付金额；其提供者付款 ID 为链接 ID（lago 使用应付单据 ID，此处放在 `payment.Refs` 中）。其他状态与事件类型不产生事件；未知的应付单据类型返回 `ErrUnknownPayable` |
| Flutterwave 连接与付款链接 | `app/services/invoices/payments/flutterwave_service.rb` | providers/flutterwave | 逻辑已完成 | 基于 net/http（Flutterwave 没有维护中的 Go SDK），调用 v3 API，以 secret key 作为 bearer token。密钥 `secret_key`（必填）与 `webhook_secret`：连接没有时由 `RegisterWebhook` 生成（64 个十六进制字符），并保留其他密钥；设置 `success_redirect_url`（最多 1024 个字符，须为有效 URL，默认 `https://www.flutterwave.com/ng`）。`FLWSECK_TEST` 开头的密钥为测试连接。`CreatePaymentURL` 创建 30 分钟有效的托管结账页，标题写明组织、描述写明发票，meta 写明客户、组织与应付单据；金额是按币种小数位的精确小数（lago 为浮点数）。付款请求的结账页与发票一样提交到 `/payments`，并带有 webhook 读取的 `tanvo_payable_id`/`tanvo_payable_type` 键（lago 把它们提交到 API 根路径，并用其 webhook 不读取的键标识付款请求）。`PaymentURLParams` 带有组织名称与标志以及每一行的发票 ID。商户发起的扣款、客户、已保存的支付方式、退款与取消均返回 `ErrUnsupported`。按连接的客户端令牌桶限流。未移植的规格用例及原因见 providers/flutterwave/SKIPPED.md |
| 带交易核验的 Flutterwave webhook | `app/services/payment_providers/flutterwave/` | providers/flutterwave | 逻辑已完成 | `VerifyWebhook` 以常数时间比较 `verif-hash` 请求头与 webhook secret；没有 webhook secret 的连接不接受任何投递。`ParseWebhook` 对写明发票或付款请求（meta，否则为交易参考号）的成功 `charge.completed` 调用 `GET /transactions/{id}/verify` 核验，并把已确认的交易转为已成功、一次性的 `payment.PaymentEvent`，带核验后的金额与币种；其提供者付款 ID 为 Flutterwave 交易 ID（lago 使用应付单据 ID，此处放在 `payment.Refs` 中）。未确认或被拒绝的核验与 lago 一样不产生事件；可重试的核验失败会返回错误，以便重新投递（lago 将其丢弃）；未知的应付单据类型返回 `ErrUnknownPayable` |
| Moneyhash 连接（API 密钥、flow ID、获取签名密钥） | `app/services/payment_providers/moneyhash_service.rb` | providers/moneyhash | 逻辑已完成 | 基于 net/http（MoneyHash 没有 Go SDK）。设置 `flow_id`（必填，最多 20 个字符）与 `sandbox`；密钥 `api_key`（必填）、`signature_key`。连接没有签名密钥时，`RegisterWebhook` 获取一次组织的签名密钥，并保留其他所有密钥（`moneyhash_service.rb` `get_signature_key`）。`sandbox` 选用 staging API（lago 按 Rails 环境选择，`moneyhash_provider.rb` `api_base_url`）。每个意向都带上连接的入站端点 `payment.Connection.WebhookURL`。按连接做客户端令牌桶限流。未移植的 spec 用例及原因：providers/moneyhash/SKIPPED.md |
| Moneyhash 客户与卡令牌化结账 | `app/services/payment_provider_customers/moneyhash_service.rb` | providers/moneyhash | 逻辑已完成 | `CreateCustomer` 发送大写的类型、姓名、邮箱、电话、作为 `company_name` 的法定名称、地址行、联系人以及 `tanvo_*` 自定义字段，以调用方的幂等键为键（lago 不发送）；税号仅在为数字时发送（lago 发送 `to_i`，含字母的税号变成 `"0"`）。`UpdateCustomer` 与 lago 一样不做任何事。`SetupURL` 以给定币种（调用方回退到组织的币种）创建 5 个单位的非计划令牌化意向，并原样返回嵌入 URL（lago 追加一个无人读取的 `lago_request` 标记）。已保存的卡只通过卡令牌 webhook 获知；列出、默认与解绑返回 `payment.ErrUnsupported` |
| Moneyhash 商户发起的循环付款与付款链接 | `app/services/payment_providers/moneyhash/payments/create_service.rb` | providers/moneyhash | 逻辑已完成 | `CreatePayment` 以调用方的卡令牌扣款（没有则返回 `payment.ErrNoPaymentMethod`，`payment_requests/payments/moneyhash_service.rb:21`），作为非计划的商户发起意向，以 `payment.PaymentParams.IdempotencyKey` 为键（lago 不发送键），适用于发票与付款请求。金额为按币种小数位的精确小数（lago 用 `div(100)` 截断，`create_service.rb:43`）。状态取意向的状态，否则取其当前交易的状态，否则为 `PENDING`。`CreatePaymentURL` 为发票的应付金额创建客户发起的意向，以付款意向为键并随之过期，客户没有 MoneyHash 客户时同时令牌化卡；lago 中付款请求没有 MoneyHash 链接，返回 `payment.ErrUnsupported`。任何非 2xx 应答为以 HTTP 状态为错误码的 `payment.ProviderError`（`create_service.rb:30-31`）；429、5xx 与连接失败可重试。退款、取消与更新参考返回 `payment.ErrUnsupported`，lago 对 MoneyHash 均未实现 |
| Moneyhash webhook：意向已处理与过期、购买失败、待认证、成功、卡令牌事件 | `app/services/payment_providers/moneyhash/` | providers/moneyhash | 逻辑已完成 | 校验 `MoneyHash-Signature` 的 v3：以签名密钥对 base64 请求体加 `t=` 时间戳计算十六进制 HMAC-SHA256，针对原样投递的请求体（lago 对解析后的请求体重新序列化）。意向与购买事件为带 SUCCESSFUL、FAILED 或 PENDING 及最小单位金额的 `payment.PaymentEvent`；意向事件的可支付类型从其自身的 `data.intent.custom_fields` 读取（lago 读取意向事件中不存在的 `intent.custom_fields`，因此总是当作发票）。卡令牌创建与更新为设为默认的 `payment.PaymentMethodAttached`，带品牌、末四位与有效期；删除为 `payment.PaymentMethodDetached`。未知的可支付类型返回 `payment.ErrUnknownPayable`；其他事件类型不产生事件（lago 使任务失败）。不报告持卡人姓名与发卡行（`payment.MethodDetails` 没有这两项） |
| 取消付款（Stripe、Adyen、GoCardless） | `app/services/payment_providers/cancel_payment_service.rb` | providers/*、libs/billing | 已完成 | 任务 `payments.cancel_payment`（`payments.Service.CancelPayment`）：付款门控的激活过期或被拒绝时，为门控发票的待处理或处理中付款入队（`subscriptions/activation_rules/cancel_service.rb:81-88`）；没有提供者付款或已成功的付款保持不变，不支持取消的提供者（Cashfree、Flutterwave、MoneyHash：`payment.ErrUnsupported`）记录日志并跳过。Stripe：`CancelPayment` 返回新状态，对已终结的意向返回零值结果（`cancel_service.rb` 记录日志并视为成功）。Adyen：以 `cancel-<psp reference>` 为键的异步取消，返回零值结果，结果由 CANCELLATION 通知报告；422 记录日志并视为成功（`adyen/payments/cancel_service.rb`）。GoCardless：同步取消并返回新状态；`cancellation_failed` 拒绝记录日志并视为成功（lago 比较错误码，`gocardless/payments/cancel_service.rb:34`） |
| 更新付款参考（Stripe） | `app/services/payment_providers/update_payment_reference_service.rb` | providers/stripe | 逻辑已完成 | |

### 1.35 付款请求、催收、收据与退款

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 汇总逾期发票（同一货币与计费主体）的付款请求 | `app/services/payment_requests/create_service.rb` | libs/billing | 已完成 | `payments.Service.CreateRequest`。偏差：排除生成中的发票（lago 排除已删除的发票，`create_service.rb:107`）；已接受的偏差：空的 `invoice_ids` 是校验错误（lago 返回 404）。金额是以主单位表示的十进制字符串，与 `Money` 一致。尚不发送邮件。 |
| 扣除贷项通知单抵销后的付款请求金额 | `app/services/payment_requests/create_service.rb` | libs/billing | 已完成 | |
| 付款请求通过提供者扣款，或通过邮件发送 | `app/services/payment_requests/payments/create_service.rb`、`app/mailers/payment_request_mailer.rb` | libs/billing | 已完成 | `payments.create_payment_request_payment` 通过客户的提供者扣取付款请求金额，失败分类与发票相同，并更新请求及其发票；没有提供者客户时通过邮件发送请求（`payments.send_payment_request_email`，`payment_request_mailer.rb`），附带付款链接。偏差：在接入 blob 存储之前，邮件不含发票 PDF 链接，也不会生成缺失的 PDF（`ensure_invoices_pdf`） |
| 付款请求状态更新 | `app/services/payment_requests/update_service.rb` | libs/billing | 已完成 | `setRequestPayment`，状态变化时发出 `payment_request.payment_status_updated` |
| 付款请求 API 与按客户列表 | `app/controllers/api/v1/payment_requests_controller.rb` | apps/server | 已完成 | 过滤条件 `customer_id`、`payment_status`、`currency` 与 `billing_entity_id`（ID 或 `code:<code>`，与其他列表一致；lago 为 `billing_entity_codes`），未知计费主体返回 NotFound（`payment_requests_controller_spec.rb:163`）；代码只匹配未归档的计费主体，lago 也匹配已归档的。按创建时间从新到旧、再按 ID 排序（`base_query.rb:111`）。客户路由的 `customer_id` 列出该客户的付款请求，未知客户返回 NotFound（`customers/payment_requests_controller_spec.rb:15`），Connect 与 REST 行为一致；`/v1/payment_requests` 通过过滤条件 `customer_id` 限定客户，未知客户返回空列表（`payment_requests_controller_spec.rb:118`）。 |
| 付款请求 webhook `payment_request.created`、`payment_status_updated`、`payment_failure` | `app/services/webhooks/payment_requests/` | libs/webhooks | 已完成 | |
| 催收活动（按货币的阈值、尝试间隔天数、最大尝试次数、BCC 邮箱） | `app/models/dunning_campaign.rb`、`app/services/dunning_campaigns/` | libs/billing、apps/server | 已完成 | 公开 API，而不只供管理界面使用：`DunningCampaignService` 基于 libs/billing/dunning。列表按名称排序，可按名称和代码搜索，可按货币和 `applied_to_organization` 过滤（`dunning_campaigns_query.rb`）；`customers_count` 与 `applied_to_organization` 遵循 `types/dunning_campaigns/object.rb`。创建或更新时的 `applied_to_organization` 会将活动应用到默认计费主体或从中移除，并重置回退到它的客户（`create_service.rb:19-24`、`update_service.rb:98-105`）。阈值带有 ID，更新时按 ID 匹配；未列出的阈值被移除，可以移除到一个不剩（`update_service.rb:43-66`）。删除会重置并取消该活动在客户和计费主体上的应用（`destroy_service.rb`）。已接受的差距：没有高级版和 `auto_dunning` 检查 |
| 活动应用于计费主体或客户，可排除客户 | `app/services/dunning_campaigns/process_customer_service.rb` | libs/billing | 已完成 | 优先客户自己的活动，否则用其计费主体的活动；跳过被排除和已归档的客户以及已删除的活动；活动不再适用时丢弃该次尝试（`process_attempt_service.rb:45-53`） |
| 阈值变更时重置不再匹配的客户 | `app/services/dunning_campaigns/update_service.rb` | libs/billing | 已完成 | 不包括被排除在催收之外的客户，因为他们从不被催收 |
| 催收处理：按货币计尝试次数、检查阈值、发起付款请求 | `app/services/dunning_campaigns/bulk_process_service.rb`、`process_attempt_service.rb` | libs/billing | 已完成 | `dunning.Service.ProcessCampaigns`、`ProcessCustomer` 与 `ProcessAttempt`，由每小时（:45）的时钟按组织运行（仅限有未排除客户逾期发票的组织），再为每个客户、每个触发的阈值与计费主体各排一个任务。已归档客户被跳过，与 lago 的默认作用域一致（`customer.rb:139`）。已接受的差距：没有 premium 与 `auto_dunning` 检查；在相应任务类型实现之前，付款请求既不发送邮件，也不通过支付提供商扣款（见上文付款请求扣款） |
| 催收 webhook `dunning_campaign.finished` | `app/services/webhooks/dunning_campaigns/finished_service.rb` | libs/billing | 已完成 | 当所有已催收货币都达到最大尝试次数时，在客户上发出；载荷遵循 `dunning_campaign_finished_serializer.rb`，包括已弃用的 `customer_external_id` |
| 成功付款的付款收据，按客户编号 | `app/services/payment_receipts/create_service.rb` | libs/billing | 已完成 | 已有收据时返回该收据（lago 返回 nil）。 |
| 付款收据 PDF 与 XML，按计费主体设置发送邮件 | `app/services/payment_receipts/generate_pdf_service.rb`、`generate_xml_service.rb` | libs/documents | 逻辑已完成 | |
| 付款收据 API 与重新发送邮件 | `app/controllers/api/v1/payment_receipts_controller.rb` | apps/server | 进行中 | 列出（过滤 `invoice_id`，按创建时间从新到旧、再按 ID 排序，`base_query.rb:111`）、获取与下载已完成；重新发送在接入文档邮件前保持未实现。 |
| 付款收据 webhook `payment_receipt.created`、`payment_receipt.generated` | `app/services/webhooks/payment_receipts/` | libs/webhooks | 计划中 | |
| 通过 Stripe、GoCardless、Adyen 为贷项通知单退款 | `app/services/credit_notes/refunds/` | providers/* | 已完成 | `payments.Service.Refunds` 是贷项通知单服务的 `kernel.RefundGateway`：打开被退款付款的连接，以贷项通知单为键退款。拒绝使退款失败，未知状态以待处理记录提供者退款 ID，其他错误重试。手动付款、已移除的连接、没有适配器或不支持退款（`payment.ErrUnsupported`）的供应商会跳过退款（`creditnotes.ErrRefundSkipped`），与 lago 只为 Stripe、GoCardless 与 Adyen 付款入队退款一致（`credit_notes/create_service.rb:255-264`）。Adyen：4xx 拒绝使退款失败，认证错误与 5xx 重试（lago 任何错误都使其失败，`adyen_service.rb:100-106`）。GoCardless：带 `total_amount_confirmation`；4xx 拒绝使其失败，5xx 与连接失败重试（`gocardless_service.rb:48-57`） |
| 把退款原因映射为提供者的原因 | `app/services/credit_notes/refunds/stripe_service.rb` | providers/stripe | 逻辑已完成 | Stripe 的 `canceled` 退款映射为失败，`requires_action` 映射为待处理；lago 直接复制状态，二者都会校验失败（`credit_note.rb:62`）。其他状态返回 `payment.ErrUnknownStatus`，调用方不予持久化，与 lago 的校验失败一致（`credit_notes/refunds/stripe_service_spec.rb:360`） |
| 根据提供者 webhook 更新退款状态 | `app/services/credit_notes/refunds/`（`update_status`） | libs/billing | 逻辑已完成 | |

### 1.36 x402 代理付款

v1.55.0 中只有准入检查可达（`config/routes/shared_api.rb:174`）：路由检查、余额检查和付款要求，所用的连接在 API 之外配置。结算、付款摘要和链上服务存在，但没有任何调用方。Tanvo 实现这一范围，并像 v1.55.0 一样不接入结算服务。

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| x402 连接（Coinbase CDP 结算方、收款地址、网络、USDC、自动创建客户） | `app/models/x402/connection.rb` | libs/core、libs/store、libs/billing | 已完成 | 没有管理 API：v1.55.0 没有相应路由。`x402pay.Service.CreateConnection` 供运维与测试使用；API 凭据用 `TANVO_SECRET_KEY` 的密钥盒加密 |
| 网络 Base、Base Sepolia、Solana 主网与开发网；各网络的 USDC 资产 | `app/services/x402/network.rb`、`app/services/x402/asset.rb` | libs/core | 已完成 | |
| 准入检查：代理现在能否使用某个指标，或者需要支付什么 | `app/services/x402/gate_checks/check_service.rb`、`app/controllers/api/v1/x402/gate_checks_controller.rb` | libs/billing、apps/server | 已完成 | 余额 − 进行中用量足以支付 1,000 次估算调用时通过（`DEGRADED_CALL_HEADROOM`）。`X402Service.CheckX402Gate`（`POST /v1/x402/gate_checks`），不记录 API 日志。拒绝为 `base` 上的 InvalidArgument（HTTP 400，而非 422）；未知连接为 `connection_not_found`；结算方失败为 Unavailable，原因为 `third_party_error`，ErrorInfo 中带结算方及其消息（Lago 返回 422）。没有 `x402_payments` 开关或高级版限制（§3） |
| 准入检查的路由检查：父套餐及其计费项必须能够以预付钱包控制准入 | `app/services/x402/gate_checks/route_check_service.rb` | libs/billing | 已完成 | |
| 准入检查接口的 `x402` API 密钥权限范围，以及 `x402_connection` 权限范围 | `app/models/api_key.rb` | libs/auth | 已完成 | 准入检查需要 `x402` 写权限。v1.55.0 没有接口使用 `x402_connection` |
| 启用 x402 的钱包（`x402_enabled`） | `app/models/wallet.rb` | libs/core | 已完成 | 已存储；与 Lago 一样不能通过钱包 API 设置 |
| 发票的 x402 列（`x402_connection_id`、`x402_payment_token`） | `app/models/invoice.rb` | libs/core | 已完成 | 仅有列：v1.55.0 中没有任何代码设置或读取它们。令牌有跨所有组织的唯一索引（`db/structure.sql:10561`）；连接属于同一组织 |
| 来自结算方的付款要求 | `app/services/x402/payment_requirements_service.rb` | libs/billing | 已完成 | 读取结算方的 `supported` 类型以确定 Solana 手续费支付方 |
| 结算方客户端（verify、settle、supported），每个请求一个 JWT | `app/services/x402/facilitator/coinbase_cdp_adapter.rb`、`app/services/x402/facilitator/client.rb`、`app/services/x402/cdp/jwt.rb` | providers/x402、apps/server | 已完成 | libs/core 中的 `x402.Facilitators`，由 `providers/x402` 实现（绑定到请求的 EdDSA 或 ES256 令牌、不重试、OpenTelemetry span 与 HTTP 指标），并在 API 角色中装配；只有 `supported` 可达，经由付款要求。传输失败为不含 URL 的固定短语；交易不是字符串的结算成功响应为 `malformed_response` |
| 结算（核验、结算） | `app/models/x402/settlement.rb`、`app/services/x402/settlements/` | libs/billing | 逻辑已完成 | 与 v1.55.0 一样不接入：没有 API、任务或对账调用它们 |
| 通过 JSON-RPC 的链上读取（EVM、Solana） | `app/services/x402/chain/` | providers/x402 | 逻辑已完成 | 与 v1.55.0 一样不接入：没有对账读取它们。libs/core 中的 `x402.ChainReaders`；JSON-RPC 客户端在 500、502、503、504 和瞬时错误时最多尝试 3 次。端点默认为各网络的公共端点；`ParseRPCURLs` 读取覆盖用的 JSON 对象并预先拒绝无效值，库本身不读取环境变量 |
| 用于幂等的付款摘要 | `app/services/x402/payments/compute_digest_service.rb` | libs/billing | 逻辑已完成 | 与 v1.55.0 一样不接入。摘要和交易哈希在所有组织间唯一（Lago 18a741762） |

### 1.37 会计、CRM 与税务集成

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| NetSuite 连接（账户、基于令牌的认证、RESTlet 端点） | `app/models/integrations/netsuite_integration.rb` | providers/netsuite | 已完成 | 直接调用 API，不经 Nango（§3）：适配器以 OAuth 1.0a TBA（HMAC-SHA256）签名 SuiteTalk REST 与 RESTlet 请求，并用 SuiteQL 读取商品、子公司与交易。Lago 的 legacy_script 标志始终关闭：v1.55.0 没有任何服务或 API 设置它（`netsuite_integration.rb:8`，只在 `contacts/payloads/netsuite.rb:72` 读取） |
| NetSuite 子公司 | `app/services/integrations/aggregator/subsidiaries_service.rb` | providers/netsuite | 已完成 | `ListIntegrationSubsidiaries`，与 lago 一样不存储 |
| 按交易 ID 对账 NetSuite 发票 | `app/services/integrations/aggregator/invoices/reconcile_service.rb` | providers/netsuite | 已完成 | 要求先查找时（`RetryInvoiceSync`）以及创建超时后（6 次尝试，间隔 6 分钟，`invoices/create_job.rb:28-36`），发票同步先按 `tranid` 查找发票再创建 |
| NetSuite 税务关联、税种与税码映射 | `app/models/integration_collection_mappings/netsuite_collection_mapping.rb` | providers/netsuite | 已完成 | 随发票与贷项通知单的税行发送；金额为按货币精度的十进制字符串 |
| Xero 连接 | `app/models/integrations/xero_integration.rb` | providers/xero | 已完成 | 以安装的应用（`TANVO_XERO_CLIENT_ID`、`TANVO_XERO_CLIENT_SECRET`）直接与 Xero 进行 OAuth 2.0（`StartIntegrationOauth`、`CompleteIntegrationOauth`），取代 Nango；租户为授权的 `authEventId` 对应的租户；刷新后的授权立即保存。按租户限流为每秒 1 次、突发 60 次（`throttling.rb`），因为 Xero 按租户限流；lago 的限流是全局的。每日上限交给 429 之后的重试 |
| 会计同步设置：发票、贷项通知单、付款 | `app/models/integrations/` | libs/billing | 已完成 | 发票、贷项通知单与付款的 webhook 在其事务中排入同步（`integrations.Service.Triggers`，包装每个计费服务的 webhook） |
| 获取提供者的商品与科目 | `app/services/integrations/aggregator/items_service.rb`、`accounts_service.rb` | providers/* | 已完成 | `SyncIntegrationItems`，以及连接时（`fetch_items_job.rb`）；获取的记录替换同类型的缓存记录 |
| 附加项与计费指标的商品映射，可按计费主体 | `app/services/integration_mappings/` | libs/core、libs/billing、apps/server | 已完成 | `IntegrationService` 基于 libs/billing/integrations 列出、写入与删除集成映射 |
| 集合映射（兜底商品、优惠券、订阅费、最低承诺消费、税、预付额度、贷项通知单、科目、货币） | `app/services/integration_collection_mappings/` | libs/core、libs/billing、apps/server | 已完成 | `IntegrationService` 列出、写入与删除集合映射，按创建时间倒序列出（`integration_collection_mappings_query.rb:9`）。NetSuite 的货币以 lago 的错误码校验（`netsuite_collection_mapping.rb:13-40`）；其他提供者的映射带 NetSuite 字段时为 `value_not_allowed`（§4） |
| 创建与更新会计联系人 | `app/services/integrations/aggregator/contacts/` | providers/* | 已完成 | NetSuite、Xero 以及税务提供者的客户。客户的关联随客户变更一起保存，以便在同一事务中入队同步任务，联系人同步失败时删除；lago 在同步成功后才创建关联（`integration_customers/netsuite_service.rb:25`），最终状态相同。客户更新按 `customers/update_service.rb:210` 推送到提供者；联系人带有名与姓 |
| 发票同步到会计系统 | `app/services/integrations/aggregator/invoices/create_service.rb` | providers/* | 已完成 | NetSuite 以 `fullInvoicePayload` 接收发票的 API 表示（API 的 JSON，而非 lago 的序列化器）。Xero 发票带 `Reference`，金额为按货币最小单位精度的 JSON 数字；映射中缺少收款科目时以 `invalid_mapping` 失败。NetSuite 只发送正数费用，在适配器中过滤 |
| 贷项通知单同步到会计系统 | `app/services/integrations/aggregator/credit_notes/create_service.rb` | providers/* | 已完成 | `fullCreditNotePayload` 同理 |
| 付款同步到会计系统 | `app/services/integrations/aggregator/payments/create_service.rb` | providers/* | 已完成 | 成功的提供者付款时排入（`payments.Service.WithPaymentSync`）；手动付款尚未排入（`payments/manual_create_service.rb:50-51`） |
| 已同步资源记录（发票、付款、贷项通知单、订阅的外部 ID） | `app/models/integration_resource.rb` | libs/core | 已完成 | |
| 手动重新同步发票或贷项通知单 | `app/graphql/mutations/integrations/sync_invoice.rb` | libs/billing | 已完成 | `RetryInvoiceSync`（先在提供者处查找发票）与 `RetryCreditNoteSync`（`sync_credit_note.rb`） |
| 部署了自定义对象与属性的 HubSpot 连接 | `app/services/integrations/hubspot/` | providers/hubspot | 已完成 | 直接与 HubSpot 进行 OAuth 2.0（`TANVO_HUBSPOT_CLIENT_ID`、`TANVO_HUBSPOT_CLIENT_SECRET`），取代 Nango；使用 v3 schema 与属性批量端点。连接在一个任务中部署全部对象与属性集；对象版本与属性版本分别记录（lago 在 `deploy_object_service.rb:9` 与 `deploy_properties_service.rb:9` 之间共用 `invoices_properties_version`，两者交替），且从 1 开始；已存在的属性视为已部署。属性命名为 `tanvo_*`。返回自定义对象类型 ID。按连接限流为每秒 11 次、突发 110 次，因为 HubSpot 按账户限流；lago 的限流是全局的（`config/initializers/throttling.rb`） |
| 每个客户的 HubSpot 公司或联系人 | `app/services/integrations/aggregator/companies/`、`contacts/` | providers/hubspot | 已完成 | 未给出目标对象时按客户类型计算 |
| 带关联的 HubSpot 发票与订阅记录 | `app/services/integrations/aggregator/invoices/hubspot/`、`subscriptions/hubspot/` | providers/hubspot | 已完成 | v3 记录与 v4 默认关联；`SyncHubspotInvoice` 手动排入发票。对象部署前同步的记录以 `object_not_deployed` 失败。发票文件 URL 为限时的 blob URL，在接入 blob 存储前省略；付款状态变化时更新记录（`invoices/update_service.rb:85-102`） |
| Salesforce 连接（实例）与发票 ID 同步 | `app/services/integrations/salesforce/` | providers/salesforce | 已完成 | 连接保存实例 ID；Salesforce 软件包读取 Tanvo 的 API。`SyncSalesforceInvoice` 发送 `invoice.resynced`；`SetInvoiceSalesforceId` 保留发票已有的记录（`sync_salesforce_id_service.rb:16-22`） |
| Anrok 连接，以及为草稿、预览与用量询价税额 | `app/services/integrations/aggregator/taxes/invoices/create_draft_service.rb` | providers/anrok | 已完成 | 即计费引擎的税务提供者（`kernel.Deps.Taxes`）。请求带 `taxDate`；达到速率上限时适配器等待而不失败 |
| 草稿发票税额预览 | `app/graphql/mutations/integrations/fetch_draft_invoice_taxes.rb` | libs/billing、apps/server | 已完成 | `PreviewInvoiceTaxes` 通过客户（ID 或外部 ID）的税务集成为附加项费用询价；没有金额的费用被略去，除非所有费用都没有金额；被拒绝时为带提供者错误码的 `tax_error`，并发出 `customer.tax_provider_error` |
| Anrok 草稿税额缓存 | `app/services/integrations/aggregator/taxes/invoices/draft_taxes_cache_service.rb` | providers/anrok | 已完成 | 每个服务器进程内缓存，默认一天（`TANVO_ANROK_DRAFT_TAXES_CACHE_TTL`，0 关闭） |
| 带公司查找的 Avalara 连接 | `app/services/integrations/aggregator/taxes/avalara/fetch_company_id_service.rb` | providers/avalara | 已完成 | AvaTax REST：草稿与预览为 `SalesOrder` 报价，已定稿发票使用 `createoradjust`，作废使用 `DocVoided` |
| 已定稿发票的税务申报 | `app/services/integrations/aggregator/taxes/invoices/create_service.rb` | providers/* | 已完成 | 对按提供者计税的客户总是排入申报任务 |
| 发票作废时作废或冲销税额 | `app/services/integrations/aggregator/taxes/invoices/void_service.rb`、`negate_service.rb` | providers/* | 已完成 | |
| 贷项通知单的税务申报 | `app/services/integrations/aggregator/taxes/credit_notes/create_service.rb` | providers/* | 已完成 | 不存储贷项通知单税务错误的详情 |
| 重试税务申报与税额作废 | `app/graphql/mutations/integrations/` | libs/billing | 已完成 | 税额计算失败用 `RetryInvoice`；已作废发票用 `RetryInvoiceTaxVoiding`（`invoices/retry_tax_provider_voiding.rb`），与 `provider_taxes/void_service.rb:16-38` 一样以 `tax_error` 失败 |
| 集成重试策略（网关错误、请求上限、限流、超时） | `app/jobs/integrations/` | libs/billing、apps/server | 已完成 | `integrations.RetryRules` 按任务类型给出，与各任务的 `retry_on` 一致，由 apps/server 注册；其他错误不重试。税务调用中供应商的 5xx 视为争用，由任务重试，而 lago 返回 Nango 脚本的 `action_script_runtime_error` 失败（`taxes/invoices/create_draft_service.rb`）；Nango 的内存不足、任务与编排器错误不存在。请求上限会重试（§4） |
| 集成 webhook `customer.accounting_provider_created`、`customer.accounting_provider_error`、`customer.crm_provider_created`、`customer.crm_provider_error`、`integration.provider_error` | `app/services/webhooks/integrations/` | libs/billing | 已完成 | 其负载带 `customer_id` 与 `integration_id` |
| 每个客户只有一个税务集成 | `app/models/integration_customers/base_customer.rb` | libs/core | 已完成 | |
| 集成连接 API（创建、获取、列出、更新、删除） | `app/services/integrations/`、`app/graphql/mutations/integrations/` | libs/billing、apps/server | 已完成 | `IntegrationService` 基于 libs/billing/integrations。代码、名称、同步设置与凭据均可更新；与 lago 的两张表一样，代码在组织的未删除支付提供者之间、以及其他集成之间分别唯一。删除时软删除支付提供者（§1.33），其他集成直接删除（`integrations/destroy_service.rb`）。商品按外部名称排序（`integration_items_query.rb:10`）。设置与凭据在保存前由适配器的 `ValidateConnection` 按 lago 的必填规则检查。凭据从不返回；Anrok 的 `external_account_id` 由 API 密钥派生（`types/integrations/anrok.rb:20-24`）。通过 apps/server 注入的 `integrations.SecurityLogger`，在变更所在事务中写 `integration.created`、`updated` 与 `deleted` 安全日志，资源与 lago 一样为 `integration_name` 与 `integration_type`，更新时另以 `{deleted, added}` 记录变化的 `code`、`name` 与 `settings`（含同步标志，从不含凭据）；SSO 连接的安全日志由 libs/auth 写入 |

### 1.38 出站 Webhook

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| Webhook 端点（每个组织最多 10 个，URL 唯一，可命名） | `app/models/webhook_endpoint.rb`、`app/services/webhook_endpoints/` | libs/webhooks | 已完成 | `webhooks.Endpoints` 的创建、更新、删除；超出上限时以规则 `webhook_endpoints_limit_reached`（FailedPrecondition）拒绝，lago 返回 422。事件类型为显式列表，留空表示全部；不接受 `["*"]` 模式。以 Standard Webhooks 密钥取代 `signature_algo`（§3）。通过 apps/server 注入的 `webhooks.SecurityLogger`，在变更所在事务中写 `webhook_endpoint.created`、`updated` 与 `deleted` 安全日志，资源与 lago 一样为 `webhook_url`（URL 变化时为 `{deleted, added}`），不含 `signature_algo` |
| 每个端点订阅的事件类型（默认全部） | `app/models/webhook_endpoint.rb`、`config/webhook_event_types.yml` | libs/webhooks | 逻辑已完成 | |
| 带描述与类别的事件类型目录 | `config/webhook_event_types.yml` | libs/webhooks | 已完成 | `ListWebhookEventTypes` |
| 带重试与退避的投递 | `app/services/webhooks/send_http_service.rb` | libs/webhooks | 逻辑已完成 | |
| 签名的载荷 | `app/models/webhook.rb` | libs/webhooks | 逻辑已完成 | Standard Webhooks（§3） |
| 阻止私有地址，可显式关闭 | `lib/lago_http_client/`（`AddressGuard`） | libs/webhooks | 逻辑已完成 | |
| 带响应、状态与重试次数的投递记录 | `app/models/webhook.rb` | libs/webhooks | 逻辑已完成 | |
| 手动重试某次投递 | `app/services/webhooks/retry_service.rb` | libs/webhooks | 逻辑已完成 | |
| 带过滤（状态、事件类型、搜索）的投递列表 | `app/graphql/resolvers/webhooks_resolver.rb` | libs/webhooks | 已完成 | `ListWebhookDeliveries` 按状态、事件类型、`http_status`（状态码、`5xx` 这样的类别、`200-204` 这样的区间或 `timeout`）与 `updated_at` 上下界（`from_date`、`to_date`）过滤，并搜索投递 ID 与对象 ID（`app/queries/webhooks_query.rb`）。`timeout` 保留没有响应的失败投递（`webhooks_query.rb:70-72`） |
| 90 天后清理投递记录 | `app/jobs/clock/webhooks_cleanup_job.rb` | libs/webhooks | 已完成 | 每日 clock 任务为每个有端点的组织入队一个清除任务，在该组织的行级安全范围内每个事务删除 1 000 条投递；lago 在 clock 任务中删除所有组织的记录（`app/jobs/clock/webhooks_cleanup_job.rb:19`）。与 lago 一致，保留期从 `updated_at` 起算，使用索引 `(organization_id, updated_at) INCLUDE (id)` |
| Webhook 端点 API | `app/controllers/api/v1/webhook_endpoints_controller.rb` | apps/server | 已完成 | `WebhookService` 的端点增删改查，以及投递的 Get、List 与 Retry。删除返回空响应，lago 返回该端点。不提供 lago 的 `/webhooks/public_key` 与 `json_public_key`（§3）。签名密钥只在创建时返回 |
| 事件组 计费指标：`billable_metric.created`、`updated`、`deleted` | `app/services/webhooks/billable_metrics/` | libs/billing、libs/webhooks | 已完成 | 由指标服务在其事务中发出 |
| 事件组 套餐：`plan.created`、`updated`、`deleted` | `app/services/webhooks/plans/` | libs/webhooks | 计划中 | |
| 事件组 特性：`feature.created`、`updated`、`deleted` | `app/services/webhooks/features/` | libs/billing、libs/webhooks | 已完成 | 由权益服务在其事务中发出 |
| 事件组 客户：`customer.created`、`updated`、`vies_check` | `app/services/webhooks/customers/` | libs/billing、libs/webhooks | 已完成 | 由客户服务在其事务中发出；`customer.vies_check` 由 VIES 检查任务发出 |
| 事件组 客户连接：`customer.payment_provider_created`、`payment_provider_error`、`checkout_url_generated`、`accounting_provider_created`、`accounting_provider_error`、`crm_provider_created`、`crm_provider_error`、`tax_provider_error` | `app/services/webhooks/payment_providers/`、`app/services/webhooks/integrations/` | libs/webhooks | 计划中 | |
| 事件组 订阅：`subscription.started`、`updated`、`terminated`、`canceled`、`incomplete`、`termination_alert`、`trial_ended`、`usage_threshold_reached` | `app/services/webhooks/subscriptions/` | libs/webhooks | 计划中 | |
| 事件组 发票：`invoice.created`、`one_off_created`、`paid_credit_added`、`generated`、`drafted`、`ready_to_finalize`、`voided`、`deleted`、`resynced` | `app/services/webhooks/invoices/` | libs/webhooks | 计划中 | |
| 事件组 发票付款：`invoice.payment_status_updated`、`payment_overdue`、`payment_failure`、`payment_dispute_lost` | `app/services/webhooks/invoices/` | libs/webhooks | 计划中 | |
| 事件组 费用：`fee.created`、`fee.tax_provider_error` | `app/services/webhooks/fees/`、`app/services/webhooks/integrations/taxes/` | libs/webhooks | 计划中 | |
| 事件组 事件：`events.errors` | `app/services/webhooks/events/validation_errors_service.rb` | libs/webhooks | 计划中 | 不移植 `event.error`（§2） |
| 事件组 贷项通知单：`credit_note.created`、`generated`、`provider_refund_failure` | `app/services/webhooks/credit_notes/` | libs/webhooks | 计划中 | |
| 事件组 钱包：`wallet.created`、`updated`、`terminated`、`depleted_ongoing_balance` | `app/services/webhooks/wallets/` | libs/webhooks | 计划中 | |
| 事件组 钱包交易：`wallet_transaction.created`、`updated`、`payment_failure` | `app/services/webhooks/wallet_transactions/` | libs/webhooks | 计划中 | |
| 事件组 付款：`payment.succeeded`、`requires_action`、`payment_provider.error` | `app/services/webhooks/payments/`、`app/services/webhooks/payment_providers/` | libs/webhooks | 计划中 | |
| 事件组 付款请求：`payment_request.created`、`payment_status_updated`、`payment_failure` | `app/services/webhooks/payment_requests/` | libs/webhooks | 计划中 | |
| 事件组 付款收据：`payment_receipt.created`、`generated` | `app/services/webhooks/payment_receipts/` | libs/webhooks | 计划中 | |
| 事件组 催收：`dunning_campaign.finished` | `app/services/webhooks/dunning_campaigns/` | libs/webhooks | 计划中 | |
| 事件组 集成：`integration.provider_error` | `app/services/webhooks/integrations/provider_error_service.rb` | libs/webhooks | 计划中 | |
| 事件组 告警：`alert.triggered` | `app/services/webhooks/usage_monitoring/` | libs/webhooks | 计划中 | |
| 事件组 报价：`quote.created`、`approved`、`voided` | `app/services/webhooks/quotes/` | libs/webhooks | 计划中 | 不设高级版门槛 |
| 事件组 订购单：`order_form.created`、`signed`、`expired`、`voided` | `app/services/webhooks/order_forms/` | libs/webhooks | 计划中 | 不设高级版门槛 |
| 事件组 订单：`order.created`、`executed` | `app/services/webhooks/orders/` | libs/webhooks | 计划中 | 不设高级版门槛 |

### 1.39 入站 Webhook

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 每个提供者连接的提供者 webhook 端点 | `app/controllers/webhooks_controller.rb` | apps/server | 已完成 | `POST /webhooks/{provider}/{connection_id}`（`rpc.InboundWebhooks`）；在所有组织中查找连接（`integration.Repository.OrganizationOf`）。偏差：URL 指明连接，而不是组织加 `?code=`（架构 §4）；未通过验证的投递返回 400，未知连接返回 404，存储失败返回 500 以便提供者重试（lago 任何失败都返回 400，`webhooks_controller.rb`）；没有待处理事件的投递会被确认但不保存，签名头也不保存（lago 保存每次投递，`inbound_webhooks/create_service.rb`）。Adyen 收到 `[accepted]` |
| 按提供者校验签名（Stripe、Adyen HMAC、GoCardless、Cashfree、Flutterwave、Moneyhash） | `app/services/payment_providers/*/validate_incoming_webhook_service.rb` | providers/*、apps/server | 已完成 | `payment.WebhookGateway.VerifyWebhook`；六个适配器均已在 `apps/server` 中装配，由 `POST /webhooks/{provider}/{connection_id}` 提供服务。Flutterwave 以 HTTP 调用核实每笔扣款，期限为 20 秒；可重试的失败返回 500，使投递被重新发送 |
| 处理前的持久存储（pending、processing、succeeded、failed） | `app/models/inbound_webhook.rb`、`app/services/inbound_webhooks/` | libs/webhooks | 逻辑已完成 | 所有提供者，而不只是 Stripe 与 Moneyhash |
| 处理窗口与卡住的 webhook 的重试 | `app/services/inbound_webhooks/process_service.rb`、`app/jobs/clock/inbound_webhooks_retry_job.rb` | libs/webhooks | 逻辑已完成 | |
| 90 天后清理入站 webhook | `app/jobs/clock/inbound_webhooks_cleanup_job.rb` | libs/webhooks | 已完成 | 每天的时钟 `webhook.clean_inbound`，针对有过期投递的组织（`webhooks.Inbound.Clean`） |

### 1.40 流式目的地

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 认领事件类型的目的地（每种类型一个目的地） | `app/models/streaming_destinations/base_destination.rb` | libs/webhooks | 计划中 | 由运维方配置：v1.55.0 没有创建它们的 API，Tanvo 也没有 |
| 事件类型 `customer_usage.refreshed.v1`、`customer_full_usage.refreshed.v1` | `app/models/streaming_destinations/base_destination.rb` | libs/webhooks | 计划中 | |
| 从完整用量事件中排除的套餐 | `app/models/streaming_destinations/base_destination.rb` | libs/webhooks | 计划中 | |
| 带角色扮演与分区键的 Kinesis 目的地 | `app/models/streaming_destinations/kinesis_destination.rb`、`lib/lago/kinesis/producer.rb` | libs/webhooks | 计划中 | |
| 带钱包信息的用量刷新信封 | `app/services/event_destinations/` | libs/webhooks | 计划中 | |
| 投递去重与日志（已投递、跳过、失败、被取代、丢弃） | `app/jobs/deliver_event_job.rb`、`app/services/event_destinations/delivery_logger.rb` | libs/webhooks | 计划中 | |

### 1.41 报价、订购单与订单

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 带状态的报价与报价版本 | `app/models/quote.rb`、`app/models/quote_version.rb`、`app/services/quotes/`、`app/services/quote_versions/` | libs/billing、apps/server | 已完成 | `QuoteService`（`quotes.Service`）：创建、获取、列出、更新负责人、添加图片，以及版本的获取、列出、更新、克隆、批准、作废（`quotes_controller.rb`、`quote_versions_controller.rb`、`quotes/versions_controller.rb`、GraphQL `Mutations::Quotes::*`、`Mutations::QuoteVersions::*`）。负责人是成员 ID 而非用户 ID。没有高级版门槛，也没有功能开关。图片与签署文件需要 blob 存储；没有时以 `blob_store_not_configured` 拒绝。锁错误原样返回，不映射为 `concurrency_conflict`（`quotes/lock_service.rb`） |
| 订单类型：创建订阅、修改订阅、一次性，各带校验器 | `app/services/quote_versions/validators/` | libs/billing | 已完成 | `quotes/validators`（JSON Schema 结构校验器与业务校验器） |
| 报价内容中的提及变量 | `app/services/quotes/` | libs/billing、libs/documents | 进行中 | 在批准时计算并快照（`compute_mention_variables_service.rb`）；本地化器（`mention_variables_localizer.rb`）属于文档模块，未移植 |
| 交易过期 | `app/services/quotes/`（`DealExpiration`） | libs/billing | 已完成 | 限定订购单的 `expires_at` 与订单的 `execute_at`（`after_deal_expiration`） |
| 订购单（已创建、已签署、已过期、已作废） | `app/models/order_form.rb`、`app/services/order_forms/` | libs/billing、apps/server | 已完成 | `OrderFormService`：获取、列出（过滤与搜索）、签署（可带签署文件 data URI、执行模式与 `execute_at`）、作废并级联作废报价版本（`order_forms_controller.rb`） |
| 订购单过期 | `app/jobs/clock/expire_order_forms_job.rb` | libs/billing | 已完成 | 按客户时区已到过期日的已生成订购单；作废报价版本 |
| 订单与定时执行 | `app/models/order.rb`、`app/jobs/clock/execute_scheduled_orders_job.rb` | libs/billing、apps/server | 已完成 | `OrderService`：获取、列出、更新（执行模式、`execute_at`）、执行（`orders_controller.rb`、`Orders::*ExecuteService`）。执行会创建订阅、优惠券与钱包，把被修改订阅的计划换成覆盖计划，或开出一次性发票。失败时回滚，再把订单标记为失败并记下错误码。每小时的时钟为每个到期订单入队一个 `billing.execute_order`。模式 `execute_in_lago` 即 `EXECUTION_MODE_EXECUTE` |

### 1.42 用量归属

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 用量归属类型，分层与扁平（`account_tree` 开关） | `app/models/usage_attribution_type.rb`、`app/controllers/api/v1/usage_attribution_types_controller.rb` | libs/core、libs/billing、apps/server | 已完成 | 基于 libs/billing/usageattribution 的 `UsageAttributionService` Create、Get、List、Update 与 Delete。类型按 ID 或 `code:<code>` 读取，父类型以引用（`parent_id`）给出，lago 使用 `parent_code`。`account_tree` 开关不作为门槛（`usage_attribution_types_controller.rb:118`）。列表过滤 `root = false`（有未删除父类型的类型）超出 lago，lago 只读取 `roots: true`（`usage_attribution_types_query.rb:12`）。Delete 返回空响应体，lago 返回被删除的类型（`usage_attribution_types_controller.rb:43-52`） |
| 归属取值 | `app/models/usage_attribution_value.rb` | libs/core、libs/store | 已完成 | 随其类型存储、读取与删除；与 v1.55.0 一样，没有代码从事件中记录取值，因此取值只用于冻结其类型的 code、role 与父类型 |
| 归属用量查询 | `app/controllers/api/v1/customers/attributed_usage_controller.rb` | libs/billing | 计划中 | 与 v1.55.0 一样只有读取侧：查询按 `events_enriched.attribution_labels` 分组，而没有任何代码写入该列，因此归属用量为空 |

### 1.43 数据导出

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 按列表过滤条件以 CSV 导出发票、发票费用、贷项通知单、贷项通知单条目 | `app/services/data_exports/` | libs/billing、apps/server | 进行中 | `DataExportService` 的 Export* RPC 基于 libs/billing/exports；仅限成员调用（lago 的 mutation 需要用户），并写 `export.created` 安全日志。CSV 列遵循 `app/services/data_exports/csv/`；总是写出 `progressive_billing_credit_amount_cents`（不设高级版门槛，`csv/invoices.rb:16`）。发票过滤接受 lago 发票导出过滤的字段；贷项通知单过滤另外接受 `customer_id` 与 `customer_external_id`。附加项费用的日期取所存边界的 UTC 日期。安全日志在导出所在事务中写入（`create_service.rb:28`）。`exports.*` 任务在 exports 队列上运行；尚未接入 blob 存储，因此各部分会被渲染，但文件不会保存，合并任务只记录日志。响应带有导出的 `id` 与 `status`，与 lago mutation 的结果一致 |
| 导出拆分为多个部分再合并 | `app/models/data_export_part.rb` | libs/billing | 逻辑已完成 | 每部分 20 个对象；合并任务按导出唯一 |
| 导出文件 7 天后过期 | `app/models/data_export.rb` | libs/billing | 逻辑已完成 | |
| 导出完成邮件 | `app/mailers/data_export_mailer.rb` | libs/mail | 逻辑已完成 | |
| 发票、费用、贷项通知单、钱包、订阅、告警、每日用量及其他业务表的导出视图（`exports_*`） | `db/views/exports_*.sql` | libs/store | 计划中 | 只读 PostgreSQL 视图，与删除记录（§1.49）一起构成数据仓库同步的契约 |
| 导出视图的数据仓库同步 | Lago 私有的数据管道 | ee/pipeline | 计划中 | 读取视图的独立进程（架构 §14） |

### 1.44 分析

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 总收入 | `app/services/analytics/gross_revenues_service.rb` | libs/billing、libs/store、apps/server | 已完成 | `AnalyticsService`。基于 PostgreSQL，按 `app/models/analytics/` 的 SQL，按月（UTC）、货币与计费主体（`billing_entity_id`）计算；发票之外预付的费用计入其订阅的计费主体。金额精确求和后截断，lago 截断的是浮点求和。未知计费主体返回 NotFound，lago 则忽略它（`app/controllers/api/v1/analytics/base_controller.rb:31-33`）。不设高级版门槛；可按 `customer_id`（lago 的 `external_customer_id`）过滤 |
| MRR | `app/services/analytics/mrrs_service.rb` | libs/billing、libs/store、apps/server | 已完成 | `AnalyticsService`。基于 PostgreSQL，按 `app/models/analytics/` 的 SQL，按月（UTC）与货币计算；金额精确求和后截断。未知计费主体返回 NotFound，lago 则忽略它（`app/controllers/api/v1/analytics/base_controller.rb:31-33`）。不设高级版门槛；跳过计费月数为零的费用；与 lago 一样不支持客户过滤 |
| 发票回款 | `app/services/analytics/invoice_collections_service.rb` | libs/billing、libs/store、apps/server | 已完成 | `AnalyticsService`。基于 PostgreSQL，按 `app/models/analytics/` 的 SQL，按月（UTC）、货币与支付状态计算；金额精确求和后截断。未知计费主体返回 NotFound，lago 则忽略它（`app/controllers/api/v1/analytics/base_controller.rb:31-33`）。不设高级版门槛；与 lago 一样不支持客户过滤；`is_customer_tin_empty` 只保留没有（或有）税务识别号的客户 |
| 已开票用量 | `app/services/analytics/invoiced_usages_service.rb` | libs/billing、libs/store、apps/server | 已完成 | `AnalyticsService`。基于 PostgreSQL，按 `app/models/analytics/` 的 SQL，按月（UTC）、货币与计费指标计算；金额精确求和后截断。未知计费主体返回 NotFound，lago 则忽略它（`app/controllers/api/v1/analytics/base_controller.rb:31-33`）。不设高级版门槛；与 lago 一样不支持客户过滤 |
| 逾期余额 | `app/services/analytics/overdue_balances_service.rb` | libs/billing、libs/store、apps/server | 已完成 | `AnalyticsService`。基于 PostgreSQL，按 `app/models/analytics/` 的 SQL，按付款到期月（UTC）、货币与计费主体（`billing_entity_id`）计算；金额精确求和后截断。未知计费主体返回 NotFound，lago 则忽略它（`app/controllers/api/v1/analytics/base_controller.rb:31-33`）。不设高级版门槛；可按 `customer_id`（lago 的 `external_customer_id`）与 `is_customer_tin_empty` 过滤 |
| 分析缓存（4 小时） | `app/services/analytics/` | libs/billing | 计划中 | 每次请求都重新计算报表：尚无缓存层，因此结果总是最新的，而 lago 的结果最多可能滞后 4 小时 |
| 按套餐的 MRR、预付额度、按客户与套餐的收入流、用量与聚合金额 | Data API（`app/services/data_api/`） | ee/analytics | 计划中 | |
| 嵌入式仪表盘 | Superset 嵌入（`app/services/` 中的 superset） | ee/analytics | 计划中 | |
| 用量预测 | 仅前端使用的高级版开关 | ee/analytics | 计划中 | |
| 收入确认 | 仅前端使用的高级版开关 | ee/analytics | 计划中 | |

### 1.45 日志

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 活动日志（来自 API、管理界面、系统的资源变更） | `app/services/utils/activity_log.rb` | libs/core、libs/billing、libs/auth | 已完成 | 存储于 PostgreSQL，由 `auth.AuditLogs` 查询（`activity_logs_query.rb`）。`auditlog.ActivityProducer` 声明了 `clickhouse/activity_log.rb` 的 75 种活动类型与 20 种资源类型，并通过 `kernel.Deps.LogActivity` 在业务事务内写入日志，带有请求的操作者（API 密钥、用户，作业则为 system；见 `activity_log.rb` 的 `activity_source`、`user_id`），`*.updated` 类型还记录变更的属性（`updated_at` 除外）。对象是 snake_case 形式的领域值，而非 v1 序列化器的输出。Lago 在提交后写入 Kafka。已记录：计费主体、计费指标、客户（门户更新不记录，`customer_portal/customer_update_service.rb` 不写日志；upsert 的 `customer.updated` 不带变更，同 `upsert_from_api_service.rb:161`）、发票（drafted、ready_to_finalize、created、one_off_created、paid_credit_added、voided、在被作废发票上记录的 regenerated、deleted、payment_overdue、payment_status_updated）、付款、付款收据与付款请求、贷记单（created、refund_failure）、钱包（created、updated）与钱包交易（由参数或预付费额度抵扣创建，结算或标记失败时更新，记录在其钱包上）、优惠券与已应用优惠券、功能与权益（每个计划的 `plan.updated` 和 `subscription.updated`，不带变更）、报价、订单表与订单、计划（created；updated，覆盖计划除外；deleted，在删除作业运行时按计划逐个记录；lago 的目录计划即 Tanvo 的计划，记录在 `plan` 资源上）、订阅（started、incomplete、terminated、canceled，以及计划降级与取消待生效降级时不带变更的 `subscription.updated`，均与其 webhook 一同记录；更新操作的 `subscription.updated` 带变更，未来才开始的订阅除外）、产品类别（删除时也为其每个产品记录日志）、产品、产品筛选器与费率卡（创建、更新或删除费率记为其费率卡的 `rate_card.updated`，对象包含费率卡的费率；随费率卡一起创建的费率不单独记录）：报价版本记录在其报价上，付款记录在其发票上，已应用优惠券记录在其优惠券上。合同、计划费率卡与阶段不记录日志，与 lago 一致。待接入：`*.generated`、`email.sent` 和付款失败类型，它们的文档、邮件与支付提供方错误路径尚未接入；付款激活规则的 `invoice.created` 已记录 |
| 每个组织的活动日志保留期 | `app/models/organization.rb` | libs/auth | 已完成 | 应用于列表，不应用于读取单条记录（`activity_logs_query.rb`、`activity_logs_controller.rb`） |
| 写请求的 API 日志 | `app/services/utils/api_log.rb` | apps/server | 已完成 | `APILogs` 拦截器记录 API 密钥的每个有副作用的调用（事件与日志服务除外，见 `api_loggable.rb` 的 `skip_audit_logs!`），且不在请求路径上：调用把日志放入有界缓冲区，由写入器按批写入 PostgreSQL（每批一个事务），而非经由 Kafka。放不进缓冲区或所在批次写入失败的日志会被丢弃，并计入 `tanvo.api_log.dropped` 指标（`reason` 为 overflow 或 write_error）；重复的请求 ID 保留第一条日志。Connect 调用记录为 `post`，路径为过程名；错误响应为 `{code, message}` |
| 安全日志（登录、用户、角色、API 密钥、集成、webhook 端点），保留 90 天 | `app/services/utils/security_log.rb` | libs/auth、apps/server | 已完成 | 各服务通过 `auth.NewRepoSecurityLogger` 写入，附带用户以及（API 密钥以外的）客户端设备；超过 90 天的记录既不列出也不可读取（`clickhouse/security_log.rb:12`）。无付费功能门槛 |
| 邮件已发送活动 | `app/services/utils/activity_log.rb` | libs/mail | 逻辑已完成 | |
| 带过滤的日志 API | `app/controllers/api/v1/activity_logs_controller.rb`、`api_logs_controller.rb`、`security_logs_controller.rb` | apps/server | 已完成 | `LogService`：以过滤表达式提供 `activity_logs_query.rb`、`api_logs_query.rb` 与 `security_logs_query.rb` 的过滤条件，含 `logged_at` 区间；列出安全日志需要上界（`security_logs_query.rb:17`） |
| 日志存放在 ClickHouse 中，没有 ClickHouse 时存放在 PostgreSQL 中 | `db/clickhouse_migrate/` | libs/eventstore | 计划中 | Lago 要求 ClickHouse |

### 1.46 客户门户

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 限定于一个客户的门户访问令牌 | `app/services/customer_portal/` | libs/auth、apps/server | 已完成 | 短期有效的签名令牌，作为门户 RPC 的 bearer 凭据发送。`Auth` 拦截器用 `auth.PortalAuthenticator` 校验令牌，并要求客户存在且未被删除（lago `CustomerPortalUser`、`Customer.find_by`） |
| 门户中查看与更新客户资料 | `app/graphql/resolvers/customer_portal/`、`app/services/customer_portal/customer_update_service.rb` | libs/billing/customers、apps/server | 已完成 | `GetPortalOrganization`（名称与 logo 取自组织；币种、时区、文档语言、发票页脚与宽限期取自默认计费主体，Tanvo 将其保存在那里）、`GetPortalCustomer`（适用时区、计费主体文档语言）与 `UpdatePortalCustomer`（通过 `customers.Service.UpdateFromPortal` 以字段掩码更新门户资料：仅门户字段，EU 税与 API 相同地应用，不发送 `customer.updated` webhook）。国家代码与 API 相同地转为大写；没有 premium 标志，也没有发票搜索词刷新任务，Tanvo 两者皆无 |
| 门户中的发票列表与下载 | `app/graphql/resolvers/customer_portal/invoices_resolver.rb`、`app/graphql/mutations/customer_portal/download_invoice.rb` | libs/billing/invoicing、apps/server | 已完成 | `ListPortalInvoices`（客户的可见发票，最新在前，`status` 过滤与搜索，分页令牌）；`DownloadPortalInvoice`（`invoicing.Service.DownloadForCustomer`）：其他客户的发票为 `invoice_not_found`，草稿为 `is_draft`；已生成时响应携带 PDF 与 XML 文档，否则将其生成加入队列 |
| 门户中的订阅与当前用量 | `app/graphql/resolvers/customer_portal/subscription*_resolver.rb`、`customers/usage_resolver.rb`、`customers/projected_usage_resolver.rb` | libs/billing/subscriptions、apps/server | 已完成 | `ListPortalSubscriptions`（默认所有状态；`status`、`plan_code` 与 `currency` 过滤）、`GetPortalSubscription`、`GetPortalCurrentUsage`（不含税，按需包含预测）；没有用量缓存 |
| 门户中的钱包与钱包充值 | `app/graphql/resolvers/customer_portal/wallet*_resolver.rb`、`app/graphql/mutations/customer_portal/wallet_transactions/create.rb` | apps/server | 已完成 | `ListPortalWallets`（默认活跃，单个 `status` 值，按扣减顺序，分页令牌）、`GetPortalWallet`（余额、进行中余额、付费充值上下限）与 `TopUpPortalWallet`（仅付费额度；没有时不创建任何交易；其他客户的钱包在 `wallet_id` 上为 `wallet_not_found`） |
| 门户中的逾期余额与发票回款 | `app/graphql/resolvers/customer_portal/analytics/` | libs/billing/analytics、libs/store、apps/server | 已完成 | `GetPortalOverdueBalance` 与 `GetPortalInvoiceCollection` 使用客户的币种，限定于该客户（发票回款查询新增客户过滤）；没有缓存 |
| 门户用户界面 | Lago 前端 | apps/web | 计划中 | |

### 1.47 文档、电子发票与邮件

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 发票 PDF（当前模板第 4 版及其局部模板） | `app/views/templates/invoices/v4/` | libs/documents | 逻辑已完成 | Typst 模板（§3） |
| 贷项通知单 PDF | `app/views/templates/credit_notes/` | libs/documents | 逻辑已完成 | |
| 付款收据 PDF | `app/views/templates/payment_receipts/` | libs/documents | 逻辑已完成 | |
| 付款请求邮件 | `app/mailers/payment_request_mailer.rb`、`app/views/payment_request_mailer/requested.slim` | libs/mail | 逻辑已完成 | 也渲染为文档 |
| 文档语言 de、en、es、fr、it、nb、pt-BR、sv、zh-TW | `config/locales/` | libs/documents | 逻辑已完成 | 另有 zh-CN（Tanvo 新增）；中文文档需要 Noto CJK 字体（fonts-noto-cjk），否则渲染以 `ErrMissingFont` 失败 |
| 品牌水印 | `app/views/templates/` | libs/documents | 逻辑已完成 | 没有水印，因此无需移除 |
| UBL 发票（EN 16931、XRechnung 3.0、Peppol BIS 3） | `app/serializers/e_invoices/invoices/ubl/` | libs/documents | 逻辑已完成 | |
| 以 Factur-X / ZUGFeRD 嵌入的 CII 发票 | `app/serializers/e_invoices/invoices/cii/` | libs/documents | 逻辑已完成 | |
| 电子发票代码表 | `app/serializers/e_invoices/` | libs/documents | 逻辑已完成 | |
| 贷项通知单与收据的电子发票 | `app/serializers/e_invoices/credit_notes/`、`app/serializers/e_invoices/payments/` | libs/documents | 逻辑已完成 | |
| 文档邮件（发票、贷项通知单、收据、付款请求） | `app/mailers/` | libs/mail | 逻辑已完成 | |
| 重新发送文档邮件 | `app/services/emails/resend_service.rb` | libs/mail | 逻辑已完成 | |
| SMTP 投递与自定义发件地址 | `config/environments/production.rb` | libs/mail | 逻辑已完成 | |

### 1.48 定时任务

Lago 的每个时钟任务都成为一个 River 周期任务，或一个在到期时刻调度的任务（[架构 §9](architecture.zh-CN.md#9-任务与调度libsjobs)）。`libs/jobs` 定义了调度表中的每一项；某一行在负责其工作的库注册了处理器后才算完成。每个周期任务都运行在 `clock` 队列上，并把按组织划分的任务分发到各工作负载队列；Lago 的部分时钟任务（订阅与合同激活、优惠券与钱包终止、免费试用计费）则在时钟任务中直接完成所有组织的工作。

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 激活订阅与合同、刷新草稿（每 5 分钟） | `clock.rb` | libs/jobs | 已完成 | 合同激活分派给有待生效订阅的组织 |
| 订阅活动处理（每分钟） | `clock.rb` | libs/jobs | 已完成 | 由每个订阅每分钟一个的延迟任务代替（§1.32）；该时钟任务类型未注册 |
| 刷新生命周期用量与钱包实时余额（每 5 分钟） | `clock.rb` | libs/jobs | 已完成 | 与 lago 选择条件相同：已开票用量被标记的生命周期用量，以及被标记 `awaiting_wallet_refresh` 且有活跃钱包的客户（`app/jobs/clock/refresh_lifetime_usages_job.rb:9-19`、`app/jobs/clock/refresh_wallets_ongoing_balance_job.rb:10`）；在错误详情实现之前，lago 的 `without_tax_errors` 没有对应条件。钱包也由用量刷新信号刷新（§1.29） |
| 终止已结束的订阅、校验事件（每小时 :05） | `clock.rb` | libs/jobs | 已完成 | 两者均已装配；事件校验按组织与时钟小时各入队一个任务 |
| 订阅计费（每小时 :10） | `clock.rb`（`SubscriptionsBiller`） | libs/jobs | 已完成 | 每个有活跃订阅的组织在每个时刻一个计费任务，再为每个到期订阅入队一个任务 |
| 创建与处理计费片段（每小时 :12、:17） | `clock.rb` | libs/jobs | 已完成 | 创建分派给有活跃订阅的组织，处理分派给有待开票片段的组织 |
| API 密钥使用情况、每日用量（每小时 :15） | `clock.rb` | libs/jobs | 已完成 | 每个组织每个时刻一个每日用量任务 |
| incomplete 订阅过期、定稿发票（每小时 :20） | `clock.rb` | libs/jobs | 已完成 | |
| 把发票标记为逾期（每小时 :25） | `clock.rb` | libs/jobs | 已完成 | |
| 重试生成发票、终止优惠券（每小时 :30） | `clock.rb` | libs/jobs | 已完成 | 对每个有订阅发票处于生成中超过一天且没有 `invoice_generation_error` 的组织运行一个任务（`app/jobs/clock/retry_generating_subscription_invoices_job.rb:9-27`） |
| 免费试用计费（每小时 :35） | `clock.rb` | libs/jobs | 已完成 | |
| 订购单过期、取消被放弃的付款（每小时 :40） | `clock.rb` | libs/jobs | 已完成 | 订购单过期已装配；被放弃的付款按组织分派 `payments.cancel_abandoned_payments` |
| 终止钱包、执行定时订单、催收（每小时 :45） | `clock.rb` | libs/jobs | 已完成 | 钱包终止、定时订单与催收已装配 |
| 终止提醒、终止循环规则（每小时 :50） | `clock.rb` | libs/jobs | 已完成 | 循环规则随钱包由同一个任务终止 |
| 按间隔的钱包充值（每小时 :55） | `clock.rb` | libs/jobs | 已完成 | |
| 重试失败的发票、重试入站 webhook（每 15 分钟） | `clock.rb` | libs/jobs | 进行中 | 为每个拥有 `tax_error_message` 包含 `API limit` 的失败发票的组织排队一个 `billing.retry_tax_failed_invoices`（`app/jobs/clock/retry_failed_invoices_job.rb:8-14`）；入站 webhook 重试尚未装配 |
| 清理 webhook、入站 webhook 与删除记录（每天） | `clock.rb` | libs/jobs | 进行中 | webhook 投递与删除记录清理已装配，每个组织一个清除任务；入站 webhook 清理待完成 |
| 消费订阅刷新队列（每 10 秒） | `clock.rb` | libs/jobs | 已完成 | 由 `usage.refresh` 消费者代替，它把每个信号立即交给计费（订阅活动与钱包刷新） |
| 按工作负载划分的队列 | `config/sidekiq/` | libs/jobs | 逻辑已完成 | River 队列按组织分片，并按组织使用令牌桶；周期任务运行在不分片的 `clock` 队列上 |

### 1.49 平台服务

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 加密提供者密钥与集成凭据 | `app/models/concerns/secrets_storable.rb` | libs/store | 计划中 | AES-256-GCM 信封加密 |
| 供下游同步使用的删除记录 | `db/structure.sql`（`record_deletions`、`record_deletion()`）、`db/views/exports_record_deletions_v01.sql`、`app/jobs/clock/record_deletions_cleanup_job.rb` | libs/store, libs/billing | 进行中 | 被删除的费用、发票订阅、发票应用税与贷项通知单应用税的墓碑（`export.RecordDeletion`；费用税是费用的一列）。lago 用延迟 `AFTER DELETE` 触发器写入；本 schema 不含触发器，因此存储层记下语句可能删除的行，并在提交前的最后一条语句中为已不存在的行写墓碑，统一以一个 `clock_timestamp()` 打时间戳：回滚不写，晚提交者时间戳更晚，与触发器一致。memstore 比较提交前后的被跟踪行。`billing.Engine.CleanRecordDeletions` 在一个日历月后删除它们，每个事务 10 000 条，每天 01:20 运行。`exports_record_deletions` 视图随导出视图（§1.43）提供；删除事件也写入 `audit_events` |
| 发票上的错误详情（未提供、税务错误、税额作废错误、发票生成错误） | `app/models/error_detail.rb` | libs/core | 计划中 | |
| 带超时、重试与 SSRF 防护的出站 HTTP 客户端 | `lib/lago_http_client/` | libs/webhooks | 计划中 | 经由 `libs/core` 端口供提供者共用 |
| 业务对象的变更历史 | PaperTrail（`app/models/concerns/paper_trail_traceable.rb`） | libs/core | 计划中 | `audit_events` |

### 1.50 Lago 的开关及其在 Tanvo 中的归属

| Lago 开关 | 类型 | Tanvo 归属 | 说明 |
| :--- | :--- | :--- | :--- |
| `License.premium?`（套餐覆盖、调整费用、付款请求、Anrok、分析、预览、批量催收） | 许可证 | 核心 | |
| `progressive_billing`、`lifetime_usage`、`granular_lifetime_usage`、`projected_usage`、`preview` | 高级集成 | 核心 | |
| `netsuite`、`xero`、`hubspot`、`salesforce`、`avalara`、`anrok` | 高级集成 | 核心 | providers/* |
| `okta`、`entra_id` | 高级集成 | 核心 | libs/auth |
| `auto_dunning`、`manual_payments`、`issue_receipts`、`beta_payment_authorization` | 高级集成 | 核心 | |
| `api_permissions`、`custom_roles`、`security_logs` | 高级集成 | 核心 | |
| `multi_entities_pro`、`multi_entities_enterprise` | 高级集成 | 核心 | 计费主体数量不设上限 |
| `from_email`、`remove_branding_watermark` | 高级集成 | 核心 | `from_email` 即组织设置 `from_email_enabled` |
| `events_targeting_wallets` | 高级集成 | 核心 | |
| `order_forms` | 高级集成与功能开关 | 核心 | |
| `revenue_share` | 高级集成 | 核心 | 合作伙伴账户与自开票 |
| `revenue_analytics` | 高级集成 | 核心 | 每日用量与分析 API 在核心中；仪表盘在 ee/analytics |
| `analytics_dashboards`、`forecasted_usage`、`revenue_recognition` | 高级集成（仅前端） | ee/analytics | |
| Data API、Superset | 私有云服务 | ee/analytics | |
| 财务助手、AI 对话 | 私有云服务 | ee/assistant | 带 MCP 服务器 |
| CS 管理工具 | 私有云服务 | ee | 组织开关改为 `tanvo admin` 命令 |
| 数据管道（`exports_*` 视图的数据仓库同步） | 私有云服务 | ee/pipeline | 视图（§1.43）和删除记录（§1.49）在核心中 |
| 功能开关 `postgres_enriched_events`、`multi_connection`、`product_catalog`、`realtime_usage`、`account_tree`、`x402_payments`、`stripe_shared_payment_token`、`skip_credit_invoice_auto_payment_delay` | 功能开关 | 核心 | 不带开关直接提供；最后一个成为计费主体设置 |
| 组织列 `custom_aggregation` | 列开关 | 不移植 | 控制已废弃的 `custom_agg`（§2） |

---

## 2. 已废弃且不移植

Lago 自己标记为废弃、只为旧数据保留，或作为死代码保留的部分。Tanvo 没有 Lago 数据需要读取，因此这些都不移植。移植的测试用例若涉及它们，则列在 `skipped` 下并引用本节（[测试策略 §2](testing.zh-CN.md)）。

| 条目 | 依据 | 原因 |
| :--- | :--- | :--- |
| `custom_agg` 聚合与 custom 计费模型 | `app/services/billable_metrics/aggregations/custom_service.rb`（"NOTE: DEPRECATED service"）；`app/services/charge_models/custom_service.rb` 只读取其结果 | Lago 已废弃；它在沙箱（`LagoUtils::RubySandbox`）中运行客户编写的 Ruby 代码。表达式（§1.9）与 dynamic 计费模型覆盖逐事件的计算 |
| `recurring_count_agg` 聚合 | `app/models/billable_metric.rb`（枚举值 4 已删除） | Lago 已删除；循环是其他聚合上的一个开关 |
| 分组与分组属性 | `app/models/group.rb`、`app/models/group_property.rb`；`cached_aggregations.group_id`、`fees.group_id` | 由计费指标过滤器与计费项过滤器取代 |
| 计费项属性 `grouped_by` | `app/models/charge.rb`、`app/services/charges/` | `pricing_group_keys` 的别名 |
| 计费指标索引计数器 | `app/models/billable_metric.rb`（"DEPRECATED since 2024-11-22"） | 未使用 |
| `billable_metrics.properties` | `db/structure.sql` | 未使用的列 |
| 不带代码的计费项过滤器级联路径 | `app/services/charge_filters/create_or_update_batch_service.rb` | 为引入代码之前创建的过滤器保留 |
| `organizations.api_key` | `db/structure.sql` | 由 API 密钥取代 |
| 被忽略的组织列 `clickhouse_aggregation`、`pre_filter_events` | `app/models/organization.rb`（`ignored_columns`） | 已移除的设置 |
| 组织级文档设置（`eu_tax_management`、`email_settings`、按组织编号、宽限期、付款期限） | `app/models/organization.rb`、`app/jobs/invoices/update_grace_period_from_billing_entity_job.rb` | 已移到计费主体 |
| `taxes.applied_to_organization` | `app/models/tax.rb` | 组织级税的兼容垫片 |
| Data API 基础控制器 | `app/controllers/data_api/base_controller.rb` | 死代码 |
| 开关 `wallet_traceability`、`x402_reservation_counter_disabled`、`fixed_charge_usage_delta_migration` | `app/models/organization.rb` | 未使用或只用于迁移的开关 |
| 已触发告警的 `resolved` 与种子类型 | `app/models/usage_monitoring/triggered_alert.rb` | v1.55.0 中从未产生：已触发告警的 `kind`（`triggered`、`resolved`、`seeded`）、`in_alarm_thresholds` 和 `fully_resolved` 列存在，但没有任何代码写入已解决或种子告警，也不发送 `alert.resolved` |
| 管理 REST 控制器（只限本地） | `app/controllers/admin/` | 由 `tanvo admin` 取代 |
| `customers.vat_rate`、`billing_entities.vat_rate` | `db/structure.sql` | 由税取代 |
| `billing_entities.archived_at`、不起作用的 `destroyBillingEntity` mutation | `app/models/billing_entity.rb`、`app/graphql/mutations/billing_entities/` | 未使用 |
| GraphQL `providerCustomer` 与各提供者的 `*Customer` 字段；单数的 `provider_customer` 输入 | `app/graphql/types/customers/` | 由连接列表取代 |
| `RetryViesCheckJob` | `app/jobs/customers/retry_vies_check_job.rb` | 被带退避的 VIES 检查任务取代 |
| 在客户上赋值 `invoice_footer` | `app/services/customers/update_service.rb` | 并不存在这个列（§4） |
| `subscriptions.incompleted_at` 与只有一个 l 的 `cancelation_reason` | `db/structure.sql` | 与正确命名的列重复 |
| 过滤别名 `overriden` | `app/controllers/api/v1/subscriptions_controller.rb` | `overridden` 的拼写错误别名 |
| `already_billed_on_day_one?` 保护 | `app/services/subscriptions/organization_billing_service.rb` | 旧调度方式的变通；按订阅调度后不再需要 |
| `events.customer_id`、`events.subscription_id` | `db/structure.sql` | 事件引用外部 ID |
| `quantified_events` | `app/models/quantified_event.rb` | 由循环聚合取代 |
| 已删除的表 `events_enriched_expanded`、`events_aggregated` | `db/clickhouse_migrate/` | Lago 已删除 |
| 富化存储的迁移路径 | `app/services/events/stores/`（`enriched_store_*`） | 只用于迁移 |
| `Events::CreateBatchJob` | `app/jobs/events/create_batch_job.rb` | 未使用 |
| 删除计费指标时删除其事件 | `app/jobs/billable_metrics/delete_events_job.rb`（方法体为 `true`）、`app/services/events/delete_for_metric_service.rb`（没有调用方） | Lago 已不再删除事件（d76413e50）；事件由存储的保留策略保留 |
| `event.error` webhook | `config/webhook_event_types.yml`（`deprecated: true`） | 由 `events.errors` 取代 |
| 订阅刷新队列的版本参数 | `app/jobs/clock/consume_subscription_refreshed_queue_job.rb` | 兼容参数 |
| `LAGO_REDIS_CACHE_*` 设置、`LAGO_REDIS_STORE_TLS` 回退 | `config/` | 未使用或已废弃的配置 |
| 报价 `share_token` | `app/models/quote.rb`（`ignored_columns`） | 被忽略的列 |
| 发票类型 `add_on` | `app/models/invoice.rb` | 由 `one_off` 取代 |
| `applied_add_ons`、`fees.applied_add_on_id` | `app/models/applied_add_on.rb` | 由一次性发票取代 |
| `fees.negative_amount_cents`、`duplicated_in_advance` | `db/structure.sql` | 未使用 |
| `invoices.organization_sequential_id` | `app/models/invoice.rb` | 由计费主体序列取代 |
| 预览上下文 `refresh`、`draft` | `app/services/invoices/preview_service.rb`（`DEPRECATED_CONTEXTS`） | 已废弃的上下文 |
| `PrepaidCreditJob` 的默认参数 | `app/jobs/invoices/prepaid_credit_job.rb` | 兼容参数 |
| `UpdateAllEuTaxesJob` | `app/jobs/taxes/update_all_eu_taxes_job.rb` | 死任务 |
| 发票 PDF 模板 v1–v3 | `app/views/templates/invoices/v1.slim`–`v3.slim` | 只为旧发票保留 |
| `/download` 端点别名 | `config/routes.rb` | 别名 |
| `dunning_campaigns.applied_to_organization` 列 | `app/models/dunning_campaign.rb`（`ignored_columns`） | API 中的该标志读取并设置默认计费主体的催收活动（§1.35） |
| 集成资源类型 `sales_order_deprecated` | `app/models/integration_resource.rb` | Lago 已废弃 |
| `payments.invoice_id` | `db/structure.sql` | 由多态的应付对象取代 |
| `organizations.webhook_url` | `db/structure.sql` | 由 webhook 端点取代 |
| `webhooks.payload` 列 | `app/models/webhook.rb` | 载荷迁移到存储之前的行的回退 |
| `GET /api/v1/webhooks/public_key` | `app/controllers/api/v1/webhooks_controller.rb`（"Deprecated"） | 由 `json_public_key` 取代；Tanvo 没有需要公布的签名公钥（§3） |
| `SendWebhookJob` 的 `webhook_id` 参数 | `app/jobs/send_webhook_job.rb`（标记为临时） | 临时路径 |
| NetSuite `legacy_script` 设置 | `app/models/integrations/netsuite_integration.rb` | 旧的 RESTlet |
| 退款原因 `subscription_activation_expired` | `app/models/refund.rb` | 没有产生方；只有在实现激活退款时，Tanvo 才加入它 |
| 多连接回填任务 | `lib/tasks/multi_customer_connections.rake` | 只用于迁移 |

---

## 3. 以不同设计取代

### 3.1 适用于所有地方的约定

| Lago | Tanvo | 原因 |
| :--- | :--- | :--- |
| 管理界面用 GraphQL，客户端用 REST v1/v2 | 一套 Connect API，经 Vanguard 提供 REST（[架构 §4](architecture.zh-CN.md#4-契约与-apilibsprotoappsserver)） | 所有客户端一份契约；管理界面使用公开 API |
| 路径中以 `external_id` 或 `code` 寻址资源 | Tanvo ID，同时接受带 `external_id:` 与 `code:` 前缀的 ID | ID 稳定；客户标识符照样可用 |
| UUIDv4 主键 | UUIDv7 | 索引局部性 |
| 偏移分页 | 带 AIP-160 过滤的游标分页 | 并发写入下页面稳定 |
| 按请求内容生成键的 `idempotency_records` | `Idempotency-Key` 请求头，响应保留 24 小时 | 由客户端控制的标准幂等 |
| 基于 `deleted_at` 的软删除（Discard） | 生命周期状态与显式删除 | 查询中没有隐藏的行 |
| PaperTrail 版本 | 同一事务中的 `audit_events`，为 webhook 与活动日志提供数据 | 只有一份变更记录 |
| Rails 属性加密 | 使用配置或 KMS 密钥的 AES-256-GCM 信封加密 | 可轮换密钥，不与框架耦合 |
| 功能开关与高级版门槛 | 所有功能都在核心中提供；云服务在 `ee/` 中（§1.50） | 开放核心，没有许可证检查 |
| Lago 许可证服务器 | 离线的 Ed25519 许可证密钥，只用于 `ee/` | 不回连 |
| Segment 跟踪 | 移除 | 安装实例不发送遥测 |
| ActionCable GraphQL 订阅 | Connect 服务端流 | 经过认证的流 |

### 3.2 组件

| Lago | Tanvo | 原因 |
| :--- | :--- | :--- |
| 通过请求头续期的 JWT HS256 用户会话 | 以哈希存储的不透明会话令牌 | 会话可撤销 |
| 明文存储的 API 密钥值 | SHA-256 哈希，只显示一次，轮换时有重叠期 | 凭据卫生 |
| Google、Okta 与 Entra ID 各自独立的实现 | 一个带预设的 OIDC 身份提供者，外加通用 OIDC | 一条代码路径 |
| 来自 `MessageVerifier` 的客户门户令牌（12 小时） | 限定于一个客户的短期签名令牌 | 暴露面更窄 |
| JWT RS256（默认）或用组织密钥的 HMAC webhook 签名 | 每个端点各自密钥并支持轮换的 Standard Webhooks 签名 | 标准化，可用现成的库验证 |
| 在 ActiveStorage 中 gzip 压缩存放的 webhook 载荷 | 载荷与投递记录存放在一起 | 投递不需要对象存储 |
| Sidekiq 队列与 clockwork 时钟进程 | River 任务与由选举出的领导者运行的周期任务 | 事务内入队，无需额外进程 |
| 为计费与激活每小时扫描所有订阅 | 在到期时刻运行的按订阅任务，加每小时对账 | 工作量与到期的事项成正比 |
| `pg_partman` 分区 | 由 Tanvo 迁移与任务管理的原生分区 | 不需要扩展 |
| RisingWave 的 `usage_buckets_15m` | 由摄取消费者在 PostgreSQL 或 ClickHouse 中维护的 15 分钟用量桶 | 不需要额外系统 |
| 带 Debezium CDC 与 Badger 缓存的独立 events-processor 服务 | 服务器 `consumer` 角色中的摄取消费者，内存目录缓存通过变更通知刷新 | 组件更少 |
| 活动、API 与安全日志的 Kafka 管道 | `audit_events` 与写入 ClickHouse 或 PostgreSQL 的日志写入器 | 没有 Kafka 也能工作 |
| 以 Nango 作为集成中介 | `providers/*` 中直接调用供应商 API 的客户端 | 数据路径中没有第三方中介 |
| Gotenberg HTML 转 PDF | 作为子进程运行、输出 PDF/A-3b 的 Typst | 单一二进制，可归档的 PDF |
| 用 pdfcpu 嵌入 Factur-X XML | Typst 的 PDF/A-3 附件 | 一步渲染 |
| 两套目录（套餐与计费项，以及受开关控制的产品目录） | 一套同时覆盖两者的目录（§1.14） | 没有平行的模型 |
| v1（包含式，下一档从 +1 开始）与 v2（相邻）两种档位区间 | 只有相邻区间 | 一种约定 |
| 每个客户只有一个支付提供者，多连接尚未完成 | 每个客户多个连接，每个类别有默认连接 | 完成 Lago 的设计 |
| 只为 Stripe 与 Moneyhash 存储入站 webhook | 所有提供者的 webhook 都被存储并可重试 | 一致的可靠性 |
| Data API 与 Superset | `ee/analytics` | 在 Lago 中只在云端提供 |
| 基于 Mistral 的财务助手与 AI 对话 | 带 MCP 服务器的 `ee/assistant` | 在 Lago 中只在云端提供 |
| CS 管理与管理 REST | `tanvo admin` 命令；支持工具在 `ee/` 中 | 没有未经认证的本地端点 |
| `LAGO_CREATE_ORG` 初始化变量 | `tanvo admin bootstrap` | 显式命令 |
| 提供者回调的 OAuth 代理 | 回调由 `apps/server` 提供 | 自包含 |
| 基于 Redis 的 `throttling` gem | 按提供者的令牌桶 | 没有 Redis 也能工作 |
| API 密钥最近使用时间在每个请求中写入 Rails 缓存，每小时复制 | 每个 API 进程收集使用记录，每分钟记录到 `api_key_uses`，每小时按组织应用 | 没有 Redis 也能工作；按组织的工作受行级安全约束 |
| Yabeda 指标 | OpenTelemetry 与 `/metrics` | 标准化的监测 |

---

## 4. 不重现的上游缺陷

移植的测试编码修正后的行为，并引用 Lago 中的位置。

| Lago 的行为 | Lago 源码 | Tanvo 的行为 |
| :--- | :--- | :--- |
| 贷项通知单退款失败 webhook 的载荷写的是 `credit_note.refund_failure`，而事件键与目录写的是 `credit_note.provider_refund_failure`，导致端点过滤与载荷不一致 | `app/services/webhooks/credit_notes/payment_provider_refund_failure_service.rb`、`config/webhook_event_types.yml` | 载荷与过滤使用同一个事件名 `credit_note.provider_refund_failure` |
| Adyen webhook 基础服务从处理器已经拆开的条目中再读取 `notificationItems` | `app/services/payment_providers/adyen/webhooks/base_service.rb` | 每个通知条目只解析一次；败诉的拒付从其自身条目读取（providers/adyen） |
| Moneyhash 提供者引用了从未定义的 `live_prefix` | `app/models/payment_providers/moneyhash_provider.rb` | 没有未定义的设置；环境来自连接 |
| Moneyhash 付款 URL 的金额 `total_due_amount_cents.div(100).to_f` 截断了分 | `app/services/invoices/payments/moneyhash_service.rb` | 使用 `libs/money` 的精确小数金额 |
| 复用处理中的发票付款时匹配 `total_amount_cents`，而付款是按 `total_due_amount_cents` 创建的 | `app/services/invoices/payments/create_service.rb` | 按应付金额匹配 |
| Flutterwave 与 Moneyhash 按 `total_amount_cents` 扣款，而其他提供者按应付金额扣款 | `app/services/invoices/payments/flutterwave_service.rb`、`moneyhash_service.rb` | 所有提供者都按应付金额扣款 |
| 发票付款状态没有处理中状态；处理中的付款显示为 pending | `app/services/payment_requests/payments/create_service.rb`（代码注释） | 发票付款状态包含 `processing` |
| Okta 与 Entra ID 登录创建了没有角色的成员资格 | `app/services/auth/okta/`、`app/services/auth/entra_id/` | 成员资格总有角色；OIDC 注册分配配置的默认角色 |
| Google 登录错误码 `invite_email_mistmatch` | `app/services/auth/google_service.rb` | `invite_email_mismatch` |
| GraphQL ActionCable 频道接受未经认证的连接 | `app/channels/graphql_channel.rb` | 流要求会话或 API 密钥 |
| 分析解析器对高级版的检查不一致 | `app/graphql/resolvers/analytics/` | 不做此类检查；每个分析 RPC 一个权限 |
| 税的更新与删除 mutation 要求 `taxes:create` | `app/graphql/mutations/taxes/` | `taxes:update` 与 `taxes:delete` |
| 订阅时区 SQL 从客户直接回退到组织，跳过了计费主体 | `app/models/subscription.rb`（`*_in_timezone_sql`） | 客户 → 计费主体 → 组织，与 Ruby 代码中一致 |
| 计费项周期结束处留下一秒的空隙（`charges_from` 的 TODO） | `app/services/subscriptions/dates_service.rb` | 半开区间 `[from, to)`，既无空隙也无重叠 |
| 客户更新给不存在的列 `invoice_footer` 赋值 | `app/services/customers/update_service.rb` | 客户上不接受该字段；页脚是计费主体设置 |
| 限定于计费指标的优惠券在发票预览中不被应用（TODO） | `app/services/invoices/preview_service.rb` | 预览像真实发票一样应用所有优惠券 |
| 贷项通知单校验 `valid_invoice_type?` 与 `valid_global_amount?` 已定义但从未调用 | `app/services/credit_notes/validate_service.rb` | 两项检查都执行 |
| 入队失败时硬删除事件行，因为唯一索引不考虑 `deleted_at` | `app/services/events/create_service.rb` | 事件写入与后续工作在一个事务中提交，无需撤销 |
| Webhook 清理删除投递行，却不删除其存储的载荷对象 | `app/jobs/clock/webhooks_cleanup_job.rb` | 载荷随投递一起删除 |
| Cashfree 结账 URL 静默地什么也不返回 | `app/services/payment_provider_customers/cashfree_service.rb` | 不支持的操作返回明确的 `unimplemented` 错误 |
| Flutterwave 付款请求页面被提交到 API 根路径，且以 webhook 从不读取的 `lago_payment_request_id` 标记，因此其付款永远找不到付款请求 | `app/services/payment_requests/payments/flutterwave_service.rb`、`app/services/payment_providers/flutterwave/webhooks/charge_completed_service.rb` | 页面在 `/payments` 创建，元数据带有可付款对象的 ID 和类型，webhook 按与发票相同的方式读取 |
| 欧盟特殊地区的邮编模式在邮编的任意位置匹配，导致里斯本 `1940123` 得到马德拉的增值税率 | `app/services/customers/eu_auto_taxes_service.rb` | 模式只匹配规范化邮编的开头 |
| 集成服务抛出 `RequestLimitError(e)`，这是一个会抛出 NoMethodError 的方法调用，因此提供者的请求上限从不按请求上限重试 | `app/services/integrations/aggregator/invoices/create_service.rb:57` 及其他聚合服务（只有 `reconcile_service.rb:36` 使用 `.new`） | 请求上限为 `integration.ErrRequestLimit`，最多重试 100 次 |
| HubSpot 发票（及订阅）的对象与属性部署把各自不同的 `VERSION` 存进同一个 `*_properties_version` 设置，因此一方部署后另一方又会重新部署 | `app/services/integrations/hubspot/invoices/deploy_object_service.rb:9, 46`、`deploy_properties_service.rb:9, 24` 及订阅的对应服务 | 对象版本与属性版本分开，从 1 开始 |
| HubSpot 订阅对象的描述是字面量 `"string"` | `app/services/integrations/hubspot/subscriptions/deploy_object_service.rb:168` | 与发票对象一样描述 |
| 记录 Salesforce ID 时以外部 ID 查找资源，第二个 ID 会为同一发票再加一条资源，之后的查找取到其中任意一条 | `app/services/invoices/sync_salesforce_id_service.rb:16-22` | 每个集成、类型与同步对象只有一条资源；第二个 ID 保留第一个 |
| 其他提供者的集合映射带 NetSuite 税务字段时抛出 NoMethodError（500） | `app/services/integration_collection_mappings/create_service.rb:38-41`、`update_service.rb:22-25` | 该字段为 `value_not_allowed` |
