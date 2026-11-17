-- root: first click id, stream and keyword carried through a conversion chain;
-- an empty root_cid means the click is its own root.
ALTER TABLE emtds.events ADD COLUMN IF NOT EXISTS root_cid String DEFAULT '';

ALTER TABLE emtds.conversions
	ADD COLUMN IF NOT EXISTS root_cid String DEFAULT '',
	ADD COLUMN IF NOT EXISTS origin_stream_name LowCardinality(String) DEFAULT '',
	ADD COLUMN IF NOT EXISTS origin_keyword String DEFAULT '';
