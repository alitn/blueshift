-- Record the size the client declared for the master at episode creation so
-- upload-complete can reject a short/aborted upload by comparing it against the
-- stored object's actual size (blob stat). Additive, nullable: existing rows and
-- older code paths that never set it stay valid.
ALTER TABLE episodes ADD COLUMN master_size_bytes bigint;
