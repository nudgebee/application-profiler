package profiler

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/agrison/go-commons-lang/stringUtils"
	"github.com/alitto/pond"
	"github.com/josepdcs/kubectl-prof/api"
	"github.com/josepdcs/kubectl-prof/internal/agent/config"
	"github.com/josepdcs/kubectl-prof/internal/agent/job"
	common "github.com/josepdcs/kubectl-prof/internal/agent/profiler/common"
	"github.com/josepdcs/kubectl-prof/internal/agent/util"
	executil "github.com/josepdcs/kubectl-prof/internal/agent/util/exec"
	"github.com/josepdcs/kubectl-prof/internal/agent/util/publish"
	"github.com/josepdcs/kubectl-prof/pkg/util/file"
	"github.com/josepdcs/kubectl-prof/pkg/util/log"
	"github.com/pkg/errors"
)

// GoPprofProfiler uses Go's pprof HTTP server to collect profiles.
type GoPprofProfiler struct {
	manager    *goPprofManager
	targetPIDs []string
}

type goPprofManager struct {
	commander executil.Commander
	publisher publish.Publisher
}

// NewGoPprofProfiler creates a new profiler with the given publisher.
func NewGoPprofProfiler(commander executil.Commander, publisher publish.Publisher) *GoPprofProfiler {
	return &GoPprofProfiler{manager: &goPprofManager{commander: commander, publisher: publisher}}
}

// SetUp ensures the job has a valid PID.
func (p *GoPprofProfiler) SetUp(job *job.ProfilingJob) error {
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

// Invoke runs the profiling job and returns execution time.
func (p *GoPprofProfiler) Invoke(job *job.ProfilingJob) (error, time.Duration) {
	start := time.Now()
	pool := pond.New(len(p.targetPIDs), 0, pond.MinWorkers(len(p.targetPIDs)))
	defer pool.StopAndWait()
	// create a task group associated to a context
	group, _ := pool.GroupContext(context.Background())
	// submit tasks to profile
	for _, pid := range p.targetPIDs {
		pid := pid
		group.Submit(func() error {
			job.PID = pid
			err := p.manager.fetchProfileFromPID(job)
			return err
		})
		// wait a bit between jobs for not overloading the system
		time.Sleep(2 * time.Second)
	}
	// wait for all tasks to finish
	err := group.Wait()

	return err, time.Since(start)
}

func (m *goPprofManager) fetchProfileFromPID(job *job.ProfilingJob) error {
	port, err := findListeningPortForPID(job.PID)
	if err != nil {
		log.ErrorLogLn(fmt.Sprintf("failed to find listening port for PID %s: %s", job.PID, err))
		port = "8080"
		log.DebugLogLn(fmt.Sprintf("using default port %s", port))
	}

	profileType := "profile"
	if job.OutputType == api.HeapDump {
		profileType = "heap"
	}

	// Build the real HTTP URL
	targetURL := fmt.Sprintf(
		"http://127.0.0.1:%s/debug/pprof/%s?seconds=%d",
		port, profileType, int(job.Interval.Seconds()),
	)

	// Shell out to nsenter + wget
	cmd := exec.Command(
		"nsenter", "-t", job.PID, "-n",
		"wget", "-qO", "-", targetURL,
	)

	// Capture stdout of the command directly into a file
	rawFile := common.GetResultFile(common.TmpDir(), job.Tool, api.Pprof, job.PID, job.Iteration)
	out, err := os.Create(rawFile)
	if err != nil {
		return errors.Wrap(err, "could not create profile file")
	}
	defer out.Close()

	cmd.Stdout = out
	cmd.Stderr = os.Stderr // so you’ll see any wget errors in your logs

	if err := cmd.Run(); err != nil {
		return errors.Wrapf(err, "failed to nsenter+wget %q", targetURL)
	}

	if job.OutputType == api.FlameGraph {
		svgFile := rawFile + ".svg"
		if err := m.generateFlamegraph(rawFile, svgFile); err != nil {
			return err
		}
		return m.publisher.Do(job.Compressor, svgFile, job.OutputType)
	}

	// Finally, publish the file as before
	return m.publisher.Do(job.Compressor, rawFile, job.OutputType)
}

func findListeningPortForPID(pid string) (string, error) {
	// nsenter into the PID's network namespace and list listening TCP sockets
	cmd := exec.Command("nsenter", "-t", pid, "-n", "ss", "-tulnp")
	output, err := cmd.Output()
	if err != nil {
		return "", errors.Wrap(err, "failed to run ss")
	}

	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, "LISTEN") {
			continue
		}
		// optionally: ensure the line really belongs to our PID
		if !strings.Contains(line, pid) {
			continue
		}
		for _, field := range strings.Fields(line) {
			// look for any field containing a colon
			if idx := strings.LastIndex(field, ":"); idx > 0 && idx < len(field)-1 {
				portPart := field[idx+1:]
				if _, err := strconv.Atoi(portPart); err == nil {
					return portPart, nil
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", errors.New("no listening port found for PID")
}

// CleanUp removes temporary profiling files.
func (p *GoPprofProfiler) CleanUp(*job.ProfilingJob) error {
	file.RemoveAll(common.TmpDir(), config.ProfilingPrefix)
	return nil
}

func (m *goPprofManager) generateFlamegraph(rawFile, svgFile string) error {

	cmd := exec.Command(
		"pprof",
		"-svg",
		"-output", svgFile,
		rawFile,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return errors.Wrapf(err, "failed to generate flamegraph: %s", string(out))
	}
	return nil
}
