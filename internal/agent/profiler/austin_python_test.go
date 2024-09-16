package profiler

import (
	"testing"

	"github.com/josepdcs/kubectl-prof/internal/agent/job"
	executil "github.com/josepdcs/kubectl-prof/internal/agent/util/exec"
	"github.com/josepdcs/kubectl-prof/internal/agent/util/publish"
)

func TestAustin_SetUp(t *testing.T) {
	// Create a new AustinPythonProfiler
	profiler := NewAustinPythonProfiler(
		executil.NewCommander(), publish.NewPublisher(),
	)
	// Check that the profiler is not nil
	if profiler == nil {
		t.Errorf("NewAustinPythonProfiler() = nil")
	}
	// Create a new ProfilingJob
	job := &job.ProfilingJob{
		PID: "501",
	}
	profiler.invoke(job, "501")
}
