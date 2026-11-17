package config

import (
	"os"
	"strings"
)

const (
	// EnvPrefix starts the names of the product environment variables.
	EnvPrefix = "EMTDS_"
	// DefaultClickHouseDB is the ClickHouse database a new process opens.
	// A fresh ClickHouse init creates this database.
	DefaultClickHouseDB = "emtds"
	// DefaultClickHouseUser is the ClickHouse user created with DefaultClickHouseDB.
	DefaultClickHouseUser = "emtds"
)

// EnvSetupToken names the variable that holds the first-run token of the admin panel.
const EnvSetupToken = EnvPrefix + "SETUP_TOKEN"

// SetupToken returns the first-run token from EMTDS_SETUP_TOKEN without surrounding spaces.
// The result is empty when the variable is unset or blank: the first-run form then needs no
// token. The value is a secret and must not be logged.
func SetupToken() string {
	return strings.TrimSpace(Env(EnvSetupToken))
}

// Env returns the environment variable key, for example EMTDS_DATA_DIR.
// The result is empty when the variable is unset. There is no error.
func Env(key string) string {
	return os.Getenv(key)
}

// EnvOr returns Env(key), or def when that is empty.
func EnvOr(key, def string) string {
	if v := Env(key); v != "" {
		return v
	}
	return def
}
