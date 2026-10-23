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
| 组织设置的读取与更新 API | `app/controllers/api/v1/organizations_controller.rb` | apps/server | 计划中 | |
| 组织 slug（用于文档编号与收据） | `app/models/organization.rb` | libs/core | 计划中 | |
| 审计日志保留期设置（`audit_logs_period`） | `app/models/organization.rb` | libs/core | 计划中 | |
| 每个客户的最大钱包数设置 | `app/models/organization.rb` | libs/billing | 逻辑已完成 | |
| 每个组织允许的认证方式（邮箱密码、Google、Okta、Entra ID） | `app/models/organization.rb`、`app/services/organizations/update_service.rb` | libs/auth | 计划中 | Okta 与 Entra ID 成为 OIDC 连接；新增通用 OIDC |
| 认证方式变更时通知管理员的邮件 | `app/mailers/organization_mailer.rb` | libs/mail | 逻辑已完成 | |
| 每个组织用于 webhook 签名的 HMAC 密钥 | `app/models/organization.rb` | libs/webhooks | 计划中 | 由每个端点的 Standard Webhooks 密钥取代（§3） |
| 首次启动时初始化组织 | `config/initializers/`（`LAGO_CREATE_ORG`、`LAGO_ORG_USER_*`） | apps/server | 计划中 | 以 `tanvo admin bootstrap` 命令代替环境变量 |
| 按组织选择事件存储（PostgreSQL 或 ClickHouse） | `app/models/organization.rb`（`clickhouse_events_store`） | libs/eventstore | 计划中 | 安装级设置，可按组织覆盖 |

### 1.3 计费主体

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 计费主体的增删改查（法律身份、地址、标志、货币、时区、语言） | `app/models/billing_entity.rb`、`app/services/billing_entities/` | libs/core | 计划中 | 以归档代替 Lago 不起作用的删除 |
| 每个组织的默认计费主体及按客户指定 | `app/models/billing_entity.rb`、`app/services/customers/` | libs/core | 计划中 | |
| 每个组织的计费主体数量（按套餐为 1、2 或不限） | `app/models/organization.rb`（`MULTI_ENTITIES_MAX`） | libs/core | 计划中 | 核心中不设上限 |
| 按客户或按计费主体编号文档 | `app/services/billing_entities/change_invoice_numbering_service.rb` | libs/billing | 逻辑已完成 | |
| 文档编号前缀 | `app/models/billing_entity.rb` | libs/billing | 逻辑已完成 | |
| 发票宽限期、付款条款、发票页脚 | `app/models/billing_entity.rb` | libs/billing | 逻辑已完成 | |
| 开票日期锚点与调整设置 | `app/models/billing_entity.rb` | libs/billing | 逻辑已完成 | |
| 零金额发票定稿设置 | `app/models/billing_entity.rb` | libs/billing | 逻辑已完成 | |
| 欧盟税务管理开关与自动欧盟税 | `app/services/billing_entities/`、`app/services/taxes/auto_generate_service.rb` | libs/tax | 计划中 | |
| 在支持的国家（法国、德国）启用电子发票 | `app/models/billing_entity.rb` | libs/documents | 逻辑已完成 | |
| 邮件设置（哪些文档通过邮件发送）与发件地址 | `app/models/billing_entity.rb`（`email_settings`） | libs/mail | 进行中 | |
| 应用于计费主体的税（应用、移除、管理） | `app/services/billing_entities/taxes/` | libs/tax | 计划中 | |
| 每个计费主体所应用的催收活动 | `app/services/billing_entities/update_applied_dunning_campaign_service.rb` | libs/billing | 计划中 | |
| 计费主体 API | `app/controllers/api/v1/billing_entities_controller.rb` | apps/server | 计划中 | |

### 1.4 用户、成员资格、角色与邀请

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 跨组织的用户与成员资格（有效、已撤销） | `app/models/user.rb`、`app/models/membership.rb` | libs/auth | 逻辑已完成 | |
| 多角色成员资格：权限为各角色权限的并集，至少一个角色 | `app/models/membership.rb`、`app/models/membership_role.rb` | libs/core、libs/store、libs/auth | 逻辑已完成 | `membership_roles` 表，行软删除；`Membership.RoleIDs`、`access.AnyGrants` |
| 预定义角色 admin、finance、manager | `app/models/role.rb`、`config/permissions.yml` | libs/auth | 逻辑已完成 | |
| 带权限集的自定义角色 | `app/services/roles/` | libs/auth | 计划中 | |
| 删除自定义角色：软删除，拒绝删除预定义角色和有效成员使用中的角色 | `app/services/roles/destroy_service.rb`、`app/models/role.rb` | libs/core、libs/store、libs/auth | 逻辑已完成 | `auth.Roles.Delete`，记录 `role.deleted` 安全日志；已删除的角色不再出现在查找、列表与成员中，其代码可以复用 |
| 权限目录（每种资源与操作） | `config/permissions.yml` | libs/auth | 计划中 | 每个 RPC 声明 `resource:action` 形式的权限字符串 |
| 成员资格规则：不能撤销自己的成员资格、不能移除最后一个管理员、只有管理员能授予管理员 | `app/services/memberships/` | libs/auth | 逻辑已完成 | `auth.Memberships`（UpdateRoles、Revoke），记录 `user.role_edited` 与 `user.deleted` 安全日志；撤销已撤销的成员会成功且不改变成员 |
| 带令牌与角色的邀请，支持接受、撤销、更新 | `app/models/invite.rb`、`app/services/invites/` | libs/auth | 逻辑已完成 | 每个邀请可带多个角色（`role_ids`），以 ID 而非代码存储 |
| 通过邮件重置密码（30 分钟令牌） | `app/services/password_resets/` | libs/auth | 逻辑已完成 | |
| 自托管安装的注册开关 | `LAGO_DISABLE_SIGNUP`、`app/services/users_service.rb` | apps/server | 计划中 | 配置项 |

### 1.5 认证与会话

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 邮箱密码登录与注册 | `app/services/users_service.rb`、`app/graphql/mutations/` | libs/auth | 逻辑已完成 | argon2id 哈希；以不透明会话代替 JWT（§3） |
| Google 登录、注册与接受邀请 | `app/services/auth/google_service.rb` | libs/auth | 逻辑已完成 | 经由 OIDC 身份提供者 |
| Okta 登录（authorize、token、userinfo、state 校验） | `app/services/auth/okta/` | libs/auth | 逻辑已完成 | OIDC 连接；一次性 state 存储在 PostgreSQL（`oidc_states`，10 分钟而非 90 秒） |
| 带租户与附加域名的 Entra ID 登录 | `app/services/auth/entra_id/` | libs/auth | 逻辑已完成 | OIDC 连接 |
| 通用 OIDC 提供者 | — | libs/auth | 逻辑已完成 | Tanvo 新增 |
| Okta 与 Entra ID 集成配置（客户端、域名、主机） | `app/models/integrations/okta_integration.rb`、`entra_id_integration.rb` | libs/core、libs/auth | 逻辑已完成 | `integration.AuthSettings`（另有通用 OIDC 的 `issuer` 和必填的 `default_role_id`）；域名跨组织且不区分大小写地路由（`IntegrationRepository.ListByDomain`、`auth.IntegrationConnections`） |
| 拒绝移除最后一种已启用的登录方式 | `app/services/integrations/okta/destroy_service.rb` | libs/auth | 逻辑已完成 | |
| 已知设备与新设备安全日志记录 | `app/models/user_device.rb`、`app/services/user_devices/` | libs/core、libs/store、libs/auth | 逻辑已完成 | `access.UserDevice`；user agent 解析（`Utils::DeviceInfo`）属于 apps/server |
| GoCardless 连接的 OAuth 回调 | `app/services/payment_providers/gocardless_service.rb`（`LAGO_OAUTH_PROXY_URL`） | providers/gocardless | 计划中 | 回调由 `apps/server` 提供；不经代理 |

### 1.6 API 密钥与 API 约定

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 每个组织多个具名 API 密钥 | `app/models/api_key.rb`、`app/services/api_keys/` | libs/auth | 逻辑已完成 | 以 SHA-256 哈希存储，只显示一次 |
| 按资源（读、写）设置每个密钥的权限 | `app/models/api_key.rb`（`RESOURCES`） | libs/auth | 逻辑已完成 | 与角色使用相同的权限字符串 |
| 密钥过期与轮换 | `app/services/api_keys/rotate_service.rb` | libs/auth | 逻辑已完成 | 轮换时旧密钥在一个重叠窗口内仍然有效 |
| 拒绝删除最后一个不过期的密钥 | `app/services/api_keys/destroy_service.rb` | libs/auth | 逻辑已完成 | |
| 最近使用时间跟踪 | `app/jobs/clock/api_keys/track_usage_job.rb` | libs/auth | 已完成 | API 角色在内存中收集使用记录，每分钟记录到 `api_key_uses`（lago 在每个请求中写入 Rails 缓存，`app/controllers/api/base_controller.rb:59`）；每小时 :15 的 clock 任务为每个有使用记录的组织入队一个任务，把记录移到 `last_used_at`（从不回退） |
| 密钥创建、轮换或删除时发送邮件 | `app/mailers/api_key_mailer.rb` | libs/mail | 逻辑已完成 | 由 `mail.send` 任务（apps/server）发送给组织管理员 |
| 写请求的幂等性 | `app/models/idempotency_record.rb`、`app/services/idempotency_records/` | apps/server | 计划中 | `Idempotency-Key` 请求头；响应保留 24 小时 |
| 健康与就绪端点 | `config/routes.rb`（`/health`、`/ready`） | apps/server | 已完成 | `/healthz`、`/readyz`；就绪检查 PostgreSQL、事件存储，以及使用时的 ClickHouse 与 Kafka |
| Prometheus 指标端点 | `config/initializers/yabeda.rb` | apps/server | 已完成 | OpenTelemetry（默认 OTLP）与可选的 `/metrics` 读取器（`TANVO_METRICS_ENDPOINT`） |
| 当前版本端点 | `app/graphql/resolvers/version_resolver.rb` | apps/server | 计划中 | |
| 对第三方 API 调用的限流 | `config/initializers/throttling.rb` | providers/* | 计划中 | 按提供者设定上限（HubSpot 110 次/10 秒，Xero 60 次/分钟与 5000 次/天，NetSuite 10 次/秒，Anrok 9 次/秒，Avalara 10 次/秒） |

### 1.7 客户

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 按外部 ID 创建或更新客户（upsert） | `app/services/customers/upsert_from_api_service.rb` | libs/core | 计划中 | 分别提供创建与更新 RPC，另有一个 upsert RPC；用字段存在性区分未提供与 null |
| 客户类型（公司、个人）与账户类型（客户、合作伙伴） | `app/models/customer.rb` | libs/core | 计划中 | |
| 合作伙伴账户与自开票发票 | `app/models/customer.rb`、`app/services/invoices/` | libs/billing | 逻辑已完成 | |
| 组织内的顺序编号与 slug | `app/models/customer.rb` | libs/core | 计划中 | |
| 账单地址与收货地址 | `app/models/customer.rb` | libs/core | 计划中 | |
| 税号、法定名称与编号 | `app/models/customer.rb` | libs/core | 计划中 | |
| 默认货币：新计费对象的偏好，而不是约束 | `app/services/customers/update_currency_service.rb` | libs/core | 逻辑已完成 | 为空时由第一个计费对象设置；显式更新可随时修改 |
| 每个客户多种货币：订阅取其套餐的货币；钱包、已应用的优惠券和一次性发票取各自的货币；发票按货币拆分 | `app/services/subscriptions/organization_billing_service.rb`（`group_by_currency`）、`app/services/wallets/create_service.rb`、`app/services/applied_coupons/create_service.rb`、`app/services/invoices/create_one_off_service.rb` | libs/billing | 逻辑已完成 | 钱包和固定金额优惠券只抵扣同货币的发票；催收按货币计数尝试次数（§1.35） |
| 时区，回退到计费主体与组织 | `app/models/customer.rb` | libs/period | 计划中 | 各处一律按客户 → 计费主体 → 组织回退（§4） |
| 宽限期、付款条款、文档语言及其回退 | `app/models/customer.rb` | libs/billing | 逻辑已完成 | |
| 零金额发票定稿：继承、跳过、定稿 | `app/models/customer.rb` | libs/billing | 逻辑已完成 | |
| 开票日期锚点与调整的覆盖 | `app/models/customer.rb` | libs/billing | 逻辑已完成 | |
| 客户元数据（最多 5 个，键 20 字符，值 100 字符，可显示在发票上） | `app/models/metadata/customer_metadata.rb` | libs/core | 计划中 | |
| 有订阅或已开票后的可编辑规则 | `app/models/customer.rb`（`editable?`） | libs/core | 计划中 | |
| 客户列表过滤（搜索词、元数据、国家、州、邮编、货币、客户与账户类型、计费主体、是否有税号） | `app/queries/customers_query.rb` | libs/store | 逻辑已完成 | 两种存储均已实现查询，移植的规格用例在 porttest 中；API 尚未实现 |
| 删除客户并终止其关联 | `app/services/customers/destroy_service.rb`、`app/jobs/customers/terminate_relations_job.rb` | libs/billing | 计划中 | 终止订阅、终止钱包、定稿草稿发票 |
| 每个客户的支付提供者连接（提供者客户 ID、与提供者同步） | `app/services/payment_provider_customers/create_or_update_batch_service.rb` | libs/core | 计划中 | 每个客户可有多个连接并有一个默认连接（§3） |
| 集成客户（会计、CRM、税务）的批量创建与更新 | `app/services/integration_customers/create_or_update_batch_service.rb` | libs/core | 计划中 | |
| 用于收集支付方式的结账 URL | `app/services/customers/generate_checkout_url_service.rb` | libs/billing | 计划中 | |
| 客户门户 URL | `app/services/customer_portal/generate_url_service.rb` | libs/auth | 逻辑已完成 | |
| 串行化每个客户操作的锁（预付额度、支付方式、贷项通知单、优惠券、计费调度） | `app/services/customers/lock_service.rb` | libs/store | 逻辑已完成 | PostgreSQL 咨询锁 |
| 按客户选择或跳过发票自定义段落 | `app/services/customers/manage_invoice_custom_sections_service.rb` | libs/billing | 计划中 | |
| 排除在催收之外与所应用的催收活动 | `app/models/customer.rb` | libs/billing | 计划中 | |
| x402 代理地址 | `app/models/customer.rb`（`x402_agent_address`） | libs/core | 计划中 | 见 §1.36 |

### 1.8 计费指标

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 计费指标的增删改查（名称、代码、描述、字段名） | `app/models/billable_metric.rb`、`app/services/billable_metrics/` | libs/core | 计划中 | |
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
| 表达式求值 API（用样例事件测试表达式） | `app/services/billable_metrics/evaluate_expression_service.rb` | libs/expression | 逻辑已完成 | |
| 指标被套餐使用后的字段锁定 | `app/services/billable_metrics/update_service.rb` | libs/core | 计划中 | |
| 删除指标：移除计费项与告警、刷新草稿 | `app/services/billable_metrics/destroy_service.rb` | libs/billing | 计划中 | 事件保留（§2）；费率卡的产品过滤器使用该指标时被拒绝（`referenced_by_product_filter`） |
| 指标过滤器（键及允许的值）与批量更新 | `app/services/billable_metric_filters/create_or_update_batch_service.rb` | libs/core | 计划中 | 移除一个值时同步裁剪依赖它的计费项过滤器；费率卡的产品过滤器使用该取值或键时被拒绝（`referenced_by_product_filter`），与 Lago 一样阻止而不是级联 |

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
| 套餐的增删改查（名称、代码、描述、发票显示名称、元数据） | `app/models/plan.rb`、`app/services/plans/` | libs/core | 计划中 | |
| 间隔：每周、每月、每季度、每半年、每年 | `app/models/plan.rb` | libs/period | 逻辑已完成 | |
| 订阅费金额与货币 | `app/models/plan.rb` | libs/billing | 逻辑已完成 | |
| 订阅费预付或后付 | `app/models/plan.rb`（`pay_in_advance`） | libs/billing | 逻辑已完成 | |
| 以天为单位的试用期 | `app/models/plan.rb` | libs/period | 逻辑已完成 | |
| 年度、半年度与季度套餐按月计费计费项 | `app/models/plan.rb`（`bill_charges_monthly`） | libs/period | 逻辑已完成 | |
| 按月计费固定计费项 | `app/models/plan.rb`（`bill_fixed_charges_monthly`） | libs/period | 逻辑已完成 | |
| 应用于套餐的税 | `app/services/plans/apply_taxes_service.rb` | libs/tax | 计划中 | |
| 用于区分升级与降级的年度金额 | `app/models/plan.rb`（`yearly_amount_cents`） | libs/billing | 逻辑已完成 | |
| 按订阅覆盖套餐（子套餐） | `app/services/plans/override_service.rb` | libs/billing | 计划中 | |
| 套餐有订阅后的字段锁定 | `app/services/plans/update_service.rb` | libs/core | 计划中 | |
| 把套餐变更级联到被覆盖的子套餐 | `app/services/plans/update_service.rb`（`cascade_updates`）、`app/jobs/plans/` | libs/billing | 计划中 | |
| 更新现有订阅的金额 | `app/jobs/plans/update_amount_job.rb` | libs/billing | 计划中 | |
| 删除套餐：先准备（待删除），再删除并终止订阅 | `app/services/plans/prepare_destroy_service.rb`、`app/services/plans/destroy_service.rb` | libs/billing | 逻辑已完成 | `plans.Service`（PrepareDestroy、Destroy、`billing.destroy_plan` 任务）：所有已删除的套餐都以 `deleted` 状态保留，其代码可再次使用；权益被软删除；待生效订阅被取消且不记录原因（领域模型 §7.2）。API 端点属于 API 层 |
| 套餐 webhook `plan.created`、`plan.updated`、`plan.deleted` | `app/services/webhooks/plans/` | libs/webhooks | 计划中 | |

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
| 可以指向某个钱包的计费项 | `app/models/charge.rb`（`accepts_target_wallet`） | libs/billing | 计划中 | |
| 计费项代码生成 | `app/services/charges/` | libs/core | 计划中 | |
| 定价分组键（按键聚合） | `app/models/charge.rb`（`pricing_group_keys`） | libs/pricing | 逻辑已完成 | |
| 展示分组键（最多两个，只用于展示） | `app/models/charge.rb`（`presentation_group_keys`） | libs/billing | 计划中 | |
| 计费项的发票显示名称 | `app/models/charge.rb` | libs/billing | 逻辑已完成 | |
| 每个计费项的税 | `app/services/charges/apply_taxes_service.rb` | libs/tax | 计划中 | |
| 嵌套在套餐与订阅下的计费项 API | `app/controllers/api/v1/plans/charges_controller.rb`、`app/controllers/api/v1/subscriptions/charges_controller.rb` | apps/server | 计划中 | |

### 1.12 计费项过滤器

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 带有自身属性的计费项过滤器 | `app/models/charge_filter.rb`、`app/services/charge_filters/` | libs/metering | 逻辑已完成 | |
| 全部取值通配符（`__ALL_FILTER_VALUES__`） | `app/models/charge_filter_value.rb` | libs/metering | 逻辑已完成 | |
| 过滤器优先级（最具体的过滤器胜出） | `app/services/events/billing_period_filters/matching_and_ignored_service.rb` | libs/metering | 逻辑已完成 | |
| 事件与过滤器匹配，未匹配事件归入默认桶 | `app/services/charge_filters/event_matching_service.rb` | libs/metering | 逻辑已完成 | |
| 为计费周期查询最小化被忽略的过滤器 | `app/services/charge_filters/`（`MinimizeIgnoredFilters`） | libs/metering | 逻辑已完成 | |
| 过滤器代码（slug 加哈希） | `app/models/charge_filter.rb` | libs/core | 计划中 | |
| 把过滤器变更级联到子套餐 | `app/services/charge_filters/create_or_update_batch_service.rb` | libs/billing | 计划中 | 只保留带代码的路径（§2） |

### 1.13 固定计费项与附加项

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 附加项的增删改查（金额、货币、发票显示名称、税） | `app/models/add_on.rb`、`app/services/add_ons/` | libs/core | 计划中 | |
| 套餐上的固定计费项（基于附加项，带单位数） | `app/models/fixed_charge.rb`、`app/services/fixed_charges/` | libs/billing | 逻辑已完成 | |
| 固定计费项的计费模型 standard、graduated、volume | `app/services/fixed_charges/` | libs/pricing | 逻辑已完成 | |
| 预付固定计费项 | `app/services/invoices/create_pay_in_advance_fixed_charges_service.rb` | libs/billing | 逻辑已完成 | |
| 按比例计算的固定计费项 | `app/models/fixed_charge.rb`（`prorated`） | libs/period | 逻辑已完成 | |
| 按订阅覆盖单位数 | `app/services/fixed_charges/override_service.rb` | libs/billing | 计划中 | |
| 单位数变更立即生效或在下一周期生效 | `app/models/fixed_charge.rb`（`apply_units_immediately`） | libs/billing | 逻辑已完成 | |
| 记录单位数随时间变化的固定计费项事件 | `app/models/fixed_charge_event.rb`、`app/services/fixed_charges/emit_events_service.rb` | libs/billing | 计划中 | |
| 把固定计费项变更级联到子套餐 | `app/jobs/fixed_charges/` | libs/billing | 计划中 | |
| 每个固定计费项的税 | `app/services/fixed_charges/` | libs/tax | 计划中 | |
| 嵌套在套餐与订阅下的固定计费项 API | `app/controllers/api/v1/plans/fixed_charges_controller.rb` | apps/server | 计划中 | |

### 1.14 产品目录（v2）

Lago 有两套目录：上面的套餐/计费项目录，以及受 `product_catalog` 开关控制的较新的产品目录。Tanvo 只实现一套目录，其模型同时覆盖两者（§3）：本节与 §1.10–§1.13 的每一行都在范围内。

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 产品类别 | `app/models/product_category.rb` | libs/core | 计划中 | |
| 产品（计量型、固定型） | `app/models/product.rb` | libs/core | 计划中 | |
| 产品过滤器与过滤器取值 | `app/models/product_filter.rb` | libs/pricing | 计划中 | |
| 费率卡（计费时机、按比例计算、发票展示、重新汇总、定价单位、锁定） | `app/models/rate_card.rb` | libs/core | 计划中 | |
| 时间线上的费率卡费率（`effective_from`） | `app/models/rate_card_rate.rb`、`app/services/rate_card_rates/create_service.rb` | libs/pricing | 计划中 | 有效费率之后只能追加；后付的 `effective_from` 为本地午夜，每天一个费率；拒绝回溯的费率（组织时区） |
| 费率模型及其与产品类型的兼容性 | `app/services/rate_card_rates/`（`ModelCompatibility`） | libs/pricing | 计划中 | |
| 每个费率的计费间隔数与单位 | `app/models/rate_card_rate.rb` | libs/period | 计划中 | |
| 区间规范化 | `app/services/rate_card_rates/`（`NormalizeRanges`） | libs/pricing | 计划中 | |
| 按比例累进费率按事件填充阶梯（允许小数阶梯边界；移除先清空最高阶梯） | `app/services/charge_models/prorated_adjacent_graduated_service.rb` | libs/pricing | 计划中 | |
| 目录套餐与套餐费率卡 | `app/models/catalog_plan.rb`、`app/models/plan_rate_card.rb` | libs/core | 计划中 | |
| 费率阶段（爬坡）及其不变量 | `app/models/rate_phase.rb` | libs/billing | 计划中 | |
| 费率覆盖 | `app/models/rate_override.rb` | libs/billing | 计划中 | |
| 目录代码格式 | `app/models/concerns/`（`CatalogCodeFormat`） | libs/core | 计划中 | |
| 带游标分页的目录 API | `app/controllers/api/v2/` | apps/server | 计划中 | 各处一律游标分页；没有 beta 请求头 |

### 1.15 定价单位

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 定价单位（名称、代码、最多 3 个字符的简称、描述） | `app/models/pricing_unit.rb`、`app/services/pricing_units/` | libs/core | 计划中 | 公开 API，而不只供管理界面使用 |
| 以定价单位计价并带换算率的计费项 | `app/models/applied_pricing_unit.rb` | libs/pricing | 逻辑已完成 | |
| 在费用上记录定价单位用量 | `app/models/pricing_unit_usage.rb` | libs/billing | 逻辑已完成 | |

### 1.16 权益

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 特性（代码、名称、描述） | `app/models/entitlement/feature.rb`、`app/services/entitlement/` | libs/core | 计划中 | 仓储按 `feature_destroy_service.rb:22-27` 的做法软删除特性（`deleted_at`） |
| 整数、字符串、布尔、选择类型的特权 | `app/models/entitlement/privilege.rb` | libs/core | 计划中 | 仓储在删除特性时、或特性更新不再列出某特权时软删除该特权（`deleted_at`，`feature_update_service.rb:99-111`） |
| 套餐权益与特权取值 | `app/services/entitlement/plan_entitlements_update_service.rb` | libs/core | 计划中 | |
| 订阅的权益覆盖与移除 | `app/services/entitlement/subscription_entitlements_update_service.rb` | libs/core | 计划中 | |
| 订阅解析后的权益 | `app/services/entitlement/` | libs/core | 计划中 | |
| 特性 webhook `feature.created`、`feature.updated`、`feature.deleted` | `app/services/webhooks/features/` | libs/webhooks | 计划中 | |
| 特性与权益 API | `app/controllers/api/v1/features_controller.rb`、`app/controllers/api/v1/plans/entitlements_controller.rb`、`app/controllers/api/v1/subscriptions/entitlements_controller.rb` | apps/server | 计划中 | |

### 1.17 事件摄取

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 单个事件摄取 | `app/services/events/create_service.rb` | libs/ingest | 已完成 | `EventService.IngestEvent`、`POST /v1/events`；apps/server 中有端到端测试 |
| 批量摄取（最多 100 个事件） | `app/services/events/create_batch_service.rb` | libs/ingest | 逻辑已完成 | 由 `EventService.IngestEvents` 提供；尚无端到端测试 |
| 事件时间戳解析（秒、带小数的秒，默认取当前时间） | `app/services/events/common_factory.rb` | libs/ingest | 逻辑已完成 | |
| 事件字段：交易 ID、外部订阅 ID、代码、属性、精确总金额 | `app/models/event.rb` | libs/ingest | 逻辑已完成 | JSON 属性中的数字保持精确，小数亦然（lago 的 JSON 解析使整数精确、小数转为浮点数）；二进制 protobuf 与 gRPC 客户端发送 double，精确值以字符串发送（[架构 §4.2](architecture.zh-CN.md#42-经由-vanguard-的-rest)） |
| 摄取和事件过滤中 `external_contract_id` 作为 `external_subscription_id` 的别名 | `app/controllers/api/v1/events_controller.rb` | libs/ingest | 逻辑已完成 | 两者都发送时以显式的 `external_subscription_id` 为准 |
| 按组织、外部订阅 ID 与交易 ID 去重 | `app/services/events/create_service.rb`、`app/services/events/validate_creation_service.rb`、`db/structure.sql`（`index_unique_transaction_id`） | libs/ingest、libs/store、libs/eventstore | 逻辑已完成 | 所有存储使用同一个键；Lago 的 ClickHouse 存储在查询时去重，并且还以代码和时间戳为键，而 Tanvo 把在另一时间戳的重发视为重复 |
| 事件上的请求元数据（user agent、IP） | `app/services/events/create_service.rb` | libs/ingest | 进行中 | |
| 事件校验（指标代码、订阅、数值字段） | `app/services/events/validate_creation_service.rb` | libs/ingest | 进行中 | |
| 摄取后的事件校验与 `events.errors` webhook | `app/services/events/post_validation_service.rb` | libs/ingest | 计划中 | 能同步返回的校验错误也同步返回 |
| 获取单个事件与列出事件 | `app/controllers/api/v1/events_controller.rb` | apps/server | 计划中 | |
| 列出富化事件 | `app/controllers/api/v1/events_controller.rb`（`events_enriched`） | apps/server | 计划中 | |
| 预付事件的费用估算 | `app/services/fees/estimate_pay_in_advance_service.rb` | libs/billing | 逻辑已完成 | |
| 即时费用估算与批量即时估算 | `app/services/fees/estimate_instant/` | libs/billing | 计划中 | |
| 富化：订阅、指标、表达式、聚合值、分组键、精确金额；查找计费项只为路由预付事件 | `app/services/events/enrich_service.rb`、events-processor `processors/` | libs/ingest | 逻辑已完成 | 富化行上没有计费项或过滤器；过滤器在查询或聚合到桶时根据原始属性匹配 |
| 带操作类型（增加/移除）的富化事件 | `app/models/enriched_event.rb` | libs/ingest | 逻辑已完成 | |
| 目录变更后重新富化 | `app/services/events/re_enrich_all_service.rb` | libs/ingest | 计划中 | |
| 后处理：用量监控、钱包刷新、目标钱包、预付计费项 | `app/services/events/post_process_service.rb` | libs/ingest | 进行中 | |
| 指向特定钱包的事件 | `app/services/events/`（`target_wallet_code`） | libs/billing | 计划中 | |
| 由事件创建预付计费项费用，按来源去重 | `app/services/fees/create_pay_in_advance_service.rb`、`app/services/events/pay_in_advance_service.rb` | libs/billing | 逻辑已完成 | |
| 带消费者的 Kafka 摄取模式 | events-processor `processors/` | libs/ingest | 逻辑已完成 | 内置于 `consumer` 角色：富化与 ClickHouse 写入消费者组已装配；尚无端到端测试 |
| 无法处理事件的死信主题 | events-processor `processors/dlq` | libs/ingest | 逻辑已完成 | |
| HTTP、SQS、Kinesis 来源连接器 | events-processor 连接器 | libs/ingest | 计划中 | 也支持 Kafka |
| 消费者中的目录缓存 | events-processor（Badger 缓存、Debezium） | libs/ingest | 逻辑已完成 | 以变更通知代替 CDC（§3） |

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
| 生命周期用量刷新 | `app/jobs/clock/refresh_lifetime_usages_job.rb` | libs/billing | 逻辑已完成 | |
| 按指标细分的生命周期用量 | `app/services/lifetime_usages/` | libs/billing | 计划中 | |
| 带差值与历史回填的每日用量 | `app/models/daily_usage.rb`、`app/services/daily_usages/` | libs/billing | 计划中 | |
| 订阅活动处理（哪些订阅发生了变化） | `app/services/usage_monitoring/process_subscription_activity_service.rb` | libs/billing | 计划中 | |

### 1.20 订阅

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 状态 pending、active、terminated、canceled、incomplete | `app/models/subscription.rb` | libs/core | 计划中 | |
| 计费时间：自然周期或周年 | `app/models/subscription.rb` | libs/period | 逻辑已完成 | |
| 订阅开始日期（过去或未来）与结束日期 | `app/services/subscriptions/create_service.rb` | libs/billing | 逻辑已完成 | |
| 订阅名称与外部 ID | `app/models/subscription.rb` | libs/core | 计划中 | |
| 创建订阅时覆盖套餐 | `app/services/subscriptions/create_service.rb`、`app/services/plans/override_service.rb` | libs/billing | 计划中 | |
| 每个订阅的计费项、过滤器与固定计费项端点 | `app/controllers/api/v1/subscriptions/` | apps/server | 计划中 | |
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
| 跳过每日用量计算 | `app/models/subscription.rb` | libs/billing | 计划中 | |
| 激活规则：要求付款，可设超时 | `app/models/subscription/activation_rule.rb`、`app/services/subscriptions/activation_rules/` | libs/billing | 逻辑已完成 | |
| 激活规则状态（inactive、pending、satisfied、not applicable、declined、failed、expired） | `app/models/subscription/activation_rule.rb` | libs/core | 计划中 | |
| 激活前要求发票已付的门槛 | `app/services/subscriptions/activate_service.rb` | libs/billing | 逻辑已完成 | |
| 创建订阅时预授权付款（Stripe） | `app/services/payment_providers/stripe/payments/authorize_service.rb` | providers/stripe | 计划中 | |
| 在开始日期激活 pending 订阅 | `app/jobs/clock/activate_subscriptions_job.rb` | libs/billing | 逻辑已完成 | 按订阅调度（§3） |
| 在结束日期终止 | `app/jobs/clock/terminate_ended_subscriptions_job.rb` | libs/billing | 逻辑已完成 | |
| incomplete 订阅过期 | `app/jobs/clock/expire_incomplete_subscriptions_job.rb` | libs/billing | 计划中 | |
| 结束日期前的终止提醒（默认 15 天与 45 天） | `app/jobs/clock/subscriptions_to_be_terminated_job.rb` | libs/billing | 计划中 | |
| 跨升级保留试用期（保留最初的开始日期） | `app/models/subscription.rb`（`trial_end_date`） | libs/period | 逻辑已完成 | |
| 试用结束检测与免费试用计费 | `app/jobs/clock/free_trial_subscriptions_biller_job.rb` | libs/billing | 逻辑已完成 | |
| 周期计费选择（今天哪些订阅需要计费） | `app/services/subscriptions/organization_billing_service.rb` | libs/billing | 逻辑已完成 | 按订阅调度的任务加对账（§3） |
| 同一张发票上一起计费的订阅分组 | `app/services/subscriptions/organization_billing_service.rb` | libs/billing | 逻辑已完成 | |
| 订阅列表过滤（状态、套餐、客户、是否被覆盖） | `app/queries/subscriptions_query.rb` | libs/store | 逻辑已完成 | 两种存储均已实现查询，移植的规格用例在 porttest 中；套餐过滤排除已删除的套餐；最近排序依次为 subscription_at、created_at（均降序），再按 id 升序；API 尚未实现 |
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
| 状态为 pending、active、terminated、canceled 的合同 | `app/models/contract.rb`、`app/services/contracts/` | libs/core | 计划中 | |
| 合同费率卡 | `app/models/contract_rate_card.rb`、`app/services/contract_rate_cards/` | libs/billing | 计划中 | |
| 合同计费数学：间隔、日历、天数、周期、阶段、片段、周期遍历器、计划表 | `app/services/billing/` | libs/period | 计划中 | |
| 合同激活 | `app/jobs/clock/activate_contracts_job.rb` | libs/billing | 计划中 | |
| 创建计费片段 | `app/services/billing_segments/`、`app/jobs/clock/create_billing_segments_job.rb` | libs/billing | 计划中 | |
| 处理计费片段并排除重叠 | `app/jobs/clock/process_billing_segments_job.rb` | libs/billing | 计划中 | |
| 合同 API | `app/controllers/api/v2/contracts_controller.rb` | apps/server | 计划中 | |
| 预付合同在创建时计费 | `app/services/contracts/create_service.rb`、`app/jobs/billing_segments/schedule_job.rb` | libs/billing | 计划中 | 订阅的计费任务随订阅一起入队（[领域模型 §21.4](domain.zh-CN.md#214-合同产品目录)） |
| 终止有效合同、取消待生效合同，绝不删除 | `app/services/contracts/terminate_service.rb` | libs/billing | 计划中 | 订阅状态转换（领域模型 §9.2） |
| 有效合同的编辑白名单（`contract_locked`） | `app/services/contracts/update_service.rb` | libs/billing | 计划中 | 适用于由费率卡定价的订阅 |
| 合同结束日期是其费率卡的唯一结束 | `app/models/contract_rate_card.rb` | libs/billing | 计划中 | 订阅的 `ending_at` |
| 生效计费锚点日期 | `app/models/contract.rb` | libs/core | 计划中 | |
| 费率阶段复制到合同费率卡上，待生效时可编辑 | `app/services/contracts/materialize_rate_cards_service.rb`、`app/services/rate_phases/` | libs/billing | 计划中 | |
| 已应用费率卡的更新：单位数和锚点，锚点会重置计费时钟 | `app/services/contract_rate_cards/update_service.rb`、`app/services/contract_rate_cards/reset_billing_clock_service.rb` | libs/billing | 计划中 | 重新计算 `next_billing_at` |
| 合同的发票自定义段落 | `app/services/invoice_custom_sections/` | libs/billing | 计划中 | |
| 可恢复的费率卡计划表（从最新持久化的周期恢复，片段去重） | `app/services/billing/rate_cards/cycle_walker.rb`、`app/services/billing/rate_cards/build_schedule_service.rb` | libs/period | 计划中 | |
| 重试时按边界标识复用费用（发票、费率卡、微秒精度的 UTC 边界） | `app/services/fees/charge_service.rb`、`app/models/billing_period_boundaries.rb` | libs/billing | 计划中 | |
| 预付计量片段按事件定价 | `app/services/events/pay_in_advance_billing_segment_resolver.rb` | libs/billing | 计划中 | 绝不在计费时钟触发时定价 |

### 1.23 发票

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 发票类型 subscription | `app/services/invoices/subscription_service.rb` | libs/billing | 逻辑已完成 | |
| 发票类型 credit（钱包充值） | `app/services/invoices/paid_credit_service.rb` | libs/billing | 逻辑已完成 | 支付后的入账由 `wallets.Service.HandleCreditInvoicePayment` 完成（lago `Invoices::PrepaidCreditJob`），尚无调用方：它等待支付编排中的付款状态更新（`app/services/invoices/update_service.rb:113-117`）。偏差：作废发票会使待处理的购买失败，之后的支付不再入账；lago 让其保持 pending，作废发票被支付后仍发放额度（`app/services/invoices/void_service.rb:30-48`、`app/jobs/invoices/prepaid_credit_job.rb:20-25`） |
| 发票类型 one-off（附加项） | `app/services/invoices/create_one_off_service.rb` | libs/billing | 逻辑已完成 | |
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
| 重试生成失败的订阅发票 | `app/services/invoices/retry_service.rb`、`app/jobs/clock/retry_generating_subscription_invoices_job.rb` | libs/billing | 逻辑已完成 | |
| 开票日期规则（锚点与调整矩阵） | `app/services/invoices/`（开票日期） | libs/billing | 逻辑已完成 | |
| 由付款条款（收到即付、净天数、月末、月末加天数、加天数后月末、每月某日）得出付款到期日，按 客户 → 计费主体 解析并连同来源快照 | `app/models/invoice.rb`、`db/structure.sql`（`payment_term`、`payment_term_source`） | libs/billing | 逻辑已完成 | Lago 已有这些列，规则在其分支上；不保留 `net_payment_term` 别名（[领域模型 §24](domain.zh-CN.md#24-设计决定)，第 12 条） |
| 发票上的采购订单号（来自归并的订阅、钱包交易或钱包、一次性请求），重新生成时保留 | `app/services/invoices/create_generating_service.rb`、`app/services/invoices/paid_credit_service.rb`、`app/services/invoices/regenerate_from_voided_service.rb` | libs/billing | 计划中 | |
| 带草稿发票的宽限期 | `app/services/invoices/subscription_service.rb` | libs/billing | 逻辑已完成 | |
| 刷新草稿 | `app/services/invoices/refresh_draft_service.rb`、`app/jobs/clock/refresh_draft_invoices_job.rb` | libs/billing | 逻辑已完成 | |
| 宽限期结束时自动定稿 | `app/jobs/clock/finalize_invoices_job.rb` | libs/billing | 逻辑已完成 | 由定时扫描定稿的草稿以扫描时的本地日期开具，并按扫描时的月份编号，即使其任务延迟执行；lago 使用执行任务时的时钟重新设定日期并编号（`app/services/invoices/refresh_draft_and_finalize_service.rb:77`、`app/models/invoice.rb:738`），两者始终一致 |
| 手动定稿与定稿全部草稿 | `app/services/invoices/refresh_draft_and_finalize_service.rb` | libs/billing | 逻辑已完成 | |
| 带额度与退款金额的作废 | `app/services/invoices/void_service.rb` | libs/billing | 逻辑已完成 | |
| 作废时退回优惠券、钱包与贷项通知单 | `app/services/invoices/void_service.rb` | libs/billing | 逻辑已完成 | |
| 作废时作废提供者的税 | `app/services/invoices/provider_taxes/void_service.rb` | libs/billing | 逻辑已完成 | |
| 从已作废的发票重新生成，带预览 | `app/services/invoices/regenerate_from_voided_service.rb` | libs/billing | 逻辑已完成 | |
| 重试失败的提供者税额计算 | `app/services/invoices/provider_taxes/pull_taxes_and_apply_service.rb` | libs/billing | 逻辑已完成 | 发票的服务商税额延迟计算：在事务之外调用税务服务商（独立预付费用、预览与当前用量如 lago 一样直接调用），在第二个事务中应用其结果；若期间费用发生变化则重新询问；lago 会应用先前的结果（`app/services/invoices/provider_taxes/pull_taxes_and_apply_service.rb:40`） |
| 定稿因 VIES 检查而挂起的发票 | `app/services/invoices/finalize_pending_vies_invoice_service.rb` | libs/billing | 计划中 | |
| 渐进式计费扣减与超额贷项通知单 | `app/services/invoices/progressive_billing_service.rb` | libs/billing | 逻辑已完成 | |
| 发票预览：提案、预测、终止、更换套餐 | `app/services/invoices/preview_service.rb` | libs/billing | 逻辑已完成 | |
| 争议败诉 | `app/services/payments/lose_dispute_service.rb` | libs/billing | 计划中 | |
| 删除草稿发票 | `app/services/invoices/delete_service.rb` | libs/billing | 计划中 | 已同步到外部系统后被拒绝；同时删除其草稿贷项通知单；该周期不再计费 |
| 更新付款状态与元数据（最多 5 个，键 20 字符，值 140 字符） | `app/services/invoices/update_service.rb` | libs/billing | 计划中 | 对付款状态变为 succeeded 或 failed 的 credit 发票，须调用 `wallets.Service.HandleCreditInvoicePayment`（`update_service.rb:113-117`） |
| 把发票标记为逾期 | `app/services/invoices/payments/mark_overdue_service.rb` | libs/billing | 计划中 | |
| 发票条目上的条目元数据（最多 50 个键） | `app/models/`（`item_metadata`） | libs/billing | 计划中 | |
| 发票结清（付款、贷项通知单） | `app/models/invoice_settlement.rb` | libs/billing | 计划中 | |
| 发票列表过滤与搜索 | `app/queries/invoices_query.rb` | libs/store | 逻辑已完成 | 包括采购订单号，不区分大小写；两种存储均已实现查询，移植的规格用例在 porttest 中；API 尚未实现 |
| 发票自定义段落（手动、系统生成）及其解析规则 | `app/models/invoice_custom_section.rb`、`app/services/invoice_custom_sections/` | libs/billing | 计划中 | 公开 API，而不只供管理界面使用 |
| 下载 PDF 与 XML | `app/services/invoices/generate_pdf_service.rb` | libs/documents | 逻辑已完成 | |
| 重新发送发票邮件 | `app/services/emails/resend_service.rb` | libs/mail | 逻辑已完成 | |
| 发票的付款 URL | `app/services/invoices/payments/generate_payment_url_service.rb` | libs/billing | 计划中 | |
| 在发票上存储 Salesforce ID | `app/controllers/api/v1/invoices_controller.rb`（`sync_salesforce_id`） | providers/salesforce | 计划中 | |
| 发票 webhook `invoice.created`、`one_off_created`、`paid_credit_added`、`generated`、`drafted`、`ready_to_finalize`、`voided`、`deleted`、`resynced` | `app/services/webhooks/invoices/` | libs/webhooks | 计划中 | |
| 发票付款 webhook `invoice.payment_status_updated`、`payment_overdue`、`payment_failure`、`payment_dispute_lost` | `app/services/webhooks/invoices/`、`app/services/webhooks/payment_providers/` | libs/webhooks | 计划中 | |

### 1.24 费用

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 费用类型：计费项、附加项、订阅、额度、承诺消费、固定计费项、产品 | `app/models/fee.rb` | libs/core | 计划中 | |
| 预付费用的付款状态（pending、succeeded、failed、refunded） | `app/models/fee.rb` | libs/core | 计划中 | |
| 订阅费用 | `app/services/fees/subscription_service.rb` | libs/billing | 逻辑已完成 | |
| 计费项费用（包括过滤器与分组） | `app/services/fees/charge_service.rb` | libs/billing | 逻辑已完成 | |
| 固定计费项费用 | `app/services/fees/fixed_charge_service.rb` | libs/billing | 逻辑已完成 | |
| 附加项与一次性费用 | `app/services/fees/one_off_service.rb` | libs/billing | 逻辑已完成 | |
| 钱包充值的额度费用 | `app/services/fees/paid_credit_service.rb` | libs/billing | 逻辑已完成 | 单价舍去钱包汇率中不足一分的部分，与 lago 的 bigint `unit_amount_cents` 一致（`app/models/wallet_transaction.rb:99`）；精确单价即该单价（`paid_credit_service.rb:40`）。作废 credit 发票会使购买失败（偏差，见 credit 发票一行） |
| 承诺消费费用 | `app/services/fees/commitments/` | libs/billing | 逻辑已完成 | |
| 草稿发票上的调整费用（单位数、单价、显示名称） | `app/models/adjusted_fee.rb`、`app/services/adjusted_fees/` | libs/billing | 逻辑已完成 | 调整只作用于计费项（或固定费用）边界相同的费用，与 lago 的 `matching_charge_boundaries` 一致（`app/models/adjusted_fee.rb:35-39`） |
| 费用的税 | `app/models/fee/applied_tax.rb` | libs/tax | 计划中 | |
| 费用的展示明细 | `app/models/`（`presentation_breakdowns`） | libs/billing | 进行中 | 存储在费用上（lago 使用 `presentation_breakdowns` 表），并在发票上打印（支持 null 值）；计费尚未计算它们 |
| 费用 API（列出、获取、更新付款状态、删除未关联的费用） | `app/controllers/api/v1/fees_controller.rb` | apps/server | 计划中 | |
| 费用 webhook `fee.created`（预付） | `app/services/webhooks/fees/pay_in_advance_created_service.rb` | libs/webhooks | 计划中 | |

### 1.25 税

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 税的增删改查（名称、代码、税率、描述） | `app/models/tax.rb`、`app/services/taxes/` | libs/tax | 计划中 | |
| 计费主体、客户、套餐、计费项、固定计费项、附加项、承诺消费上的税 | `app/models/` 中的关联表 | libs/tax | 计划中 | |
| 税的解析顺序（显式、附加项、计费项、固定计费项、承诺消费、套餐、客户、计费主体） | `app/services/fees/apply_taxes_service.rb` | libs/tax | 逻辑已完成 | |
| 费率卡的税覆盖套餐、客户和计费主体的税 | `app/models/rate_card.rb`（`rate_cards_taxes`） | libs/tax | 计划中 | 在统一目录中，费率卡的税就是其计费项的税 |
| 发票上应用的税，带计税基础与分摊 | `app/services/invoices/apply_taxes_service.rb` | libs/tax | 逻辑已完成 | 服务商的计税基础存储在发票已应用税上并打印在发票上（`app/models/invoice/applied_tax.rb:31-41`） |
| 贷项通知单上应用的税 | `app/services/credit_notes/apply_taxes_service.rb` | libs/tax | 逻辑已完成 | |
| 提供者计算的税（Anrok、Avalara），带特殊代码与基础税率 | `app/services/invoices/apply_provider_taxes_service.rb` | libs/tax | 逻辑已完成 | |
| 各国欧盟增值税税率及例外 | `app/services/taxes/`（ibericode 税率） | libs/tax | 逻辑已完成 | |
| 为组织自动生成欧盟税 | `app/services/taxes/auto_generate_service.rb` | libs/tax | 逻辑已完成 | |
| 按客户的欧盟税判定（境内、欧盟内 B2B 反向征税、B2C、欧盟外） | `app/services/customers/eu_auto_taxes_service.rb` | libs/tax | 逻辑已完成 | |
| 带重试与退避的 VIES 增值税号检查 | `app/services/customers/vies_check_service.rb` | libs/tax | 逻辑已完成 | |
| 在待定的 VIES 检查完成前挂起发票 | `app/models/pending_vies_check.rb` | libs/billing | 计划中 | |
| VIES webhook `customer.vies_check` | `app/services/webhooks/customers/vies_check_service.rb` | libs/webhooks | 计划中 | |
| 税务提供者错误 webhook `customer.tax_provider_error`、`fee.tax_provider_error` | `app/services/webhooks/integrations/taxes/` | libs/webhooks | 计划中 | |

### 1.26 优惠券

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 固定金额或百分比优惠券的增删改查 | `app/models/coupon.rb`、`app/services/coupons/` | libs/billing | 逻辑已完成 | |
| 频率：一次、循环（带持续期）、永久 | `app/models/coupon.rb` | libs/billing | 逻辑已完成 | |
| 可重复使用的优惠券 | `app/models/coupon.rb` | libs/billing | 逻辑已完成 | |
| 限定于套餐或计费指标 | `app/models/coupon_target.rb` | libs/billing | 逻辑已完成 | |
| 按日期过期与终止任务 | `app/jobs/clock/terminate_coupons_job.rb` | libs/billing | 逻辑已完成 | |
| 已应用的优惠券，可覆盖金额、百分比与频率 | `app/services/applied_coupons/create_service.rb` | libs/billing | 逻辑已完成 | |
| 发票上的应用顺序与金额计算 | `app/services/credits/applied_coupons_service.rb`、`app/services/credits/applied_coupon_service.rb` | libs/billing | 逻辑已完成 | |
| 作废时退回优惠券 | `app/services/applied_coupons/recredit_service.rb` | libs/billing | 逻辑已完成 | |
| 终止已应用的优惠券 | `app/services/applied_coupons/terminate_service.rb` | libs/billing | 逻辑已完成 | |

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
| 贷项通知单列表过滤，包括发票的采购订单号 | `app/queries/credit_notes_query.rb` | libs/store | 逻辑已完成 | 两种存储均已实现查询，移植的规格用例在 porttest 中；API 尚未实现 |
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
| 钱包交易的付款 URL | `app/services/wallet_transactions/payments/generate_payment_url_service.rb` | libs/billing | 计划中 | |
| 钱包与交易元数据（最多 15 个键） | `app/models/metadata/` | libs/billing | 逻辑已完成 | |
| 钱包 webhook `wallet.created`、`updated`、`terminated`、`depleted_ongoing_balance` | `app/services/webhooks/wallets/` | libs/webhooks | 计划中 | |
| 钱包交易 webhook `wallet_transaction.created`、`updated`、`payment_failure` | `app/services/webhooks/wallet_transactions/` | libs/webhooks | 计划中 | |

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
| 套餐或订阅上的用量阈值（二者互斥） | `app/models/usage_threshold.rb`、`app/services/usage_thresholds/` | libs/billing | 逻辑已完成 | |
| 最多一个循环阈值 | `app/services/usage_thresholds/update_service.rb` | libs/billing | 逻辑已完成 | |
| 已应用的用量阈值 | `app/models/applied_usage_threshold.rb` | libs/billing | 逻辑已完成 | |
| 按订阅覆盖阈值 | `app/services/usage_thresholds/override_service.rb` | libs/billing | 逻辑已完成 | |
| 用量刷新时检查阈值是否越过 | `app/services/lifetime_usages/check_thresholds_service.rb` | libs/billing | 逻辑已完成 | |
| 越过阈值时开具渐进式计费发票 | `app/services/invoices/progressive_billing_service.rb` | libs/billing | 逻辑已完成 | |

### 1.32 用量监控告警

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 告警类型：当前用量金额 | `app/models/usage_monitoring/alert.rb` | libs/billing | 计划中 | |
| 告警类型：计费指标当前用量金额与单位数 | `app/models/usage_monitoring/` | libs/billing | 计划中 | |
| 告警类型：生命周期用量金额与计费指标生命周期用量单位数 | `app/models/usage_monitoring/` | libs/billing | 计划中 | |
| 告警类型：钱包余额金额、额度余额、实时余额金额、实时额度余额 | `app/models/usage_monitoring/` | libs/billing | 计划中 | |
| 每个告警最多 20 个阈值，可有循环阈值 | `app/models/usage_monitoring/alert.rb` | libs/billing | 计划中 | |
| 已触发的告警 | `app/models/usage_monitoring/triggered_alert.rb` | libs/billing | 计划中 | |
| 根据订阅活动评估告警 | `app/services/usage_monitoring/process_subscription_activity_service.rb` | libs/billing | 计划中 | 持有告警的行锁，编辑告警时也取同一把锁 |
| 订阅与钱包上的告警 API，支持批量创建 | `app/controllers/api/v1/subscriptions/alerts_controller.rb`、`app/controllers/api/v1/customers/wallets/alerts_controller.rb` | apps/server | 计划中 | |
| webhook `alert.triggered`（高优先级） | `app/services/webhooks/usage_monitoring/alert_triggered_service.rb` | libs/webhooks | 计划中 | |

### 1.33 支付

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 每个组织带代码与名称的支付提供者 | `app/models/payment_providers/base_provider.rb` | libs/core | 计划中 | 每个提供者可有多个连接 |
| 把提供者状态映射为 pending、processing、succeeded、failed | `app/models/payment_providers/*.rb`（`determine_payment_status`） | providers/* | 进行中 | Stripe 已完成（`stripe.StatusOf`，对应 lago `StripeProvider` 的状态列表）；`payment.PaymentResult` 还带有提供者状态与 `requires_action` 的下一步 URL；缺少 `next_action.type` 时按 `redirect_to_url` 处理 |
| 成功后的跳转 URL | `app/models/payment_providers/base_provider.rb` | libs/core | 计划中 | |
| 创建提供者客户 | `app/services/payment_providers/create_customer_factory.rb` | providers/* | 进行中 | 端口 `payment.CustomerGateway`（创建、更新、移除；由 `payment.SyncActionOf` 决定同步方式）；Stripe 已完成 |
| 删除提供者（解除客户关联） | `app/services/payment_providers/destroy_service.rb` | libs/billing | 计划中 | |
| 每个客户多个提供者与集成连接，每个类别一个默认连接 | `app/services/payment_provider_customers/`、`app/services/integration_customers/` | libs/core | 计划中 | 完整实现；Lago 一次只允许一个 |
| 按计费对象选择连接（指定、跳过、继承） | `app/services/billing_object_connections/` | libs/billing | 计划中 | |
| 支付方式（来自提供者、默认、详情、删除） | `app/services/payment_methods/` | libs/billing | 计划中 | 提供者侧已完成：providers/stripe 中的 `payment.MethodGateway`（列出、默认、详情、设为默认、解绑、设置 URL）。解绑会在 Stripe 上解绑；lago 只丢弃记录（`app/services/payment_methods/destroy_service.rb`） |
| 支付方式优先级（显式覆盖、订阅、钱包交易、规则、钱包、客户默认） | `app/services/payment_methods/determine_service.rb` | libs/billing | 计划中 | |
| 手动支付方式跳过自动扣款 | `app/services/payment_methods/determine_service.rb` | libs/billing | 计划中 | |
| 付款类型 provider、manual、x402 | `app/models/payment.rb` | libs/core | 计划中 | |
| 带参考号（最多 40 字符）的手动付款 | `app/services/payments/manual_create_service.rb` | libs/billing | 计划中 | |
| 带在途保护与尝试计数的发票自动付款 | `app/services/invoices/payments/create_service.rb` | libs/billing | 计划中 | 每个应付对象只有一笔在途的提供者付款 |
| 零金额发票标记为已付 | `app/services/invoices/payments/create_service.rb` | libs/billing | 逻辑已完成 | |
| 有未关闭的付款链接时延迟自动付款 | `app/services/invoices/payments/create_service.rb`（`CHECKOUT_AUTO_PAYMENT_DELAY`） | libs/billing | 计划中 | |
| 付款参考文字 | `app/services/invoices/payments/create_service.rb` | libs/billing | 计划中 | |
| 付款意向（带过期与复用的付款链接） | `app/models/payment_intent.rb`、`app/services/payment_intents/` | libs/billing | 计划中 | |
| 重试发票付款与全部重试 | `app/services/invoices/payments/retry_service.rb`、`retry_batch_service.rb` | libs/billing | 计划中 | |
| 取消需要操作而被放弃的付款 | `app/services/invoices/payments/cancel_abandoned_service.rb` | libs/billing | 计划中 | |
| 付款成功的效果：已付合计、发票付款状态、会计同步 | `app/services/invoices/payments/*_service.rb` | libs/billing | 计划中 | |
| 付款失败 webhook（发票、钱包交易、付款请求） | `app/services/invoices/payments/deliver_error_webhook_service.rb` | libs/webhooks | 计划中 | |
| 付款 webhook `payment.succeeded`、`payment.requires_action` | `app/services/webhooks/payments/` | libs/webhooks | 计划中 | |
| 提供者 webhook `payment_provider.error`、`customer.payment_provider_created`、`customer.payment_provider_error`、`customer.checkout_url_generated` | `app/services/webhooks/payment_providers/` | libs/webhooks | 计划中 | |
| 付款 API（列出、获取、创建手动付款）与按客户的付款 | `app/controllers/api/v1/payments_controller.rb` | apps/server | 计划中 | |
| 支付方式 API（列出、删除、设为默认） | `app/controllers/api/v1/customers/payment_methods_controller.rb` | apps/server | 计划中 | |

### 1.34 支付提供者

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| Stripe 连接（密钥），自动注册 webhook 端点 | `app/services/payment_providers/stripe_service.rb`、`app/jobs/payment_providers/stripe/register_webhook_job.rb` | providers/stripe | 逻辑已完成 | 设置项 `success_redirect_url`、`webhook_endpoint_id`、`supports_3ds`、`require_terms_of_service_consent`；webhook 签名密钥保存在连接的 secrets 中，而非设置（lago 保存在设置中，`base_provider.rb:30`）。端点按连接区分（`/webhooks/stripe/{connection_id}`），使用 stripe-go 固定的 API 版本，不支持按调用覆盖版本（`register_webhook_service.rb:8`）；额外订阅两个事件（`payment_intent.requires_action`、`payment_method.attached`）。按连接与密钥模式的客户端令牌桶限流；stripe-go 在进程内重试连接错误与 429 `lock_timeout`，其余错误与 lago 一样由调用方的任务重试（`create_service.rb:50-56`）。未移植的 spec 用例及原因见 providers/stripe/SKIPPED.md |
| Stripe 3DS 支持与服务条款同意 | `app/models/payment_providers/stripe_provider.rb` | providers/stripe | 逻辑已完成 | |
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
| Adyen 连接（API 密钥、商户账户、live 前缀、HMAC 密钥） | `app/models/payment_providers/adyen_provider.rb` | providers/adyen | 计划中 | |
| 以零金额预授权链接实现的 Adyen 结账 | `app/services/payment_provider_customers/adyen_service.rb` | providers/adyen | 计划中 | |
| 使用已存支付方式的 Adyen 付款 | `app/services/payment_providers/adyen/payments/create_service.rb` | providers/adyen | 计划中 | |
| Adyen 付款链接 | `app/services/invoices/payments/adyen_service.rb` | providers/adyen | 计划中 | |
| Adyen webhook：授权、取消、退款、退款失败、拒付 | `app/services/payment_providers/adyen/handle_event_service.rb` | providers/adyen | 计划中 | |
| 通过 OAuth 的 GoCardless 连接 | `app/services/payment_providers/gocardless_service.rb` | providers/gocardless | 计划中 | |
| GoCardless 客户，通过 billing request 设置授权 | `app/services/payment_provider_customers/gocardless_service.rb` | providers/gocardless | 计划中 | |
| 针对最新有效授权的 GoCardless 付款 | `app/services/payment_providers/gocardless/payments/create_service.rb` | providers/gocardless | 计划中 | |
| GoCardless webhook：付款、退款、授权 | `app/services/payment_providers/gocardless/handle_event_service.rb` | providers/gocardless | 计划中 | |
| Cashfree 连接与付款链接 | `app/services/invoices/payments/cashfree_service.rb` | providers/cashfree | 计划中 | |
| 带签名校验的 Cashfree webhook（付款链接已付） | `app/services/payment_providers/cashfree/` | providers/cashfree | 计划中 | |
| Flutterwave 连接与付款链接 | `app/services/invoices/payments/flutterwave_service.rb` | providers/flutterwave | 计划中 | |
| 带交易核验的 Flutterwave webhook | `app/services/payment_providers/flutterwave/` | providers/flutterwave | 计划中 | |
| Moneyhash 连接（API 密钥、flow ID、获取签名密钥） | `app/services/payment_providers/moneyhash_service.rb` | providers/moneyhash | 计划中 | |
| Moneyhash 客户与卡令牌化结账 | `app/services/payment_provider_customers/moneyhash_service.rb` | providers/moneyhash | 计划中 | |
| Moneyhash 商户发起的循环付款与付款链接 | `app/services/payment_providers/moneyhash/payments/create_service.rb` | providers/moneyhash | 计划中 | |
| Moneyhash webhook：意向已处理与过期、购买失败、待认证、成功、卡令牌事件 | `app/services/payment_providers/moneyhash/` | providers/moneyhash | 计划中 | |
| 取消付款（Stripe、Adyen、GoCardless） | `app/services/payment_providers/cancel_payment_service.rb` | providers/* | 进行中 | Stripe 已完成：`CancelPayment` 返回新状态，对已终结的意向返回零值结果（`cancel_service.rb` 记录日志并视为成功） |
| 更新付款参考（Stripe） | `app/services/payment_providers/update_payment_reference_service.rb` | providers/stripe | 逻辑已完成 | |

### 1.35 付款请求、催收、收据与退款

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 汇总逾期发票（同一货币与计费主体）的付款请求 | `app/services/payment_requests/create_service.rb` | libs/billing | 计划中 | |
| 扣除贷项通知单抵销后的付款请求金额 | `app/services/payment_requests/create_service.rb` | libs/billing | 计划中 | |
| 付款请求通过提供者扣款，或通过邮件发送 | `app/services/payment_requests/payments/create_service.rb`、`app/mailers/payment_request_mailer.rb` | libs/billing | 计划中 | |
| 付款请求状态更新 | `app/services/payment_requests/update_service.rb` | libs/billing | 计划中 | |
| 付款请求 API 与按客户列表 | `app/controllers/api/v1/payment_requests_controller.rb` | apps/server | 计划中 | |
| 付款请求 webhook `payment_request.created`、`payment_status_updated`、`payment_failure` | `app/services/webhooks/payment_requests/` | libs/webhooks | 计划中 | |
| 催收活动（按货币的阈值、尝试间隔天数、最大尝试次数、BCC 邮箱） | `app/models/dunning_campaign.rb`、`app/services/dunning_campaigns/` | libs/billing | 计划中 | 公开 API，而不只供管理界面使用 |
| 活动应用于计费主体或客户，可排除客户 | `app/services/dunning_campaigns/process_customer_service.rb` | libs/billing | 计划中 | |
| 阈值变更时重置不再匹配的客户 | `app/services/dunning_campaigns/update_service.rb` | libs/billing | 计划中 | |
| 催收处理：按货币计尝试次数、检查阈值、发起付款请求 | `app/services/dunning_campaigns/bulk_process_service.rb`、`process_attempt_service.rb` | libs/billing | 计划中 | |
| 催收 webhook `dunning_campaign.finished` | `app/services/webhooks/dunning_campaigns/finished_service.rb` | libs/webhooks | 计划中 | |
| 成功付款的付款收据，按客户编号 | `app/services/payment_receipts/create_service.rb` | libs/billing | 计划中 | |
| 付款收据 PDF 与 XML，按计费主体设置发送邮件 | `app/services/payment_receipts/generate_pdf_service.rb`、`generate_xml_service.rb` | libs/documents | 逻辑已完成 | |
| 付款收据 API 与重新发送邮件 | `app/controllers/api/v1/payment_receipts_controller.rb` | apps/server | 计划中 | |
| 付款收据 webhook `payment_receipt.created`、`payment_receipt.generated` | `app/services/webhooks/payment_receipts/` | libs/webhooks | 计划中 | |
| 通过 Stripe、GoCardless、Adyen 为贷项通知单退款 | `app/services/credit_notes/refunds/` | providers/* | 进行中 | 端口 `payment.RefundGateway`，以必填的贷项通知单 ID 为幂等键；Stripe 已完成 |
| 把退款原因映射为提供者的原因 | `app/services/credit_notes/refunds/stripe_service.rb` | providers/stripe | 逻辑已完成 | Stripe 的 `canceled` 退款映射为失败，`requires_action` 映射为待处理；lago 直接复制状态，二者都会校验失败（`credit_note.rb:62`）。其他状态返回 `payment.ErrUnknownStatus`，调用方不予持久化，与 lago 的校验失败一致（`credit_notes/refunds/stripe_service_spec.rb:360`） |
| 根据提供者 webhook 更新退款状态 | `app/services/credit_notes/refunds/`（`update_status`） | libs/billing | 逻辑已完成 | |

### 1.36 x402 代理付款

Lago 的 x402 支持尚未完成：只有准入检查可以调用。Tanvo 移植整个设计并把它完成。

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| x402 连接（Coinbase CDP 结算方、收款地址、网络、USDC、自动创建客户） | `app/models/x402/connection.rb` | libs/core | 计划中 | 提供管理 API |
| 网络 Base、Base Sepolia、Solana 主网与开发网；各网络的 USDC 资产 | `app/services/x402/network.rb`、`app/services/x402/asset.rb` | libs/core | 计划中 | |
| 准入检查：代理现在能否使用某个指标，或者需要支付什么 | `app/services/x402/gate_checks/check_service.rb` | libs/billing | 计划中 | 余额 − 进行中用量足以支付 1,000 次估算调用时通过，直到 Lago 的预留计数器取代这个下限 |
| 准入检查的路由检查：父套餐及其计费项必须能够以预付钱包控制准入 | `app/services/x402/gate_checks/route_check_service.rb` | libs/billing | 计划中 | |
| 准入检查接口的 `x402` API 密钥权限范围 | `app/models/api_key.rb` | libs/auth | 计划中 | |
| 启用 x402 的钱包（`x402_enabled`） | `app/models/wallet.rb` | libs/core | 计划中 | |
| 绑定到 x402 连接的发票支付链接（`x402_connection_id`、`x402_payment_token`） | `app/models/invoice.rb` | libs/core | 计划中 | 令牌设置时唯一；连接属于同一组织 |
| 来自结算方的付款要求 | `app/services/x402/payment_requirements_service.rb` | libs/billing | 计划中 | |
| 结算方客户端（verify、settle、supported），每个请求一个 JWT | `app/services/x402/facilitator/coinbase_cdp_adapter.rb` | providers/* | 计划中 | 新的提供者模块 `providers/x402` |
| 结算（核验、结算、对账） | `app/services/x402/settlements/` | libs/billing | 计划中 | 接入任务；在 Lago 中未被使用 |
| 通过 JSON-RPC 的链上对账（EVM、Solana） | `app/services/x402/chain/` | providers/* | 计划中 | |
| 用于幂等的付款摘要 | `app/services/x402/payments/compute_digest_service.rb` | libs/billing | 计划中 | 摘要和交易哈希在所有组织间唯一，是唯一跨租户的唯一索引（Lago 18a741762） |

### 1.37 会计、CRM 与税务集成

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| NetSuite 连接（账户、基于令牌的认证、RESTlet 端点） | `app/models/integrations/netsuite_integration.rb` | providers/netsuite | 计划中 | 直接调用 API，不经 Nango（§3） |
| NetSuite 子公司 | `app/services/integrations/aggregator/subsidiaries_service.rb` | providers/netsuite | 计划中 | |
| 按交易 ID 对账 NetSuite 发票 | `app/services/integrations/aggregator/invoices/reconcile_service.rb` | providers/netsuite | 计划中 | |
| NetSuite 税务关联、税种与税码映射 | `app/models/integration_collection_mappings/netsuite_collection_mapping.rb` | providers/netsuite | 计划中 | |
| Xero 连接 | `app/models/integrations/xero_integration.rb` | providers/xero | 计划中 | |
| 会计同步设置：发票、贷项通知单、付款 | `app/models/integrations/` | libs/billing | 计划中 | |
| 获取提供者的商品与科目 | `app/services/integrations/aggregator/items_service.rb`、`accounts_service.rb` | providers/* | 计划中 | |
| 附加项与计费指标的商品映射，可按计费主体 | `app/services/integration_mappings/` | libs/core | 计划中 | |
| 集合映射（兜底商品、优惠券、订阅费、最低承诺消费、税、预付额度、贷项通知单、科目、货币） | `app/services/integration_collection_mappings/` | libs/core | 计划中 | |
| 创建与更新会计联系人 | `app/services/integrations/aggregator/contacts/` | providers/* | 计划中 | |
| 发票同步到会计系统 | `app/services/integrations/aggregator/invoices/create_service.rb` | providers/* | 计划中 | |
| 贷项通知单同步到会计系统 | `app/services/integrations/aggregator/credit_notes/create_service.rb` | providers/* | 计划中 | |
| 付款同步到会计系统 | `app/services/integrations/aggregator/payments/create_service.rb` | providers/* | 计划中 | |
| 已同步资源记录（发票、付款、贷项通知单、订阅的外部 ID） | `app/models/integration_resource.rb` | libs/core | 计划中 | |
| 手动重新同步发票或贷项通知单 | `app/graphql/mutations/integrations/sync_invoice.rb` | libs/billing | 计划中 | |
| 部署了自定义对象与属性的 HubSpot 连接 | `app/services/integrations/hubspot/` | providers/hubspot | 计划中 | |
| 每个客户的 HubSpot 公司或联系人 | `app/services/integrations/aggregator/companies/`、`contacts/` | providers/hubspot | 计划中 | |
| 带关联的 HubSpot 发票与订阅记录 | `app/services/integrations/aggregator/invoices/hubspot/`、`subscriptions/hubspot/` | providers/hubspot | 计划中 | |
| Salesforce 连接（实例）与发票 ID 同步 | `app/services/integrations/salesforce/` | providers/salesforce | 计划中 | |
| Anrok 连接，以及为草稿、预览与用量询价税额 | `app/services/integrations/aggregator/taxes/invoices/create_draft_service.rb` | providers/anrok | 计划中 | |
| Anrok 草稿税额缓存 | `app/services/integrations/aggregator/taxes/invoices/draft_taxes_cache_service.rb` | providers/anrok | 计划中 | |
| 带公司查找的 Avalara 连接 | `app/services/integrations/aggregator/taxes/avalara/fetch_company_id_service.rb` | providers/avalara | 计划中 | |
| 已定稿发票的税务申报 | `app/services/integrations/aggregator/taxes/invoices/create_service.rb` | providers/* | 计划中 | |
| 发票作废时作废或冲销税额 | `app/services/integrations/aggregator/taxes/invoices/void_service.rb`、`negate_service.rb` | providers/* | 计划中 | |
| 贷项通知单的税务申报 | `app/services/integrations/aggregator/taxes/credit_notes/create_service.rb` | providers/* | 计划中 | |
| 重试税务申报与税额作废 | `app/graphql/mutations/integrations/` | libs/billing | 逻辑已完成 | |
| 集成重试策略（网关错误、请求上限、限流、超时） | `app/jobs/integrations/` | libs/jobs | 计划中 | |
| 集成 webhook `customer.accounting_provider_created`、`customer.accounting_provider_error`、`customer.crm_provider_created`、`customer.crm_provider_error`、`integration.provider_error` | `app/services/webhooks/integrations/` | libs/webhooks | 计划中 | |
| 每个客户只有一个税务集成 | `app/models/integration_customers/base_customer.rb` | libs/core | 计划中 | |

### 1.38 出站 Webhook

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| Webhook 端点（每个组织最多 10 个，URL 唯一，可命名） | `app/models/webhook_endpoint.rb`、`app/services/webhook_endpoints/` | libs/webhooks | 计划中 | |
| 每个端点订阅的事件类型（默认全部） | `app/models/webhook_endpoint.rb`、`config/webhook_event_types.yml` | libs/webhooks | 逻辑已完成 | |
| 带描述与类别的事件类型目录 | `config/webhook_event_types.yml` | libs/webhooks | 计划中 | |
| 带重试与退避的投递 | `app/services/webhooks/send_http_service.rb` | libs/webhooks | 逻辑已完成 | |
| 签名的载荷 | `app/models/webhook.rb` | libs/webhooks | 逻辑已完成 | Standard Webhooks（§3） |
| 阻止私有地址，可显式关闭 | `lib/lago_http_client/`（`AddressGuard`） | libs/webhooks | 逻辑已完成 | |
| 带响应、状态与重试次数的投递记录 | `app/models/webhook.rb` | libs/webhooks | 逻辑已完成 | |
| 手动重试某次投递 | `app/services/webhooks/retry_service.rb` | libs/webhooks | 逻辑已完成 | |
| 带过滤（状态、事件类型、搜索）的投递列表 | `app/graphql/resolvers/webhooks_resolver.rb` | libs/webhooks | 计划中 | |
| 90 天后清理投递记录 | `app/jobs/clock/webhooks_cleanup_job.rb` | libs/webhooks | 已完成 | 每日 clock 任务为每个有端点的组织入队一个清除任务，在该组织的行级安全范围内每个事务删除 1 000 条投递；lago 在 clock 任务中删除所有组织的记录（`app/jobs/clock/webhooks_cleanup_job.rb:19`）。与 lago 一致，保留期从 `updated_at` 起算，使用索引 `(organization_id, updated_at) INCLUDE (id)` |
| Webhook 端点 API | `app/controllers/api/v1/webhook_endpoints_controller.rb` | apps/server | 计划中 | |
| 事件组 计费指标：`billable_metric.created`、`updated`、`deleted` | `app/services/webhooks/billable_metrics/` | libs/webhooks | 计划中 | |
| 事件组 套餐：`plan.created`、`updated`、`deleted` | `app/services/webhooks/plans/` | libs/webhooks | 计划中 | |
| 事件组 特性：`feature.created`、`updated`、`deleted` | `app/services/webhooks/features/` | libs/webhooks | 计划中 | |
| 事件组 客户：`customer.created`、`updated`、`vies_check` | `app/services/webhooks/customers/` | libs/webhooks | 计划中 | |
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
| 每个提供者连接的提供者 webhook 端点 | `app/controllers/webhooks_controller.rb` | apps/server | 计划中 | `POST /webhooks/{provider}/{connection_id}` |
| 按提供者校验签名（Stripe、Adyen HMAC、GoCardless、Cashfree、Flutterwave、Moneyhash） | `app/services/payment_providers/*/validate_incoming_webhook_service.rb` | providers/* | 计划中 | |
| 处理前的持久存储（pending、processing、succeeded、failed） | `app/models/inbound_webhook.rb`、`app/services/inbound_webhooks/` | libs/webhooks | 逻辑已完成 | 所有提供者，而不只是 Stripe 与 Moneyhash |
| 处理窗口与卡住的 webhook 的重试 | `app/services/inbound_webhooks/process_service.rb`、`app/jobs/clock/inbound_webhooks_retry_job.rb` | libs/webhooks | 逻辑已完成 | |
| 90 天后清理入站 webhook | `app/jobs/clock/inbound_webhooks_cleanup_job.rb` | libs/webhooks | 计划中 | |

### 1.40 流式目的地

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 认领事件类型的目的地（每种类型一个目的地） | `app/models/streaming_destinations/base_destination.rb` | libs/webhooks | 计划中 | 提供管理 API（§3） |
| 事件类型 `customer_usage.refreshed.v1`、`customer_full_usage.refreshed.v1` | `app/models/streaming_destinations/base_destination.rb` | libs/webhooks | 计划中 | |
| 从完整用量事件中排除的套餐 | `app/models/streaming_destinations/base_destination.rb` | libs/webhooks | 计划中 | |
| 带角色扮演与分区键的 Kinesis 目的地 | `app/models/streaming_destinations/kinesis_destination.rb`、`lib/lago/kinesis/producer.rb` | libs/webhooks | 计划中 | |
| 带钱包信息的用量刷新信封 | `app/services/event_destinations/` | libs/webhooks | 计划中 | |
| 投递去重与日志（已投递、跳过、失败、被取代、丢弃） | `app/jobs/deliver_event_job.rb`、`app/services/event_destinations/delivery_logger.rb` | libs/webhooks | 计划中 | |

### 1.41 报价、订购单与订单

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 带状态的报价与报价版本 | `app/models/quote.rb`、`app/models/quote_version.rb`、`app/services/quotes/`、`app/services/quote_versions/` | libs/billing | 计划中 | |
| 订单类型：创建订阅、修改订阅、一次性，各带校验器 | `app/services/quote_versions/validators/` | libs/billing | 计划中 | |
| 报价内容中的提及变量 | `app/services/quotes/` | libs/documents | 计划中 | |
| 交易过期 | `app/services/quotes/`（`DealExpiration`） | libs/billing | 计划中 | |
| 订购单（已创建、已签署、已过期、已作废） | `app/models/order_form.rb`、`app/services/order_forms/` | libs/billing | 计划中 | |
| 订购单过期 | `app/jobs/clock/expire_order_forms_job.rb` | libs/billing | 计划中 | |
| 订单与定时执行 | `app/models/order.rb`、`app/jobs/clock/execute_scheduled_orders_job.rb` | libs/billing | 计划中 | |

### 1.42 用量归属

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 用量归属类型，分层与扁平（`account_tree` 开关） | `app/models/usage_attribution_type.rb`、`app/controllers/api/v1/usage_attribution_types_controller.rb` | libs/core | 计划中 | |
| 归属取值 | `app/models/usage_attribution_value.rb` | libs/core | 计划中 | |
| 归属用量查询 | `app/controllers/api/v1/customers/attributed_usage_controller.rb` | libs/billing | 计划中 | Lago 没有填充路径；Tanvo 定义一个 |

### 1.43 数据导出

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 按列表过滤条件以 CSV 导出发票、发票费用、贷项通知单、贷项通知单条目 | `app/services/data_exports/` | libs/billing | 计划中 | 公开 API |
| 导出拆分为多个部分再合并 | `app/models/data_export_part.rb` | libs/jobs | 计划中 | |
| 导出文件 7 天后过期 | `app/models/data_export.rb` | libs/billing | 计划中 | |
| 导出完成邮件 | `app/mailers/data_export_mailer.rb` | libs/mail | 逻辑已完成 | |
| 发票、费用、贷项通知单、钱包、订阅、告警、每日用量及其他业务表的导出视图（`exports_*`） | `db/views/exports_*.sql` | libs/store | 计划中 | 只读 PostgreSQL 视图，与删除记录（§1.49）一起构成数据仓库同步的契约 |
| 导出视图的数据仓库同步 | Lago 私有的数据管道 | ee/pipeline | 计划中 | 读取视图的独立进程（架构 §14） |

### 1.44 分析

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 总收入 | `app/services/analytics/gross_revenues_service.rb` | libs/billing | 计划中 | 核心 API |
| MRR | `app/services/analytics/mrrs_service.rb` | libs/billing | 计划中 | 核心 API |
| 发票回款 | `app/services/analytics/invoice_collections_service.rb` | libs/billing | 计划中 | 核心 API |
| 已开票用量 | `app/services/analytics/invoiced_usages_service.rb` | libs/billing | 计划中 | 核心 API |
| 逾期余额 | `app/services/analytics/overdue_balances_service.rb` | libs/billing | 计划中 | 核心 API |
| 分析缓存（4 小时） | `app/services/analytics/` | libs/billing | 计划中 | |
| 按套餐的 MRR、预付额度、按客户与套餐的收入流、用量与聚合金额 | Data API（`app/services/data_api/`） | ee/analytics | 计划中 | |
| 嵌入式仪表盘 | Superset 嵌入（`app/services/` 中的 superset） | ee/analytics | 计划中 | |
| 用量预测 | 仅前端使用的高级版开关 | ee/analytics | 计划中 | |
| 收入确认 | 仅前端使用的高级版开关 | ee/analytics | 计划中 | |

### 1.45 日志

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 活动日志（来自 API、管理界面、系统的资源变更） | `app/services/utils/activity_log.rb` | libs/core | 计划中 | 由 `audit_events` 提供数据 |
| 每个组织的活动日志保留期 | `app/models/organization.rb` | libs/eventstore | 计划中 | |
| 写请求的 API 日志 | `app/services/utils/api_log.rb` | apps/server | 计划中 | |
| 安全日志（登录、用户、角色、API 密钥、集成、webhook 端点），保留 90 天 | `app/services/utils/security_log.rb` | libs/auth | 进行中 | |
| 邮件已发送活动 | `app/services/utils/activity_log.rb` | libs/mail | 逻辑已完成 | |
| 带过滤的日志 API | `app/controllers/api/v1/activity_logs_controller.rb`、`api_logs_controller.rb`、`security_logs_controller.rb` | apps/server | 计划中 | |
| 日志存放在 ClickHouse 中，没有 ClickHouse 时存放在 PostgreSQL 中 | `db/clickhouse_migrate/` | libs/eventstore | 计划中 | Lago 要求 ClickHouse |

### 1.46 客户门户

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 限定于一个客户的门户访问令牌 | `app/services/customer_portal/` | libs/auth | 逻辑已完成 | 短期有效的签名令牌 |
| 门户中查看与更新客户资料 | `app/graphql/resolvers/customer_portal/` | apps/server | 计划中 | |
| 门户中的发票列表与下载 | `app/graphql/resolvers/customer_portal/` | apps/server | 计划中 | |
| 门户中的订阅与当前用量 | `app/graphql/resolvers/customer_portal/` | apps/server | 计划中 | |
| 门户中的钱包与钱包充值 | `app/graphql/mutations/customer_portal/` | apps/server | 计划中 | |
| 门户中的逾期余额与发票回款 | `app/graphql/resolvers/customer_portal/` | apps/server | 计划中 | |
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
| 激活订阅与合同、刷新草稿（每 5 分钟） | `clock.rb` | libs/jobs | 进行中 | 激活按订阅调度 |
| 订阅活动处理（每分钟） | `clock.rb` | libs/jobs | 进行中 | |
| 刷新生命周期用量与钱包实时余额（每 5 分钟） | `clock.rb` | libs/jobs | 进行中 | 钱包也由用量刷新信号刷新（§1.29） |
| 终止已结束的订阅、校验事件（每小时 :05） | `clock.rb` | libs/jobs | 进行中 | |
| 订阅计费（每小时 :10） | `clock.rb`（`SubscriptionsBiller`） | libs/jobs | 进行中 | 按订阅的任务加每小时对账 |
| 创建与处理计费片段（每小时 :12、:17） | `clock.rb` | libs/jobs | 进行中 | |
| API 密钥使用情况、每日用量（每小时 :15） | `clock.rb` | libs/jobs | 进行中 | API 密钥使用情况已装配 |
| incomplete 订阅过期、定稿发票（每小时 :20） | `clock.rb` | libs/jobs | 进行中 | |
| 把发票标记为逾期（每小时 :25） | `clock.rb` | libs/jobs | 进行中 | |
| 重试生成发票、终止优惠券（每小时 :30） | `clock.rb` | libs/jobs | 进行中 | |
| 免费试用计费（每小时 :35） | `clock.rb` | libs/jobs | 进行中 | |
| 订购单过期、取消被放弃的付款（每小时 :40） | `clock.rb` | libs/jobs | 进行中 | |
| 终止钱包、执行定时订单、催收（每小时 :45） | `clock.rb` | libs/jobs | 进行中 | |
| 终止提醒、终止循环规则（每小时 :50） | `clock.rb` | libs/jobs | 进行中 | |
| 按间隔的钱包充值（每小时 :55） | `clock.rb` | libs/jobs | 进行中 | |
| 重试失败的发票、重试入站 webhook（每 15 分钟） | `clock.rb` | libs/jobs | 进行中 | |
| 清理 webhook、入站 webhook 与删除记录（每天） | `clock.rb` | libs/jobs | 进行中 | webhook 投递清理已装配，每个组织一个清除任务；入站 webhook 与删除记录清理待完成 |
| 消费订阅刷新队列（每 10 秒） | `clock.rb` | libs/jobs | 计划中 | 由消费者中的变更通知代替 |
| 按工作负载划分的队列 | `config/sidekiq/` | libs/jobs | 逻辑已完成 | River 队列按组织分片，并按组织使用令牌桶；周期任务运行在不分片的 `clock` 队列上 |

### 1.49 平台服务

| 功能 | Lago 源码 | Tanvo 模块 | 状态 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| 加密提供者密钥与集成凭据 | `app/models/concerns/secrets_storable.rb` | libs/store | 计划中 | AES-256-GCM 信封加密 |
| 供下游同步使用的删除记录 | `db/structure.sql`（`record_deletions`、`record_deletion()`）、`db/views/exports_record_deletions_v01.sql`、`app/jobs/clock/record_deletions_cleanup_job.rb` | libs/store | 计划中 | 费用、费用税、发票订阅和税表上的延迟 `AFTER DELETE` 触发器（刷新草稿会重建这些行）；导出给数据仓库同步（§1.43）；一个月后删除；删除事件也写入 `audit_events` |
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
| 已触发告警的 `resolved` 与种子类型 | `app/models/usage_monitoring/triggered_alert.rb` | 从未产生。Lago 正在构建 `alert.resolved`（296436eac、a84ccd69c、8fa46d700）；可达后再评估 |
| 管理 REST 控制器（只限本地） | `app/controllers/admin/` | 由 `tanvo admin` 取代 |
| `customers.vat_rate`、`billing_entities.vat_rate` | `db/structure.sql` | 由税取代 |
| `billing_entities.archived_at`、不起作用的 `destroyBillingEntity` mutation | `app/models/billing_entity.rb`、`app/graphql/mutations/billing_entities/` | 未使用 |
| GraphQL `providerCustomer` 与各提供者的 `*Customer` 字段；单数的 `provider_customer` 输入 | `app/graphql/types/customers/` | 由连接列表取代 |
| `RetryViesCheckJob` | `app/jobs/customers/retry_vies_check_job.rb` | 被带退避的 VIES 检查任务取代 |
| 在客户上赋值 `invoice_footer` | `app/services/customers/update_service.rb` | 并不存在这个列（§4） |
| `subscriptions.incompleted_at` 与只有一个 l 的 `cancelation_reason` | `db/structure.sql` | 与正确命名的列重复 |
| 过滤别名 `overriden` | `app/controllers/api/v1/subscriptions_controller.rb` | `overridden` 的拼写错误别名 |
| `plan_overrides.usage_thresholds` | `app/services/subscriptions/create_service.rb` | 由订阅用量阈值取代 |
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
| `dunning_campaigns.applied_to_organization` | `app/models/dunning_campaign.rb`（`ignored_columns`） | 由计费主体级指定取代 |
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
| 没有 API、只支持 Kinesis 的流式目的地 | 通过 API 管理的目的地；先支持 Kinesis，以连接器接口扩展更多 | 无需访问数据库即可配置 |
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
| Adyen webhook 基础服务从处理器已经拆开的条目中再读取 `notificationItems` | `app/services/payment_providers/adyen/webhooks/base_service.rb` | 每个通知条目只解析一次 |
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
| 欧盟特殊地区的邮编模式在邮编的任意位置匹配，导致里斯本 `1940123` 得到马德拉的增值税率 | `app/services/customers/eu_auto_taxes_service.rb` | 模式只匹配规范化邮编的开头 |
