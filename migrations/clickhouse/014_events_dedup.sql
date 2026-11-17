-- Insert deduplication for events. The engine gives every batch an
-- idempotency token and repeats the batch when the answer was lost; the server
-- drops a block it has already accepted under the same token. A non-replicated
-- MergeTree remembers blocks only when this window is above zero (default 0).
-- 10000 blocks, about one batch a second, covers hours. MODIFY SETTING changes
-- table metadata only: no part is rewritten and no mutation starts. It is
-- idempotent; ensureSchema runs the same statement at engine start.
ALTER TABLE emtds.events
	MODIFY SETTING non_replicated_deduplication_window = 10000;
