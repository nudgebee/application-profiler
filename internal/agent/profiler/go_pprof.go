package profiler

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/josepdcs/kubectl-prof/api"
	"github.com/josepdcs/kubectl-prof/internal/agent/config"
	"github.com/josepdcs/kubectl-prof/internal/agent/job"
	common "github.com/josepdcs/kubectl-prof/internal/agent/profiler/common"
	executil "github.com/josepdcs/kubectl-prof/internal/agent/util/exec"
	"github.com/josepdcs/kubectl-prof/internal/agent/util/publish"
	"github.com/josepdcs/kubectl-prof/pkg/util/file"
	"github.com/pkg/errors"
)

// GoPprofProfiler uses Go's pprof HTTP server to collect profiles.
type GoPprofProfiler struct {
	manager *goPprofManager
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
	if job.PID == "" {
		return errors.New("PID is required for GoPprofProfiler")
	}
	return nil
}

// Invoke runs the profiling job and returns execution time.
func (p *GoPprofProfiler) Invoke(job *job.ProfilingJob) (error, time.Duration) {
	start := time.Now()
	err := p.manager.fetchProfileFromPID(job)
	return err, time.Since(start)
}

// fetchProfileFromPID fetches the pprof profile from the application's HTTP endpoint.
func (m *goPprofManager) fetchProfileFromPID(job *job.ProfilingJob) error {
	port, err := findListeningPortForPID(job.PID)
	if err != nil {
		return errors.Wrap(err, "could not resolve port for PID")
	}

	profileType := "profile"
	if job.OutputType == api.HeapDump {
		profileType = "heap"
	}

	url := fmt.Sprintf("http://127.0.0.1:%s/debug/pprof/%s?seconds=%d", port, profileType, int(job.Interval.Seconds()))
	resp, err := http.Get(url)
	if err != nil {
		return errors.Wrapf(err, "failed to fetch pprof from %s", url)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pprof HTTP error: %s", resp.Status)
	}

	rawFile := common.GetResultFile(common.TmpDir(), job.Tool, api.Pprof, job.PID, job.Iteration)

	out, err := os.Create(rawFile)
	if err != nil {
		return errors.Wrap(err, "could not create profile file")
	}
	defer out.Close()

	if _, err = io.Copy(out, resp.Body); err != nil {
		return errors.Wrap(err, "failed to write profile")
	}
	return m.publisher.Do(job.Compressor, rawFile, job.OutputType)
}

// findListeningPortForPID discovers the HTTP pprof listening port for the given PID.
func findListeningPortForPID(pid string) (string, error) {
	// run lsof to list all LISTEN sockets for this PID
	cmd := exec.Command("lsof", "-Pan", "-p", pid, "-iTCP", "-sTCP:LISTEN")
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
			return port, nil
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
