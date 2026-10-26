-- atlas:txmode none

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS fees_code_key ON fees (code);
