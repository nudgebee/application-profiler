package profiler

import (
	"bufio"
	"context"
	"fmt"
	"net"
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

// fetchProfileFromPID fetches the pprof profile from the application's HTTP endpoint.
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
	targetURL := fmt.Sprintf("http://127.0.0.1:%s/debug/pprof/%s?seconds=%d",
		port, profileType, int(job.Interval.Seconds()),
	)

	// Shell out to nsenter + curl
	cmd := exec.Command(
		"nsenter", "-t", job.PID, "--",
		"curl", "-s", "--fail", targetURL,
	)

	// Capture stdout of the command directly into a file
	rawFile := common.GetResultFile(common.TmpDir(), job.Tool, api.Pprof, job.PID, job.Iteration)
	out, err := os.Create(rawFile)
	if err != nil {
		return errors.Wrap(err, "could not create profile file")
	}
	defer out.Close()

	cmd.Stdout = out
	cmd.Stderr = os.Stderr // so you’ll see any curl errors in your logs

	if err := cmd.Run(); err != nil {
		return errors.Wrapf(err, "failed to nsenter+curl %q", targetURL)
	}

	// Finally, publish the file as before
	return m.publisher.Do(job.Compressor, rawFile, job.OutputType)
}

// findListeningPortForPID discovers the HTTP pprof listening port for the given PID.
func findListeningPortForPID(pid string) (string, error) {
	// run lsof to list all LISTEN sockets for this PID
	cmd := exec.Command("nsenter", "-t", pid, "lsof", "-Pan", "-p", pid, "-iTCP", "-sTCP:LISTEN")
	output, err := cmd.Output()
	if err != nil {
		return "", errors.Wrap(err, "failed to run lsof")
	}

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, "LISTEN") {
			continue
		}
		fields := strings.Fields(line)
		for _, f := range fields {
			// attempt to split “host:port”
			host, port, err := net.SplitHostPort(f)
			if err != nil {
				continue
			}
			// sanity‐check that it really is a TCP listen port
			if host == "" && port == "" {
				continue
			}
			// Validate port before returning
			if _, err := strconv.Atoi(port); err == nil {
				return port, nil
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
