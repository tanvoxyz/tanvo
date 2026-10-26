-- atlas:txmode none

CREATE INDEX CONCURRENTLY IF NOT EXISTS fees_code ON fees (code);
DROP INDEX CONCURRENTLY IF EXISTS fees_code;
