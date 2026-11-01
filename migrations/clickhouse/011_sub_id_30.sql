-- sub_id_13 through sub_id_30. The engine does not add these columns.
-- ADD COLUMN IF NOT EXISTS stores the empty default in table metadata and
-- does not rewrite existing parts. Apply this file, then restart.
ALTER TABLE kuztds.events
	ADD COLUMN IF NOT EXISTS sub_id_13 String,
	ADD COLUMN IF NOT EXISTS sub_id_14 String,
	ADD COLUMN IF NOT EXISTS sub_id_15 String,
	ADD COLUMN IF NOT EXISTS sub_id_16 String,
	ADD COLUMN IF NOT EXISTS sub_id_17 String,
	ADD COLUMN IF NOT EXISTS sub_id_18 String,
	ADD COLUMN IF NOT EXISTS sub_id_19 String,
	ADD COLUMN IF NOT EXISTS sub_id_20 String,
	ADD COLUMN IF NOT EXISTS sub_id_21 String,
	ADD COLUMN IF NOT EXISTS sub_id_22 String,
	ADD COLUMN IF NOT EXISTS sub_id_23 String,
	ADD COLUMN IF NOT EXISTS sub_id_24 String,
	ADD COLUMN IF NOT EXISTS sub_id_25 String,
	ADD COLUMN IF NOT EXISTS sub_id_26 String,
	ADD COLUMN IF NOT EXISTS sub_id_27 String,
	ADD COLUMN IF NOT EXISTS sub_id_28 String,
	ADD COLUMN IF NOT EXISTS sub_id_29 String,
	ADD COLUMN IF NOT EXISTS sub_id_30 String;

ALTER TABLE kuztds.conversions
	ADD COLUMN IF NOT EXISTS sub_id_13 String,
	ADD COLUMN IF NOT EXISTS sub_id_14 String,
	ADD COLUMN IF NOT EXISTS sub_id_15 String,
	ADD COLUMN IF NOT EXISTS sub_id_16 String,
	ADD COLUMN IF NOT EXISTS sub_id_17 String,
	ADD COLUMN IF NOT EXISTS sub_id_18 String,
	ADD COLUMN IF NOT EXISTS sub_id_19 String,
	ADD COLUMN IF NOT EXISTS sub_id_20 String,
	ADD COLUMN IF NOT EXISTS sub_id_21 String,
	ADD COLUMN IF NOT EXISTS sub_id_22 String,
	ADD COLUMN IF NOT EXISTS sub_id_23 String,
	ADD COLUMN IF NOT EXISTS sub_id_24 String,
	ADD COLUMN IF NOT EXISTS sub_id_25 String,
	ADD COLUMN IF NOT EXISTS sub_id_26 String,
	ADD COLUMN IF NOT EXISTS sub_id_27 String,
	ADD COLUMN IF NOT EXISTS sub_id_28 String,
	ADD COLUMN IF NOT EXISTS sub_id_29 String,
	ADD COLUMN IF NOT EXISTS sub_id_30 String;
