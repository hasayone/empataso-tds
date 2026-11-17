-- step: 0 is the first step of a visit (a landing or a direct offer), 1 is the
-- /offer step after a landing. Campaign unique counts include only step 0.
-- A constant DEFAULT is kept in the table metadata: ADD COLUMN rewrites no
-- parts and starts no mutation, and rows written earlier read as 0.
ALTER TABLE emtds.events
	ADD COLUMN IF NOT EXISTS step UInt8 DEFAULT 0;
