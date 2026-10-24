-- x402 connections and settlements (parity §1.36).

-- name: CreateX402Connection :execrows
INSERT INTO x402_connections (
  organization_id, id, code, name, facilitator, asset, networks, payout_addresses, auto_create_customers, secrets,
  deleted_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @code, @name, @facilitator, @asset, @networks, @payout_addresses, @auto_create_customers, @secrets,
  @deleted_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateX402Connection :execrows
UPDATE x402_connections SET
  -- The key columns are assigned to themselves first so that the parameters
  -- follow the order of CreateX402Connection.
  organization_id = @organization_id, id = @id, code = @code, name = @name, facilitator = @facilitator, asset = @asset,
  networks = @networks, payout_addresses = @payout_addresses, auto_create_customers = @auto_create_customers, secrets = @secrets,
  deleted_at = @deleted_at, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetX402Connection :one
SELECT * FROM x402_connections WHERE organization_id = @organization_id AND id = @id;

-- name: GetX402ConnectionByCode :one
SELECT * FROM x402_connections WHERE organization_id = @organization_id AND code = @code AND deleted_at IS NULL;

-- name: CreateX402Settlement :exec
INSERT INTO x402_settlements (
  organization_id, id, x402_connection_id, customer_id, subscription_id, wallet_transaction_id, payment_id, invoice_id,
  kind, status, settled_by, network, asset, payer_address, payee_address, settled_amount_atomic, settled_amount_cents,
  transaction_hash, reconcile_after, payment_digest, purchase_settings, payload, error_reason, created_at, updated_at
) VALUES (
  @organization_id, @id, @x402_connection_id, @customer_id, @subscription_id, @wallet_transaction_id, @payment_id, @invoice_id,
  @kind, @status, @settled_by, @network, @asset, @payer_address, @payee_address, @settled_amount_atomic, @settled_amount_cents,
  @transaction_hash, @reconcile_after, @payment_digest, @purchase_settings, @payload, @error_reason, @created_at, @updated_at
);

-- name: UpdateX402Settlement :execrows
-- The verified facts (payer, payee, amount, digest, settings, invoice)
-- are never rewritten.
UPDATE x402_settlements SET
  customer_id = @customer_id, subscription_id = @subscription_id, wallet_transaction_id = @wallet_transaction_id,
  payment_id = @payment_id, status = @status, settled_by = @settled_by, transaction_hash = @transaction_hash,
  reconcile_after = @reconcile_after, payload = @payload, error_reason = @error_reason, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetX402Settlement :one
SELECT * FROM x402_settlements WHERE organization_id = @organization_id AND id = @id;

-- name: X402PendingCreditPurchaseExists :one
SELECT EXISTS (
  SELECT 1 FROM x402_settlements
  WHERE organization_id = @organization_id AND payer_address = @payer_address
    AND kind = 'credit_purchase' AND status = 'pending'
);

-- name: ListX402PendingReconciliation :many
SELECT * FROM x402_settlements
WHERE organization_id = @organization_id AND status = 'pending' AND reconcile_after <= @now
ORDER BY reconcile_after, id;
