package store

import (
	"bufio"
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisProbeTimeout bounds every network wait of the memory probe.
const redisProbeTimeout = 2 * time.Second

// RedisMemory is the part of Redis INFO the memory alert reads.
// Used is used_memory and Max is maxmemory in bytes; Max 0 means no limit.
// OOMErrors is the errorstat_OOM count (commands refused for memory) and
// Rejected is rejected_connections.
type RedisMemory struct {
	Used      uint64
	Max       uint64
	OOMErrors uint64
	Rejected  uint64
}

// ParseRedisMemory reads RedisMemory from the text of INFO.
// Absent counters are zero. It returns an error when used_memory is missing,
// which means the text is not an INFO reply.
func ParseRedisMemory(info string) (RedisMemory, error) {
	var m RedisMemory
	seen := false
	sc := bufio.NewScanner(strings.NewReader(info))
	for sc.Scan() {
		key, val, ok := strings.Cut(strings.TrimSpace(sc.Text()), ":")
		if !ok {
			continue
		}
		switch key {
		case "used_memory":
			m.Used, seen = parseUint(val), true
		case "maxmemory":
			m.Max = parseUint(val)
		case "rejected_connections":
			m.Rejected = parseUint(val)
		case "errorstat_OOM":
			if n, found := strings.CutPrefix(val, "count="); found {
				m.OOMErrors = parseUint(n)
			}
		}
	}
	if !seen {
		return RedisMemory{}, errors.New("redis: INFO has no used_memory")
	}
	return m, nil
}

// parseUint reads a decimal counter; anything else is zero.
func parseUint(s string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	return n
}

// RedisProbe reads Redis memory figures for the memory alert.
type RedisProbe struct{ rdb redis.UniversalClient }

// OpenRedisProbe returns a probe for addr with password. The client connects
// on first use and every wait is bounded by redisProbeTimeout, so a Redis that
// is down fails one check instead of blocking the caller. It does not fail.
func OpenRedisProbe(addr, password string) *RedisProbe {
	return &RedisProbe{rdb: redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DialTimeout:  redisProbeTimeout,
		ReadTimeout:  redisProbeTimeout,
		WriteTimeout: redisProbeTimeout,
		PoolSize:     1,
		MaxRetries:   -1,
	})}
}

// Memory runs INFO and returns the memory figures.
// It returns the command error or the ParseRedisMemory error.
func (p *RedisProbe) Memory(ctx context.Context) (RedisMemory, error) {
	info, err := p.rdb.Info(ctx).Result()
	if err != nil {
		return RedisMemory{}, err
	}
	return ParseRedisMemory(info)
}

// Close releases the probe connection.
func (p *RedisProbe) Close() error { return p.rdb.Close() }
