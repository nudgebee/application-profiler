package profiler

import (
	_ "net/http/pprof"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/josepdcs/kubectl-prof/api"
	"github.com/josepdcs/kubectl-prof/internal/agent/job"
	common "github.com/josepdcs/kubectl-prof/internal/agent/profiler/common"
	executil "github.com/josepdcs/kubectl-prof/internal/agent/util/exec"
	"github.com/josepdcs/kubectl-prof/pkg/util/compressor"
)

type dummyPublisher struct{}

func (d *dummyPublisher) Do(compressor compressor.Type, path string, outputType api.OutputType) error {
	return nil
}

func (d *dummyPublisher) DoWithNativeGzipAndSplit(compressor string, path string, outputType api.OutputType) error {
	return nil
}

func TestGoPprofProfiler_Invoke(t *testing.T) {
	// Start a pprof HTTP server
	// go http.ListenAndServe("127.0.0.1:6060", nil)
	// time.Sleep(100 * time.Millisecond)

	pid := strconv.Itoa(82390)
	commander := executil.NewFakeCommander()
	p := NewGoPprofProfiler(commander, &dummyPublisher{})

	job := &job.ProfilingJob{
		PID:        pid,
		Interval:   1 * time.Second,
		OutputType: api.FlameGraph,
		Tool:       "go",
		Iteration:  1,
		Compressor: "",
	}

	if err := p.SetUp(job); err != nil {
		t.Fatalf("SetUp failed: %v", err)
	}

	if err, duration := p.Invoke(job); err != nil {
		t.Fatalf("Invoke failed: %v", err)
	} else if duration < job.Interval {
		t.Errorf("expected duration >= %v; got %v", job.Interval, duration)
	}

	// Verify raw profile file exists
	rawFile := common.GetResultFile(common.TmpDir(), job.Tool, api.Pprof, job.PID, job.Iteration)
	if _, err := os.Stat(rawFile); os.IsNotExist(err) {
		t.Errorf("raw profile file not found: %s", rawFile)
	}
}
