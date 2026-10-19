-- origin: first-entry campaign on each click and conversion (defaults to the
-- entry group when the visitor did not arrive via an origin token).
ALTER TABLE kuztds.events
	ADD COLUMN IF NOT EXISTS origin_group_name LowCardinality(String) DEFAULT group_name,
	ADD COLUMN IF NOT EXISTS origin_group_id LowCardinality(String) DEFAULT group_id;

ALTER TABLE kuztds.conversions
	ADD COLUMN IF NOT EXISTS origin_group_name LowCardinality(String) DEFAULT group_name,
	ADD COLUMN IF NOT EXISTS origin_group_id LowCardinality(String) DEFAULT group_id;
