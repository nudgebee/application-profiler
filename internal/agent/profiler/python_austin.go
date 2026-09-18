package profiler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/agrison/go-commons-lang/stringUtils"
	"github.com/alitto/pond"
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
	austinLocation = "austin"
	// mojo2austinLocation converts austin's binary MOJO output back to the
	// line-per-sample text format. austin 4 dropped the text writer, and the
	// text format is what the raw artefact and flamegraph.pl both consume.
	mojo2austinLocation = "mojo2austin"
	// austinInterruptedExitCode is what austin returns when sampling ends on
	// a signal — `retval = -interrupt_signal` in its main(), so SIGINT (2)
	// surfaces as 254. It ends every exposure-limited (-x) run, including the
	// successful ones, so it can't be treated as a failure on its own.
	austinInterruptedExitCode = 254
)

// mojoMagic prefixes austin's binary output format.
var mojoMagic = []byte{'M', 'O', 'J', 3}

var austinPythonCommand = func(commander executil.Commander, job *job.ProfilingJob, pid string, fileName string) *exec.Cmd {
	interval := strconv.Itoa(int(job.Interval.Seconds()))
	args := []string{}
	args = append(args, "-p", pid, "-o", fileName, "-x", interval, "-m")
	return commander.Command(austinLocation, args...)
}

type AustinPythonProfiler struct {
	targetPIDs []string
	delay      time.Duration
	AustinPythonManager
}

type AustinPythonManager interface {
	invoke(*job.ProfilingJob, string) (error, time.Duration)
	handleFlamegraph(*job.ProfilingJob, flamegraph.FrameGrapher, string, string) error
}

type austinPythonManager struct {
	commander executil.Commander
	publisher publish.Publisher
}

func NewAustinPythonProfiler(commander executil.Commander, publisher publish.Publisher) *AustinPythonProfiler {
	return &AustinPythonProfiler{
		delay: pySpyDelayBetweenJobs,
		AustinPythonManager: &austinPythonManager{
			commander: commander,
			publisher: publisher,
		},
	}
}

func (p *AustinPythonProfiler) SetUp(job *job.ProfilingJob) error {
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

func (p *AustinPythonProfiler) Invoke(job *job.ProfilingJob) (error, time.Duration) {
	start := time.Now()

	pool := pond.New(len(p.targetPIDs), 0, pond.MinWorkers(len(p.targetPIDs)))
	defer pool.StopAndWait()

	// create a task group associated to a context
	group, _ := pool.GroupContext(context.Background())

	// submit tasks to profile
	for _, pid := range p.targetPIDs {
		pid := pid
		group.Submit(func() error {
			err, _ := p.invoke(job, pid)
			return err
		})
		// wait a bit between jobs for not overloading the system
		time.Sleep(p.delay)
	}

	// wait for all tasks to finish
	err := group.Wait()

	return err, time.Since(start)
}

func (p *austinPythonManager) invoke(job *job.ProfilingJob, pid string) (error, time.Duration) {
	start := time.Now()

	var out bytes.Buffer
	var stderr bytes.Buffer
	fileName := common.GetResultFile(common.TmpDir(), job.Tool, job.OutputType, pid, job.Iteration)
	if job.OutputType == api.FlameGraph {
		fileName = common.GetResultFile(common.TmpDir(), job.Tool, api.Raw, pid, job.Iteration)
	}
	cmd := austinPythonCommand(p.commander, job, pid, fileName)
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil && !austinStoppedOnSignal(err) {
		log.ErrorLogLn(out.String())
		return errors.Wrapf(err, "could not launch profiler: %s", stderr.String()), time.Since(start)
	}

	// austin 4 always writes the binary MOJO format; convert it back to the
	// text format the raw artefact and flamegraph.pl are built around.
	if err := p.convertMojo(fileName); err != nil {
		return errors.Wrap(err, "could not convert the profile to text"), time.Since(start)
	}

	// austin writes its samples to the -o file, so an empty one means it
	// attached but read nothing — report that instead of publishing a
	// zero-byte artefact as a success.
	if file.IsEmpty(fileName) {
		return errors.Errorf("no samples collected (PID: %s): %s", pid, stderr.String()), time.Since(start)
	}

	// result file name is composed by the job info and the pid
	resultFileName := common.GetResultFile(common.TmpDir(), job.Tool, job.OutputType, pid, job.Iteration)
	if job.OutputType == api.FlameGraph {
		err = p.handleFlamegraph(job, flamegraph.Get(job), fileName, resultFileName)
		if err != nil {
			log.ErrorLogLn(fmt.Sprintf("could not generate flamegraph (PID: %s): %s", pid, err.Error()))
			return nil, time.Since(start)
		}
	}
	// Nothing to do for the other output types: austin has already written
	// the samples to resultFileName (fileName == resultFileName there).
	// Copying cmd stdout over it, as this used to, truncated every non-
	// flamegraph profile to zero bytes — austin prints nothing on stdout
	// when -o is given.

	return p.publisher.Do(job.Compressor, resultFileName, job.OutputType), time.Since(start)
}

// austinStoppedOnSignal reports whether austin exited the way an
// exposure-limited (-x) run always does: sampling ends on a signal and
// austin returns -SIGINT. The samples are already on disk at that point.
func austinStoppedOnSignal(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == austinInterruptedExitCode
}

// convertMojo rewrites fileName in place when it holds austin's binary MOJO
// output. A no-op for the text format, so it is safe across austin versions.
func (p *austinPythonManager) convertMojo(fileName string) error {
	f, err := os.Open(fileName) //nolint:gosec // path built by us from TmpDir
	if err != nil {
		return err
	}
	header := make([]byte, len(mojoMagic))
	n, _ := io.ReadFull(f, header)
	_ = f.Close()
	if !bytes.Equal(header[:n], mojoMagic) {
		return nil
	}
	textFileName := fileName + ".txt"
	if err := p.commander.Command(mojo2austinLocation, fileName, textFileName).Run(); err != nil {
		return err
	}
	return os.Rename(textFileName, fileName)
}

func (p *austinPythonManager) handleFlamegraph(job *job.ProfilingJob, flameGrapher flamegraph.FrameGrapher,
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

func (p *AustinPythonProfiler) CleanUp(*job.ProfilingJob) error {
	file.RemoveAll(common.TmpDir(), config.ProfilingPrefix)

	return nil
}
