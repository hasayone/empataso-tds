package store

import (
	"errors"
	"testing"
	"time"
)

func TestRetryReturnsOnFirstSuccess(t *testing.T) {
	n := 0
	err := Retry(5, time.Hour, func() error {
		n++
		if n == 1 {
			return nil
		}
		return errors.New("later")
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("calls %d", n)
	}
}

func TestRetryWithinReturnsOnFirstSuccess(t *testing.T) {
	n := 0
	err := RetryWithin(time.Hour, time.Hour, func() error {
		n++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("calls %d", n)
	}
}

func TestRetryWithinStopsWhenTheBudgetPasses(t *testing.T) {
	n := 0
	err := RetryWithin(40*time.Millisecond, time.Millisecond, func() error {
		n++
		return errors.New("down")
	})
	if err == nil || err.Error() != "down" {
		t.Fatalf("err %v", err)
	}
	if n < 1 {
		t.Fatal("no attempt")
	}
}

func TestRetryReturnsLastErrorWhenExhausted(t *testing.T) {
	n := 0
	err := Retry(3, 0, func() error {
		n++
		return errors.New("down")
	})
	if err == nil || err.Error() != "down" {
		t.Fatalf("err %v", err)
	}
	if n != 3 {
		t.Fatalf("calls %d", n)
	}
}
