-- Profit as Float64 (no dependency on decimal in the Go driver).
ALTER TABLE emtds.postbacks MODIFY COLUMN profit Float64;
