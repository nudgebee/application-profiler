package profiler

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/agrison/go-commons-lang/stringUtils"
	"github.com/alitto/pond"
	"github.com/nudgebee/application-profiler/api"
	"github.com/nudgebee/application-profiler/internal/agent/config"
	"github.com/nudgebee/application-profiler/internal/agent/job"
	common "github.com/nudgebee/application-profiler/internal/agent/profiler/common"
	"github.com/nudgebee/application-profiler/internal/agent/util"
	executil "github.com/nudgebee/application-profiler/internal/agent/util/exec"
	"github.com/nudgebee/application-profiler/internal/agent/util/publish"
	"github.com/nudgebee/application-profiler/pkg/util/file"
	"github.com/nudgebee/application-profiler/pkg/util/log"
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
func (p *goPprofManager) heapProfile(job *job.ProfilingJob, port string, fileName string) error {
	var out bytes.Buffer
	var stderr bytes.Buffer
	targetURL := fmt.Sprintf(
		"http://127.0.0.1:%s/debug/pprof/%s?seconds=%d",
		port, "heap", int(job.Interval.Seconds()),
	)
	// for local testing
	// cmd := exec.Command(
	// 	"curl", targetURL, "-o", fileName,
	// )
	cmd := exec.Command(
		"nsenter", "-t", job.PID, "-n", "wget", "-qO", fileName, targetURL,
	)
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		log.ErrorLogLn(out.String())
		return errors.Wrapf(err, "failed to nsenter+wget %q error %s", targetURL, stderr.String())
	}
	return nil
}
func (p *goPprofManager) cpuProfile(job *job.ProfilingJob, port string, fileName string) error {
	var out bytes.Buffer
	var stderr bytes.Buffer
	targetURL := fmt.Sprintf(
		"http://127.0.0.1:%s/debug/pprof/%s?seconds=%d",
		port, "profile", int(job.Interval.Seconds()),
	)
	// for local testing
	// cmd := exec.Command(
	// 	"curl", targetURL, "-o", fileName,
	// )
	cmd := exec.Command(
		"nsenter", "-t", job.PID, "-n", "wget", "-qO", fileName, targetURL,
	)
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		log.ErrorLogLn(out.String())
		return errors.Wrapf(err, "failed to nsenter+wget %q error %s", targetURL, stderr.String())
	}
	return nil
}
func (p *goPprofManager) convertPprofToRaw(pprofFilePath string) (string, error) {
	// Convert the pprof output to raw format using go tool pprof
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd := exec.Command("go", "tool", "pprof", "--raw", pprofFilePath)
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		log.ErrorLogLn(out.String())
		return "", errors.Wrapf(err, "failed to convert pprof output %q to raw format error %s", pprofFilePath, stderr.String())
	}
	return out.String(), nil

}

func (m *goPprofManager) fetchProfileFromPID(job *job.ProfilingJob) error {
	port, err := findListeningPortForPID(job.PID)
	if err != nil {
		log.ErrorLogLn(fmt.Sprintf("failed to find listening port for PID %s: %s", job.PID, err))
		port = "8080"
		log.DebugLogLn(fmt.Sprintf("using default port %s", port))
	}
	rawFilePath := common.GetResultFile(common.TmpDir(), job.Tool, job.OutputType, job.PID, job.Iteration)
	if job.OutputType == api.HeapDump {
		err = m.heapProfile(job, port, rawFilePath)
		if err != nil {
			return errors.Wrapf(err, "failed to fetch heap profile for PID %s", job.PID)
		}
	} else if job.OutputType == api.Pprof {
		err = m.cpuProfile(job, port, rawFilePath)
		if err != nil {
			return errors.Wrapf(err, "failed to fetch CPU profile for PID %s", job.PID)
		}
	} else if job.OutputType == api.Raw {
		profileFilePath := common.GetResultFile(common.TmpDir(), job.Tool, "cpu", job.PID, job.Iteration)
		err = m.cpuProfile(job, port, profileFilePath)
		if err != nil {
			return errors.Wrapf(err, "failed to create CPU profile for PID %s", job.PID)
		}
		profileRaw, err := m.convertPprofToRaw(profileFilePath)
		if err != nil {
			return errors.Wrapf(err, "failed to convert CPU profile for PID %s", job.PID)
		}
		heapFilePath := common.GetResultFile(common.TmpDir(), job.Tool, "heap", job.PID, job.Iteration)
		err = m.heapProfile(job, port, heapFilePath)
		if err != nil {
			return errors.Wrapf(err, "failed to fetch heap profile for PID %s", job.PID)
		}
		heapRaw, err := m.convertPprofToRaw(heapFilePath)
		if err != nil {
			return errors.Wrapf(err, "failed to convert heap profile for PID %s", job.PID)
		}
		file.Write(rawFilePath, fmt.Sprintf("heap dump\n %s \n cpu dump\n %s", heapRaw, profileRaw))
	} else {
		return errors.New("unsupported output type for Go pprof profiler")
	}
	// Finally, publish the file as before
	return m.publisher.Do(job.Compressor, rawFilePath, job.OutputType)
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
