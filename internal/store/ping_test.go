package store

import (
	"context"
	"errors"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// pingConn is a driver.Conn whose Ping returns err; no other method is implemented.
type pingConn struct {
	driver.Conn
	err error
}

func (p pingConn) Ping(context.Context) error { return p.err }

// TestCHPing checks that Ping reports a missing connection, passes the driver error through
// and returns nil for a server that answers.
func TestCHPing(t *testing.T) {
	ctx := context.Background()
	if err := (*CH)(nil).Ping(ctx); err == nil {
		t.Error("nil CH must fail")
	}
	if err := (&CH{}).Ping(ctx); err == nil {
		t.Error("CH without a connection must fail")
	}
	boom := errors.New("boom")
	if err := (&CH{conn: pingConn{err: boom}}).Ping(ctx); !errors.Is(err, boom) {
		t.Errorf("driver error = %v, want boom", err)
	}
	if err := (&CH{conn: pingConn{}}).Ping(ctx); err != nil {
		t.Errorf("answering server: %v", err)
	}
}

// TestRedisSessionsPing checks that Ping succeeds against a live Redis, fails once it is gone,
// and does not panic on a nil store or a store without a client.
func TestRedisSessionsPing(t *testing.T) {
	ctx := context.Background()
	if err := (*RedisSessions)(nil).Ping(ctx); err == nil {
		t.Error("nil store must fail")
	}
	if err := (&RedisSessions{}).Ping(ctx); err == nil {
		t.Error("store without a client must fail")
	}
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	ss := NewRedisSessions(rdb)
	if err := ss.Ping(ctx); err != nil {
		t.Fatalf("live redis: %v", err)
	}
	mr.Close()
	if err := ss.Ping(ctx); err == nil {
		t.Error("closed redis must fail")
	}
}
