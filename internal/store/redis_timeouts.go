package store

import (
	"time"

	"github.com/egerkuzma/emtds/internal/config"
)

// Engine Redis defaults. A visitor request waits on Redis for uniqueness,
// the firewall and limits; a slow Redis must cost a fraction of a second
// and fall open, not hold the click for the go-redis default of 3s.
const (
	EngineRedisDialTimeout  = time.Second
	EngineRedisReadTimeout  = 200 * time.Millisecond
	EngineRedisWriteTimeout = 200 * time.Millisecond
	EngineRedisPoolTimeout  = 300 * time.Millisecond
	// EngineRedisDialRetries is one connect attempt per new connection: the
	// go-redis default of five would hold a click for five dial timeouts
	// while Redis is unreachable.
	EngineRedisDialRetries = 1
)

// RedisTimeouts bounds the network waits of one Redis client.
// Dial is one connect, Read and Write one command, Pool the wait for a free
// connection, DialRetries the connect attempts for one new connection.
// A zero field keeps the go-redis default for that wait.
type RedisTimeouts struct {
	Dial, Read, Write, Pool time.Duration
	DialRetries             int
}

// EngineRedisTimeouts returns the engine's Redis timeouts.
// Each default above is replaced by EMTDS_ENGINE_REDIS_DIAL_TIMEOUT,
// EMTDS_ENGINE_REDIS_READ_TIMEOUT, EMTDS_ENGINE_REDIS_WRITE_TIMEOUT or
// EMTDS_ENGINE_REDIS_POOL_TIMEOUT when that variable is a positive Go
// duration such as 250ms. An empty, malformed or non-positive value keeps
// the default. DialRetries is always EngineRedisDialRetries. It does not fail.
func EngineRedisTimeouts() RedisTimeouts {
	return RedisTimeouts{
		Dial:        envDuration("EMTDS_ENGINE_REDIS_DIAL_TIMEOUT", EngineRedisDialTimeout),
		Read:        envDuration("EMTDS_ENGINE_REDIS_READ_TIMEOUT", EngineRedisReadTimeout),
		Write:       envDuration("EMTDS_ENGINE_REDIS_WRITE_TIMEOUT", EngineRedisWriteTimeout),
		Pool:        envDuration("EMTDS_ENGINE_REDIS_POOL_TIMEOUT", EngineRedisPoolTimeout),
		DialRetries: EngineRedisDialRetries,
	}
}

// envDuration reads key through config.Env as a Go duration.
// def is returned when the value is empty, malformed or not positive.
func envDuration(key string, def time.Duration) time.Duration {
	d, err := time.ParseDuration(config.Env(key))
	if err != nil || d <= 0 {
		return def
	}
	return d
}
