package profiler

import (
	"testing"
	"time"

	"github.com/nudgebee/application-profiler/internal/agent/job"
	executil "github.com/nudgebee/application-profiler/internal/agent/util/exec"
)

func TestAustin(t *testing.T) {
	commander := executil.NewCommander()

	austinProfiler := NewAustinPythonProfiler(commander, nil)

	job := &job.ProfilingJob{Duration: 2, PID: "49533", Interval: 10 * time.Second}
	austinProfiler.SetUp(job)
	austinProfiler.Invoke(job)
	time.Sleep(300 * time.Second)
}
