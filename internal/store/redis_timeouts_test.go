package store

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// TestEngineRedisTimeoutsDefaults checks the engine defaults without env.
func TestEngineRedisTimeoutsDefaults(t *testing.T) {
	for _, k := range []string{"DIAL", "READ", "WRITE", "POOL"} {
		t.Setenv("EMTDS_ENGINE_REDIS_"+k+"_TIMEOUT", "")
		t.Setenv("KUZTDS_ENGINE_REDIS_"+k+"_TIMEOUT", "")
	}
	got := EngineRedisTimeouts()
	want := RedisTimeouts{Dial: time.Second, Read: 200 * time.Millisecond, Write: 200 * time.Millisecond, Pool: 300 * time.Millisecond, DialRetries: 1}
	if got != want {
		t.Fatalf("defaults %+v, want %+v", got, want)
	}
}

// TestEngineRedisTimeoutsEnv checks overrides, the legacy prefix and rejected values.
func TestEngineRedisTimeoutsEnv(t *testing.T) {
	t.Setenv("EMTDS_ENGINE_REDIS_DIAL_TIMEOUT", "2s")
	t.Setenv("EMTDS_ENGINE_REDIS_READ_TIMEOUT", "")
	t.Setenv("KUZTDS_ENGINE_REDIS_READ_TIMEOUT", "500ms")
	t.Setenv("EMTDS_ENGINE_REDIS_WRITE_TIMEOUT", "fast")
	t.Setenv("EMTDS_ENGINE_REDIS_POOL_TIMEOUT", "-1s")
	got := EngineRedisTimeouts()
	want := RedisTimeouts{Dial: 2 * time.Second, Read: 500 * time.Millisecond, Write: EngineRedisWriteTimeout, Pool: EngineRedisPoolTimeout, DialRetries: EngineRedisDialRetries}
	if got != want {
		t.Fatalf("env %+v, want %+v", got, want)
	}
}

// TestRedisOptionsTimeouts checks that a zero RedisTimeouts keeps the
// go-redis defaults with a 2s connect and that set fields reach the client.
func TestRedisOptionsTimeouts(t *testing.T) {
	zero := redisOptions("r:6379", "", 0, RedisTimeouts{})
	if zero.DialTimeout != 2*time.Second || zero.ReadTimeout != 0 || zero.WriteTimeout != 0 || zero.PoolTimeout != 0 || zero.DialerRetries != 0 || zero.MaxRetries != -1 {
		t.Fatalf("zero options %+v", zero)
	}
	set := redisOptions("r:6379", "", 0, RedisTimeouts{Dial: time.Second, Read: 200 * time.Millisecond, Write: 150 * time.Millisecond, Pool: 300 * time.Millisecond, DialRetries: 1})
	if set.DialTimeout != time.Second || set.ReadTimeout != 200*time.Millisecond || set.WriteTimeout != 150*time.Millisecond || set.PoolTimeout != 300*time.Millisecond || set.DialerRetries != 1 {
		t.Fatalf("set options %+v", set)
	}
}

// TestCountersFailOpenOnSilentRedis checks that a Redis which accepts and
// never answers costs about the read timeout and the click stays unique.
func TestCountersFailOpenOnSilentRedis(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	defer func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	rdb := redis.NewClient(redisOptions(ln.Addr().String(), "", 0, RedisTimeouts{Dial: time.Second, Read: 50 * time.Millisecond, Write: 50 * time.Millisecond, Pool: 100 * time.Millisecond}))
	defer rdb.Close()
	c := NewCounters(rdb)
	start := time.Now()
	ok, err := c.Unique(context.Background(), "g", netip.MustParseAddr("192.0.2.1"), time.Minute)
	if took := time.Since(start); took > time.Second {
		t.Fatalf("silent redis held the call %v", took)
	}
	if !ok || err == nil {
		t.Fatalf("want fail-open with an error, got ok=%v err=%v", ok, err)
	}
}
