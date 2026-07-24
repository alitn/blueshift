-- Revert 0011: drop the moment source column.
ALTER TABLE moments DROP COLUMN IF EXISTS source;
