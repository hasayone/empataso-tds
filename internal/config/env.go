package config

import (
	"os"
	"strings"
)

const (
	// EnvPrefix starts the names of the product environment variables.
	EnvPrefix = "EMTDS_"
	// LegacyEnvPrefix starts the names read when the EMTDS_ variable is empty.
	LegacyEnvPrefix = "KUZTDS_"
	// DefaultClickHouseDB is the physical ClickHouse database. Renaming it needs a data migration.
	DefaultClickHouseDB = "kuztds"
	// DefaultClickHouseUser is the physical ClickHouse user created with the database.
	DefaultClickHouseUser = "kuztds"
)

// Env returns the environment variable key, for example EMTDS_DATA_DIR.
// An empty or unset value of an EMTDS_ key falls back to the same name with the KUZTDS_ prefix,
// so servers that still carry the old names keep their settings.
// Keys without the EMTDS_ prefix are read as they are. The result is empty when nothing is set.
func Env(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	if rest, ok := strings.CutPrefix(key, EnvPrefix); ok {
		return os.Getenv(LegacyEnvPrefix + rest)
	}
	return ""
}

// EnvOr returns Env(key), or def when that is empty.
func EnvOr(key, def string) string {
	if v := Env(key); v != "" {
		return v
	}
	return def
}
