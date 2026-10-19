-- Event clicks are kept 365 days from ts. MODIFY TTL is idempotent when the
-- same interval is already set. materialize_ttl_after_modify = 0 rewrites no
-- parts and starts no mutation.
ALTER TABLE kuztds.events
	MODIFY TTL ts + INTERVAL 365 DAY SETTINGS materialize_ttl_after_modify = 0;
