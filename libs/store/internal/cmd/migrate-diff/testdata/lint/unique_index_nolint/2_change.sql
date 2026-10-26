-- atlas:txmode none

-- Codes were deduplicated by the previous release.
-- atlas:nolint MF101
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS fees_code_key ON fees (code);
