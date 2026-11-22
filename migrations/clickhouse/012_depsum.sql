ALTER TABLE emtds.conversions
	ADD COLUMN IF NOT EXISTS depsum Float64,
	ADD COLUMN IF NOT EXISTS dep2sum Float64;
