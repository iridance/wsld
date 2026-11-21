package wsl

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIsTransient(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("boom"), false},
		{&ExitError{Code: 1}, false},
		{&ExitError{Code: 255}, false},
		{&ExitError{Code: 0xFFFFFFFF}, true},
		{&ExitError{Code: -1}, true},
		{errors.Join(errors.New("ctx"), &ExitError{Code: 0xFFFFFFFF}), true},
	}
	for _, c := range cases {
		if got := IsTransient(c.err); got != c.want {
			t.Errorf("IsTransient(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func attempts(results ...error) (func() ([]byte, []byte, error), *int) {
	n := 0
	return func() ([]byte, []byte, error) {
		i := n
		n++
		if i >= len(results) {
			i = len(results) - 1
		}
		return []byte("out"), nil, results[i]
	}, &n
}

func TestRetryTransientRecoversAfterFailure(t *testing.T) {
	fn, n := attempts(&ExitError{Code: 0xFFFFFFFF}, nil)
	slept := 0
	out, _, err := retryTransient(context.Background(), true, func(time.Duration) { slept++ }, fn)
	if err != nil || string(out) != "out" || *n != 2 || slept != 1 {
		t.Fatalf("err=%v attempts=%d slept=%d", err, *n, slept)
	}
}

func TestRetryTransientGivesUpAfterRetries(t *testing.T) {
	fn, n := attempts(&ExitError{Code: 0xFFFFFFFF})
	_, _, err := retryTransient(context.Background(), true, func(time.Duration) {}, fn)
	if !IsTransient(err) || *n != 1+transientRetries {
		t.Fatalf("err=%v attempts=%d", err, *n)
	}
}

func TestRetryTransientDoesNotRetryRealCommandFailure(t *testing.T) {
	fn, n := attempts(&ExitError{Code: 1})
	_, _, err := retryTransient(context.Background(), true, func(time.Duration) {}, fn)
	if err == nil || *n != 1 {
		t.Fatalf("err=%v attempts=%d", err, *n)
	}
}

func TestRetryTransientSkipsWhenStdinConsumed(t *testing.T) {
	fn, n := attempts(&ExitError{Code: 0xFFFFFFFF}, nil)
	_, _, err := retryTransient(context.Background(), false, func(time.Duration) {}, fn)
	if !IsTransient(err) || *n != 1 {
		t.Fatalf("err=%v attempts=%d", err, *n)
	}
}

func TestRetryTransientStopsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fn, n := attempts(&ExitError{Code: 0xFFFFFFFF}, nil)
	_, _, _ = retryTransient(ctx, true, func(time.Duration) {}, fn)
	if *n != 1 {
		t.Fatalf("attempts=%d", *n)
	}
}
