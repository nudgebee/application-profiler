package common

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nudgebee/application-profiler/api"
	"github.com/nudgebee/application-profiler/pkg/util/log"
	"github.com/pkg/errors"
)

// ProfilePIDs runs profile once for every PID, concurrently, starting each one
// delay after the previous so a container with many processes is not hit all
// at once. Each PID publishes its own result.
//
// The PIDs are the leaf processes of the container's process tree, which
// routinely include ones the tool cannot profile: a shell, a sidecar-style
// helper, a process that is not the target's runtime, one that exits
// mid-run. One of those failing must not throw away the profiles the others
// produced, so the run succeeds as long as at least one PID did, and a notice
// names the PIDs that were skipped and why. It fails only when every PID
// failed, with each one's reason.
func ProfilePIDs(pids []string, delay time.Duration, profile func(pid string) error) error {
	if len(pids) == 0 {
		// Nothing would be published, so succeeding here would end the run
		// without a result.
		return errors.New("no PIDs to profile")
	}

	errs := make([]error, len(pids))
	var wg sync.WaitGroup
	for i, pid := range pids {
		if i > 0 {
			// wait a bit between jobs for not overloading the system
			time.Sleep(delay)
		}
		wg.Go(func() { errs[i] = profile(pid) })
	}
	wg.Wait()

	var failed pidErrors
	for i, err := range errs {
		if err != nil {
			failed = append(failed, pidError{pid: pids[i], err: err})
		}
	}
	switch len(failed) {
	case 0:
		return nil
	case len(pids):
		return failed
	}

	_ = log.EventLn(api.Notice, &api.NoticeData{
		Time: time.Now(),
		Msg: fmt.Sprintf("Profiled %d of %d PIDs; skipped %s",
			len(pids)-len(failed), len(pids), failed.Error()),
	})
	return nil
}

// pidError is the reason one PID could not be profiled.
type pidError struct {
	pid string
	err error
}

// pidErrors reads "PID <n>: <reason>; PID <m>: <reason>".
type pidErrors []pidError

func (e pidErrors) Error() string {
	reasons := make([]string, len(e))
	for i, f := range e {
		reasons[i] = fmt.Sprintf("PID %s: %s", f.pid, f.err)
	}
	return strings.Join(reasons, "; ")
}

func (e pidErrors) Unwrap() []error {
	errs := make([]error, len(e))
	for i, f := range e {
		errs[i] = f.err
	}
	return errs
}
