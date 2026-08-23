-- Data-only recovery migration. A rollback must not fabricate the original
-- rejection timestamp or mark successfully redelivered notifications failed.
SELECT 1;
