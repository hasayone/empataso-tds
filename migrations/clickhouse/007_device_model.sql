ALTER TABLE kuztds.events
	ADD COLUMN IF NOT EXISTS device_model LowCardinality(String);

ALTER TABLE kuztds.conversions
	ADD COLUMN IF NOT EXISTS device_model LowCardinality(String);
