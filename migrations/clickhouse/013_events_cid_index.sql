-- Skip index for click lookups by cid (postback, journal, import, conditions).
-- One bloom filter per 8192-row granule: cid is unique per click and spread
-- over the whole table, so a coarser granularity would only widen each read.
-- New parts get the index on insert and merge. Old parts are indexed by the
-- separate manual MATERIALIZE INDEX step from docs/wiki, never from here.
ALTER TABLE emtds.events
	ADD INDEX IF NOT EXISTS idx_cid cid TYPE bloom_filter(0.01) GRANULARITY 1;
