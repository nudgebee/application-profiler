package profiler

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
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
	commonLog "github.com/josepdcs/kubectl-prof/pkg/util/log"
	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
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
	commonLog.DebugLogLn(fmt.Sprintf("The PIDs to be profiled: %s", pids))
	p.targetPIDs = pids
	return nil
}

// Invoke runs the profiling job and returns execution time.
func (p *GoPprofProfiler) Invoke(job *job.ProfilingJob) (error, time.Duration) {
	start := time.Now()
	pool := pond.New(len(p.targetPIDs), 0, pond.MinWorkers(len(p.targetPIDs)))
	defer pool.StopAndWait()
	group, _ := pool.GroupContext(context.Background())
	for _, pid := range p.targetPIDs {
		pid := pid
		group.Submit(func() error {
			job.PID = pid
			err := p.manager.fetchProfileFromPID(job)
			return err
		})
		time.Sleep(2 * time.Second)
	}
	err := group.Wait()
	return err, time.Since(start)
}

// fetchProfileFromPID fetches the pprof profile from the application's HTTP endpoint by
// entering the target PID's net namespace and using Go's HTTP client.
func (m *goPprofManager) fetchProfileFromPID(job *job.ProfilingJob) error {
	port, err := findListeningPortForPID(job.PID)
	if err != nil {
		commonLog.ErrorLogLn(fmt.Sprintf("failed to find listening port for PID %s: %s", job.PID, err))
		port = "8080"
		commonLog.DebugLogLn(fmt.Sprintf("using default port %s", port))
	}

	profileType := "profile"
	if job.OutputType == api.HeapDump {
		profileType = "heap"
	}

	targetURL := fmt.Sprintf("http://127.0.0.1:%s/debug/pprof/%s?seconds=%d",
		port, profileType, int(job.Interval.Seconds()),
	)

	rawFile := common.GetResultFile(common.TmpDir(), job.Tool, api.Pprof, job.PID, job.Iteration)
	out, err := os.Create(rawFile)
	if err != nil {
		return errors.Wrap(err, "could not create profile file")
	}
	defer out.Close()

	// Enter target PID's network namespace
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	origNS, err := os.Open("/proc/self/ns/net")
	if err != nil {
		return errors.Wrap(err, "opening original netns")
	}
	defer origNS.Close()

	targetNS, err := os.Open(fmt.Sprintf("/proc/%s/ns/net", job.PID))
	if err != nil {
		return errors.Wrapf(err, "opening netns of PID %s", job.PID)
	}
	defer targetNS.Close()

	if err := unix.Setns(int(targetNS.Fd()), unix.CLONE_NEWNET); err != nil {
		return errors.Wrapf(err, "setns into PID %s netns", job.PID)
	}
	// Revert namespace on exit
	defer func() {
		if err := unix.Setns(int(origNS.Fd()), unix.CLONE_NEWNET); err != nil {
			commonLog.ErrorLogLn(fmt.Sprintf(
				"failed to revert to original netns: %v", err,
			))
		}
	}()

	// Perform HTTP GET inside target netns
	resp, err := http.Get(targetURL)
	if err != nil {
		return errors.Wrapf(err, "http.Get %s", targetURL)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pprof HTTP error: %s", resp.Status)
	}

	if _, err := io.Copy(out, resp.Body); err != nil {
		return errors.Wrap(err, "failed to write profile")
	}

	return m.publisher.Do(job.Compressor, rawFile, job.OutputType)
}

// findListeningPortForPID discovers the HTTP pprof listening port for the given PID by
// entering its netns, running lsof, and parsing the first LISTEN port.
func findListeningPortForPID(pid string) (string, error) {
	commonLog.DebugLogLn(fmt.Sprintf("Looking for LISTEN ports in netns of PID %s", pid))

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	origNS, err := os.Open("/proc/self/ns/net")
	if err != nil {
		return "", errors.Wrap(err, "opening original netns")
	}
	defer origNS.Close()

	targetNS, err := os.Open(fmt.Sprintf("/proc/%s/ns/net", pid))
	if err != nil {
		return "", errors.Wrapf(err, "opening netns of PID %s", pid)
	}
	defer targetNS.Close()

	if err := unix.Setns(int(targetNS.Fd()), unix.CLONE_NEWNET); err != nil {
		return "", errors.Wrapf(err, "setns into PID %s netns", pid)
	}
	defer func() {
		if err := unix.Setns(int(origNS.Fd()), unix.CLONE_NEWNET); err != nil {
			commonLog.ErrorLogLn(fmt.Sprintf("failed to revert netns: %v", err))
		}
	}()

	cmd := exec.Command("lsof", "-Pan", "-p", pid, "-iTCP", "-sTCP:LISTEN")
	outBytes, err := cmd.CombinedOutput()
	if err != nil {
		commonLog.ErrorLogLn(fmt.Sprintf("lsof failed in PID %s netns: %v\n%s", pid, err, string(outBytes)))
		return "", errors.Wrap(err, "running lsof inside netns")
	}

	scanner := bufio.NewScanner(strings.NewReader(string(outBytes)))
	for scanner.Scan() {
		line := scanner.Text()
		commonLog.DebugLogLn(fmt.Sprintf("lsof> %s", line))

		if !strings.Contains(line, "LISTEN") {
			continue
		}
		for _, field := range strings.Fields(line) {
			host, port, err := net.SplitHostPort(field)
			if err != nil {
				continue
			}
			if _, err := strconv.Atoi(port); err != nil {
				continue
			}
			commonLog.InfoLogLn(fmt.Sprintf("Found listening port %s (host %s) for PID %s", port, host, pid))
			return port, nil
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		commonLog.ErrorLogLn(fmt.Sprintf("error reading lsof output: %v", scanErr))
		return "", scanErr
	}

	commonLog.ErrorLogLn(fmt.Sprintf("no LISTEN port found for PID %s", pid))
	return "", errors.New("no listening port found for PID " + pid)
}

// CleanUp removes temporary profiling files.
func (p *GoPprofProfiler) CleanUp(*job.ProfilingJob) error {
	file.RemoveAll(common.TmpDir(), config.ProfilingPrefix)
	return nil
}
