ALTER TABLE emtds.events
	ADD COLUMN IF NOT EXISTS device_model LowCardinality(String);

ALTER TABLE emtds.conversions
	ADD COLUMN IF NOT EXISTS device_model LowCardinality(String);
