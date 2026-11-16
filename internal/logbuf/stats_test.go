package logbuf

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestStatsConcurrentPushAndStats pushes from several goroutines while another
// reads Stats; under -race it proves the snapshot shares nothing unsafe with
// Push, Run and the writer. At the end every pushed event is either inserted or
// counted as lost and nothing is pending.
func TestStatsConcurrentPushAndStats(t *testing.T) {
	ins := &fakeInserter{}
	b := New(ins, 5000, 50, 10*time.Millisecond, quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	const workers, per = 8, 500
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				b.Push(Event{Stream: "s"})
			}
		}()
	}
	stop := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stop:
				return
			default:
				s := b.Stats()
				if s.Pending < 0 || s.InputCap != 5000 || s.QueueCap != 100 {
					t.Errorf("bad snapshot %+v", s)
					return
				}
			}
		}
	}()
	wg.Wait()
	waitFor(t, func() bool { return b.Stats().Pending == 0 && b.Stats().Inserted+b.Losses().Total() == workers*per })
	close(stop)
	<-readerDone

	s := b.Stats()
	if s.Inserted != int64(ins.Total()) || s.Inserted+s.Losses.Total() != workers*per {
		t.Fatalf("inserted %d lost %d of %d", s.Inserted, s.Losses.Total(), workers*per)
	}
	if s.InsertErrors != 0 || !s.FailingSince.IsZero() || s.LastInsertOK.IsZero() {
		t.Fatalf("healthy buffer reports %+v", s)
	}
}

// TestStatsPendingWhileWriterIsStuck holds the writer inside InsertEvents and
// checks that events in the batch being inserted, the queue and the
// accumulator all stay in Pending, then that Pending drains after the release.
func TestStatsPendingWhileWriterIsStuck(t *testing.T) {
	ins := newBlockingInserter()
	b := New(ins, 1000, 2, time.Hour, quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	b.Push(Event{Stream: "a"})
	b.Push(Event{Stream: "b"})
	select {
	case <-ins.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the writer never entered InsertEvents")
	}
	// Two full batches wait in the queue and one event stays in the accumulator.
	for i := 0; i < 5; i++ {
		b.Push(Event{Stream: "c"})
	}
	waitFor(t, func() bool { s := b.Stats(); return s.QueueLen == 2 && s.InputLen == 0 && s.Pending == 7 })
	s := b.Stats()
	if s.Inserted != 0 || !s.LastInsertOK.IsZero() {
		t.Fatalf("nothing is inserted yet: %+v", s)
	}

	close(ins.release)
	waitFor(t, func() bool { return b.Stats().Inserted == 6 })
	// The lone event leaves with the flush timer, which is an hour here: Close it.
	if err := b.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := b.Stats(); s.Pending != 0 || s.Inserted != 7 {
		t.Fatalf("after Close: %+v", s)
	}
}

// flakyInserter fails while fail is set and counts the events it accepted.
type flakyInserter struct {
	fail atomic.Bool
	ok   atomic.Int64
}

func (f *flakyInserter) InsertEvents(_ context.Context, batch []Event) error {
	if f.fail.Load() {
		return errors.New("clickhouse is down")
	}
	f.ok.Add(int64(len(batch)))
	return nil
}

// TestStatsFailureStreak checks the insert error counters: the streak starts at
// the first failure and keeps its start across further failures, the last
// successful insert time survives a failure, and one success ends the streak.
func TestStatsFailureStreak(t *testing.T) {
	ins := &flakyInserter{}
	b := New(ins, 100, 1, time.Hour, quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	b.Push(Event{Stream: "ok"})
	waitFor(t, func() bool { return b.Stats().Inserted == 1 })
	healthy := b.Stats()
	if healthy.LastInsertOK.IsZero() || !healthy.FailingSince.IsZero() || healthy.InsertErrors != 0 {
		t.Fatalf("after a good insert: %+v", healthy)
	}

	ins.fail.Store(true)
	b.Push(Event{Stream: "bad1"})
	waitFor(t, func() bool { return b.Stats().InsertErrors == 1 })
	first := b.Stats()
	if first.FailingSince.IsZero() || !first.LastInsertOK.Equal(healthy.LastInsertOK) {
		t.Fatalf("after the first failure: %+v", first)
	}
	if first.Losses.Insert != 1 {
		t.Fatalf("lost = %+v", first.Losses)
	}

	time.Sleep(5 * time.Millisecond)
	b.Push(Event{Stream: "bad2"})
	waitFor(t, func() bool { return b.Stats().InsertErrors == 2 })
	if got := b.Stats().FailingSince; !got.Equal(first.FailingSince) {
		t.Fatalf("the streak start moved: %v -> %v", first.FailingSince, got)
	}

	ins.fail.Store(false)
	b.Push(Event{Stream: "ok2"})
	waitFor(t, func() bool { return b.Stats().Inserted == 2 })
	end := b.Stats()
	if !end.FailingSince.IsZero() || !end.LastInsertOK.After(healthy.LastInsertOK) || end.InsertErrors != 2 {
		t.Fatalf("after recovery: %+v", end)
	}
}
