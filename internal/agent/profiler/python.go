package profiler

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"time"

	"github.com/agrison/go-commons-lang/stringUtils"
	"github.com/nudgebee/application-profiler/api"
	"github.com/nudgebee/application-profiler/internal/agent/config"
	"github.com/nudgebee/application-profiler/internal/agent/job"
	"github.com/nudgebee/application-profiler/internal/agent/profiler/common"
	"github.com/nudgebee/application-profiler/internal/agent/util"
	executil "github.com/nudgebee/application-profiler/internal/agent/util/exec"
	"github.com/nudgebee/application-profiler/internal/agent/util/flamegraph"
	"github.com/nudgebee/application-profiler/internal/agent/util/publish"
	"github.com/nudgebee/application-profiler/pkg/util/file"
	"github.com/nudgebee/application-profiler/pkg/util/log"
	"github.com/pkg/errors"
)

const (
	pySpyLocation         = "/app/py-spy"
	pySpyDelayBetweenJobs = 2 * time.Second
)

var pythonCommand = func(commander executil.Commander, job *job.ProfilingJob, pid string, fileName string) *exec.Cmd {
	output := job.OutputType
	if job.OutputType == api.FlameGraph {
		// overrides to Raw
		output = api.Raw
	}

	switch output {
	case api.FlameGraph, api.SpeedScope, api.Raw:
		interval := strconv.Itoa(int(job.Interval.Seconds()))
		args := []string{"record"}
		args = append(args, "-p", pid, "-o", fileName, "-d", interval, "-s", "-t", "-f", string(output))
		return commander.Command(pySpyLocation, args...)
	// api.ThreadDump:
	default:
		args := []string{"dump"}
		args = append(args, "-p", pid)
		return commander.Command(pySpyLocation, args...)
	}
}

type PythonProfiler struct {
	targetPIDs []string
	delay      time.Duration
	PythonManager
}

type PythonManager interface {
	invoke(*job.ProfilingJob, string) (error, time.Duration)
	handleFlamegraph(*job.ProfilingJob, flamegraph.FrameGrapher, string, string) error
}

type pythonManager struct {
	commander executil.Commander
	publisher publish.Publisher
}

func NewPythonProfiler(commander executil.Commander, publisher publish.Publisher) *PythonProfiler {
	return &PythonProfiler{
		delay: pySpyDelayBetweenJobs,
		PythonManager: &pythonManager{
			commander: commander,
			publisher: publisher,
		},
	}
}

func (p *PythonProfiler) SetUp(job *job.ProfilingJob) error {
	if stringUtils.IsNotBlank(job.PID) {
		p.targetPIDs = []string{job.PID}
		return nil
	}
	pids, err := util.GetCandidatePIDs(job)
	if err != nil {
		return err
	}
	log.DebugLogLn(fmt.Sprintf("The PIDs to be profiled: %s", pids))
	p.targetPIDs = pids

	return nil
}

func (p *PythonProfiler) Invoke(job *job.ProfilingJob) (error, time.Duration) {
	start := time.Now()
	err := common.ProfilePIDs(p.targetPIDs, p.delay, func(pid string) error {
		err, _ := p.invoke(job, pid)
		return err
	})
	return err, time.Since(start)
}

func (p *pythonManager) invoke(job *job.ProfilingJob, pid string) (error, time.Duration) {
	start := time.Now()

	var out bytes.Buffer
	var stderr bytes.Buffer

	fileName := common.GetResultFile(common.TmpDir(), job.Tool, job.OutputType, pid, job.Iteration)
	if job.OutputType == api.FlameGraph {
		fileName = common.GetResultFile(common.TmpDir(), job.Tool, api.Raw, pid, job.Iteration)
	}
	cmd := pythonCommand(p.commander, job, pid, fileName)
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		log.ErrorLogLn(out.String())
		return errors.Wrapf(err, "could not launch profiler: %s", stderr.String()), time.Since(start)
	}

	// result file name is composed by the job info and the pid
	resultFileName := common.GetResultFile(common.TmpDir(), job.Tool, job.OutputType, pid, job.Iteration)
	if job.OutputType == api.ThreadDump {
		file.Write(resultFileName, out.String())
	} else {
		err = p.handleFlamegraph(job, flamegraph.Get(job), fileName, resultFileName)
		if err != nil {
			// Not nil: nothing is published for this PID, and a run that
			// ends without a result must not report success.
			return errors.Wrap(err, "could not generate flamegraph"), time.Since(start)
		}
	}

	return p.publisher.Do(job.Compressor, resultFileName, job.OutputType), time.Since(start)
}

func (p *pythonManager) handleFlamegraph(job *job.ProfilingJob, flameGrapher flamegraph.FrameGrapher,
	rawFileName string, flameFileName string) error {
	if job.OutputType == api.FlameGraph {
		if file.IsEmpty(rawFileName) {
			return errors.New("unable to generate flamegraph: no stacks found (maybe due low cpu load)")
		}
		// convert raw format to flamegraph
		err := flameGrapher.StackSamplesToFlameGraph(rawFileName, flameFileName)
		if err != nil {
			return errors.Wrap(err, "could not convert raw format to flamegraph")
		}
	}
	return nil
}

func (p *PythonProfiler) CleanUp(*job.ProfilingJob) error {
	file.RemoveAll(common.TmpDir(), config.ProfilingPrefix)

	return nil
}
