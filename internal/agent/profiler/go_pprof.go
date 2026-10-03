package profiler

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/agrison/go-commons-lang/stringUtils"
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

const (
	goPprofDelayBetweenJobs = 2 * time.Second
	// defaultPprofPort is scraped when the target's listening port cannot be
	// detected.
	defaultPprofPort = "8080"
)

// wgetHTTPStatus finds the status code in busybox wget's report of an HTTP
// error, e.g. "wget: server returned error: HTTP/1.1 401 Unauthorized".
var wgetHTTPStatus = regexp.MustCompile(`HTTP/\d(?:\.\d)? (\d{3})`)

// GoPprofProfiler uses Go's pprof HTTP server to collect profiles.
type GoPprofProfiler struct {
	manager    *goPprofManager
	targetPIDs []string
	delay      time.Duration
}

type goPprofManager struct {
	commander executil.Commander
	publisher publish.Publisher
}

// NewGoPprofProfiler creates a new profiler with the given publisher.
func NewGoPprofProfiler(commander executil.Commander, publisher publish.Publisher) *GoPprofProfiler {
	return &GoPprofProfiler{
		manager: &goPprofManager{commander: commander, publisher: publisher},
		delay:   goPprofDelayBetweenJobs,
	}
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
	err := common.ProfilePIDs(p.targetPIDs, p.delay, func(pid string) error {
		// The PIDs run concurrently, so each needs its own copy of the job:
		// setting PID on the shared one let a later PID's value leak into an
		// earlier PID's scrape.
		pidJob := *job
		pidJob.PID = pid
		return p.manager.fetchProfileFromPID(&pidJob)
	})
	return err, time.Since(start)
}

func (p *goPprofManager) heapProfile(job *job.ProfilingJob, port string, fileName string) error {
	// A heap profile is a snapshot of what the process is holding right now, so
	// it takes no duration. Passing ?seconds= makes Go return a DELTA over that
	// window instead — it samples, waits, samples again and subtracts — which
	// nets to nothing whenever the heap is steady. That is how a 30s "heap
	// profile" of a healthy service arrives as `Total samples = 0` with an
	// empty flamegraph.
	//
	// ?gc=1 runs a collection first so the snapshot describes live memory
	// rather than whatever the last GC cycle happened to leave behind; without
	// it a process that has not GC'd yet reports nothing at all.
	targetURL := fmt.Sprintf("http://127.0.0.1:%s/debug/pprof/heap?gc=1", port)
	return p.scrape(job.PID, port, targetURL, fileName)
}
func (p *goPprofManager) cpuProfile(job *job.ProfilingJob, port string, fileName string) error {
	targetURL := fmt.Sprintf(
		"http://127.0.0.1:%s/debug/pprof/%s?seconds=%d",
		port, "profile", int(job.Interval.Seconds()),
	)
	return p.scrape(job.PID, port, targetURL, fileName)
}

// scrape downloads targetURL into fileName from inside the PID's network
// namespace, where 127.0.0.1 is the target's own loopback.
func (p *goPprofManager) scrape(pid string, port string, targetURL string, fileName string) error {
	var out bytes.Buffer
	var stderr bytes.Buffer
	// for local testing
	// cmd := exec.Command(
	// 	"curl", targetURL, "-o", fileName,
	// )
	cmd := p.commander.Command(
		"nsenter", "-t", pid, "-n", "wget", "-qO", fileName, targetURL,
	)
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		log.ErrorLogLn(out.String())
		return pprofScrapeError(port, targetURL, stderr.String(), err)
	}
	return nil
}

// pprofScrapeError turns a failed scrape into a reason the user can act on.
// wget exits 1 for every failure, so the cause can only be read from its
// stderr. The images ship busybox wget, which prints the failure even with
// -q:
//
//	wget: server returned error: HTTP/1.1 401 Unauthorized
//	wget: can't connect to remote host (127.0.0.1): Connection refused
//
// Anything else is passed through as wget reported it.
func pprofScrapeError(port string, targetURL string, stderr string, err error) error {
	msg := strings.TrimSpace(stderr)
	if m := wgetHTTPStatus.FindStringSubmatch(msg); m != nil {
		switch m[1] {
		case "401", "403":
			return errors.Errorf("the pprof endpoint on :%s requires authentication", port)
		case "404":
			return errors.Errorf("no /debug/pprof handler on :%s (is net/http/pprof registered?)", port)
		}
	}
	if strings.Contains(msg, "Connection refused") {
		return errors.Errorf("nothing listening on :%s", port)
	}
	return errors.Wrapf(err, "failed to nsenter+wget %q error %s", targetURL, msg)
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
	port, portErr := findListeningPortForPID(m.commander, job.PID)
	if portErr != nil {
		log.ErrorLogLn(fmt.Sprintf("failed to find listening port for PID %s: %s", job.PID, portErr))
		port = defaultPprofPort
		log.DebugLogLn(fmt.Sprintf("using default port %s", port))
	}
	err := m.fetchProfile(job, port)
	if err != nil && portErr != nil {
		// The port was a guess, not one the target listens on. Say so, or
		// "nothing listening on :8080" reads as if the target's own pprof
		// port were down.
		return errors.Wrapf(err, "could not detect the listening port (%s), so tried the default :%s",
			portErr, port)
	}
	return err
}

// fetchProfile scrapes the profile the job asks for from port and publishes
// it. The PID is not repeated in the errors: the caller reports each PID's
// failure under its PID.
func (m *goPprofManager) fetchProfile(job *job.ProfilingJob, port string) error {
	rawFilePath := common.GetResultFile(common.TmpDir(), job.Tool, job.OutputType, job.PID, job.Iteration)
	switch job.OutputType {
	case api.HeapDump:
		err := m.heapProfile(job, port, rawFilePath)
		if err != nil {
			return errors.Wrap(err, "failed to fetch heap profile")
		}
	case api.Pprof:
		err := m.cpuProfile(job, port, rawFilePath)
		if err != nil {
			return errors.Wrap(err, "failed to fetch CPU profile")
		}
	case api.Raw:
		profileFilePath := common.GetResultFile(common.TmpDir(), job.Tool, "cpu", job.PID, job.Iteration)
		err := m.cpuProfile(job, port, profileFilePath)
		if err != nil {
			return errors.Wrap(err, "failed to create CPU profile")
		}
		profileRaw, err := m.convertPprofToRaw(profileFilePath)
		if err != nil {
			return errors.Wrap(err, "failed to convert CPU profile")
		}
		heapFilePath := common.GetResultFile(common.TmpDir(), job.Tool, "heap", job.PID, job.Iteration)
		err = m.heapProfile(job, port, heapFilePath)
		if err != nil {
			return errors.Wrap(err, "failed to fetch heap profile")
		}
		heapRaw, err := m.convertPprofToRaw(heapFilePath)
		if err != nil {
			return errors.Wrap(err, "failed to convert heap profile")
		}
		file.Write(rawFilePath, fmt.Sprintf("heap dump\n %s \n cpu dump\n %s", heapRaw, profileRaw))
	default:
		return errors.New("unsupported output type for Go pprof profiler")
	}
	// Finally, publish the file as before
	return m.publisher.Do(job.Compressor, rawFilePath, job.OutputType)
}

func findListeningPortForPID(commander executil.Commander, pid string) (string, error) {
	// nsenter into the PID's network namespace and list listening TCP sockets
	cmd := commander.Command("nsenter", "-t", pid, "-n", "ss", "-tulnp")
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
