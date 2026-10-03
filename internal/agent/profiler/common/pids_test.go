package common

import (
	"bufio"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/nudgebee/application-profiler/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureEvents returns the events printed on stdout while fn runs.
func captureEvents(t *testing.T, fn func()) []string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()

	var out []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			out = append(out, scanner.Text())
		}
		_, _ = io.Copy(io.Discard, r)
	}()
	fn()
	require.NoError(t, w.Close())
	<-done
	return out
}

func notices(t *testing.T, events []string) []string {
	t.Helper()
	var msgs []string
	for _, e := range events {
		data, err := api.ParseEvent(e)
		require.NoError(t, err)
		if n, ok := data.(*api.NoticeData); ok {
			msgs = append(msgs, n.Msg)
		}
	}
	return msgs
}

// profileWith fakes a per-PID profile: a PID listed in failures fails with
// that reason, any other succeeds. It records which PIDs ran.
func profileWith(failures map[string]string) (func(string) error, func() []string) {
	var mu sync.Mutex
	var ran []string
	return func(pid string) error {
			mu.Lock()
			ran = append(ran, pid)
			mu.Unlock()
			if reason, ok := failures[pid]; ok {
				return errors.New(reason)
			}
			return nil
		}, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), ran...)
		}
}

func TestProfilePIDs(t *testing.T) {
	t.Run("one PID failing does not fail the run, and is reported in a notice", func(t *testing.T) {
		profile, ran := profileWith(map[string]string{"8": "not a Python process"})
		var err error

		events := captureEvents(t, func() {
			err = ProfilePIDs([]string{"7", "8", "9"}, 0, profile)
		})

		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"7", "8", "9"}, ran(), "every PID should be tried")
		assert.Equal(t, []string{"Profiled 2 of 3 PIDs; skipped PID 8: not a Python process"}, notices(t, events))
	})

	t.Run("every PID failing fails the run with each reason", func(t *testing.T) {
		profile, ran := profileWith(map[string]string{"7": "permission denied", "8": "no such process"})
		var err error

		events := captureEvents(t, func() {
			err = ProfilePIDs([]string{"7", "8"}, 0, profile)
		})

		require.Error(t, err)
		assert.EqualError(t, err, "PID 7: permission denied; PID 8: no such process")
		assert.ElementsMatch(t, []string{"7", "8"}, ran())
		assert.Empty(t, notices(t, events), "a failed run reports through its error, not a notice")
	})

	t.Run("the per-PID errors stay reachable", func(t *testing.T) {
		cause := errors.New("cause")
		err := ProfilePIDs([]string{"7"}, 0, func(string) error { return cause })

		assert.EqualError(t, err, "PID 7: cause")
		assert.ErrorIs(t, err, cause)
	})

	t.Run("every PID succeeding needs no notice", func(t *testing.T) {
		profile, ran := profileWith(nil)
		var err error

		events := captureEvents(t, func() {
			err = ProfilePIDs([]string{"7", "8"}, 0, profile)
		})

		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"7", "8"}, ran())
		assert.Empty(t, notices(t, events))
	})

	t.Run("no PIDs is a failure, not a run without a result", func(t *testing.T) {
		profile, ran := profileWith(nil)

		err := ProfilePIDs(nil, 0, profile)

		assert.EqualError(t, err, "no PIDs to profile")
		assert.Empty(t, ran())
	})

	t.Run("PIDs are started delay apart but run concurrently", func(t *testing.T) {
		const delay = 50 * time.Millisecond
		var mu sync.Mutex
		started := map[string]time.Time{}
		startedLate := false
		release := make(chan struct{})
		go func() {
			// Hold every PID until well after the last should have started: a
			// sequential run would not start the second until this fires.
			time.Sleep(5 * delay)
			close(release)
		}()

		err := ProfilePIDs([]string{"7", "8", "9"}, delay, func(pid string) error {
			mu.Lock()
			started[pid] = time.Now()
			select {
			case <-release:
				startedLate = true
			default:
			}
			mu.Unlock()
			<-release
			return nil
		})

		require.NoError(t, err)
		require.Len(t, started, 3)
		assert.False(t, startedLate, "every PID should start while the others are still running")
		// Scheduling jitter can shave a little off the gap between two
		// goroutines starting; the stagger itself is a sleep of delay.
		assert.Greater(t, started["8"].Sub(started["7"]), delay/2)
		assert.Greater(t, started["9"].Sub(started["8"]), delay/2)
	})
}
