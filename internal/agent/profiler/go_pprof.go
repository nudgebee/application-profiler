package profiler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
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

type Sample struct {
	Count      int
	CPUCost    int
	StackIDs   []int
	StackFuncs []string
}

type Node struct {
	Name     string  `json:"name"`
	File     string  `json:"file,omitempty"`
	Value    int     `json:"value"`
	Children []*Node `json:"children"`
	childMap map[string]*Node
}

func (m *goPprofManager) convertRawToJson(content string) (string, error) {
	lines := strings.Split(content, "\n")
	header := make(map[string]interface{})
	var rawSamples []Sample
	locations := make(map[int][]string)
	mappings := make(map[int]string)

	mode := ""
	var currentLocID int

	sampleRegex := regexp.MustCompile(`^\s*(\d+)\s+(\d+):\s+(.+)`)
	locLineRegex := regexp.MustCompile(`^\d+:`)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if mode == "" {
			switch {
			case strings.HasPrefix(line, "PeriodType:"):
				header["PeriodType"] = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
			case strings.HasPrefix(line, "Period:"):
				val, _ := strconv.Atoi(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]))
				header["Period"] = val
			case strings.HasPrefix(line, "Time:"):
				header["Time"] = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
			case strings.HasPrefix(line, "Duration:"):
				val, _ := strconv.ParseFloat(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]), 64)
				header["Duration"] = val
			case strings.HasPrefix(line, "samples/count"):
				mode = "samples"
			}
		} else if mode == "samples" {
			if line == "" || strings.HasPrefix(line, "Locations") {
				mode = "locations"
				continue
			}
			if match := sampleRegex.FindStringSubmatch(line); match != nil {
				count, _ := strconv.Atoi(match[1])
				cost, _ := strconv.Atoi(match[2])
				rawIDs := strings.Fields(match[3])
				stackIDs := []int{}
				for _, idStr := range rawIDs {
					id, _ := strconv.Atoi(idStr)
					stackIDs = append(stackIDs, id)
				}
				rawSamples = append(rawSamples, Sample{Count: count, CPUCost: cost, StackIDs: stackIDs})
			}
		} else if mode == "locations" {
			if line == "" || strings.HasPrefix(line, "Mappings") {
				mode = "mappings"
				continue
			}
			if locLineRegex.MatchString(line) {
				parts := strings.SplitN(line, ":", 2)
				currentLocID, _ = strconv.Atoi(parts[0])
				frame := strings.TrimSpace(parts[1])
				if frame != "" {
					locations[currentLocID] = append(locations[currentLocID], frame)
				}
			} else {
				locations[currentLocID] = append(locations[currentLocID], line)
			}
		} else if mode == "mappings" {
			if locLineRegex.MatchString(line) {
				parts := strings.SplitN(line, ":", 2)
				id, _ := strconv.Atoi(parts[0])
				mappings[id] = strings.TrimSpace(parts[1])
			}
		}
	}

	var resolved []Sample
	for _, s := range rawSamples {
		var funcs []string
		for _, id := range s.StackIDs {
			frames := locations[id]
			for _, f := range frames {
				fn := strings.Fields(f)
				if len(fn) > 0 {
					funcs = append(funcs, fn[0])
				}
			}
		}
		s.StackFuncs = funcs
		resolved = append(resolved, s)
	}

	result := map[string]interface{}{
		"header":    header,
		"samples":   resolved,
		"locations": locations,
		"mappings":  mappings,
	}
	samples := result["samples"].([]Sample)
	locations = result["locations"].(map[int][]string)

	root := m.buildTree(samples, locations)

	outBytes, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", errors.Wrap(err, "failed to marshal JSON")
	}
	out := string(outBytes)
	return out, nil
}

func (m *goPprofManager) buildTree(samples []Sample, locations map[int][]string) *Node {
	reverseStrings := func(s []string) []string {
		for i := 0; i < len(s)/2; i++ {
			s[i], s[len(s)-1-i] = s[len(s)-1-i], s[i]
		}
		return s
	}
	getSourceFile := func(fn string, locID int) string {
		for _, line := range locations[locID] {
			match := regexp.MustCompile(`(.+\.go):(\d+):`).FindStringSubmatch(line)
			if match != nil {
				return match[1]
			}
		}
		return ""
	}

	addStack := func(node *Node, stack []string, locIDs []int, value int) {
		for i, fn := range stack {
			var locID int
			if i < len(locIDs) {
				locID = locIDs[i]
			}
			src := getSourceFile(fn, locID)
			key := fn
			if src != "" {
				key += "@" + src
			}

			child, exists := node.childMap[key]
			if !exists {
				child = &Node{
					Name:     fn,
					File:     src,
					Value:    0,
					Children: []*Node{},
					childMap: make(map[string]*Node),
				}
				node.Children = append(node.Children, child)
				node.childMap[key] = child
			}
			child.Value += value
			node = child
		}
	}

	root := &Node{
		Name:     "root",
		Children: []*Node{},
		childMap: make(map[string]*Node),
	}

	for _, s := range samples {
		stack := reverseStrings(s.StackFuncs)
		ids := m.reverseInts(s.StackIDs)
		addStack(root, stack, ids, s.CPUCost)
		root.Value += s.CPUCost
	}

	m.cleanTree(root)
	return root
}

func (m *goPprofManager) cleanTree(n *Node) {
	n.childMap = nil
	for _, child := range n.Children {
		m.cleanTree(child)
	}
}

func (m *goPprofManager) reverseStrings(s []string) []string {
	for i := 0; i < len(s)/2; i++ {
		s[i], s[len(s)-1-i] = s[len(s)-1-i], s[i]
	}
	return s
}

func (m *goPprofManager) reverseInts(s []int) []int {
	for i := 0; i < len(s)/2; i++ {
		s[i], s[len(s)-1-i] = s[len(s)-1-i], s[i]
	}
	return s
}

func (m *goPprofManager) fetchProfileFromPID(job *job.ProfilingJob) error {
	port, err := findListeningPortForPID(job.PID)
	if err != nil {
		log.ErrorLogLn(fmt.Sprintf("failed to find listening port for PID %s: %s", job.PID, err))
		port = "8080"
		log.DebugLogLn(fmt.Sprintf("using default port %s", port))
	}
	resultFilePath := common.GetResultFile(common.TmpDir(), job.Tool, job.OutputType, job.PID, job.Iteration)
	if job.OutputType == api.HeapDump {
		err = m.heapProfile(job, port, resultFilePath)
		if err != nil {
			return errors.Wrapf(err, "failed to fetch heap profile for PID %s", job.PID)
		}
	} else if job.OutputType == api.Pprof {
		err = m.cpuProfile(job, port, resultFilePath)
		if err != nil {
			return errors.Wrapf(err, "failed to fetch CPU profile for PID %s", job.PID)
		}
	} else if job.OutputType == api.FlameJson {
		err = m.cpuProfile(job, port, resultFilePath)
		profileFilePath := common.GetResultFile(common.TmpDir(), job.Tool, "cpu", job.PID, job.Iteration)
		if err != nil {
			return errors.Wrapf(err, "failed to fetch CPU profile for PID %s", job.PID)
		}
		profileRaw, err := m.convertPprofToRaw(profileFilePath)
		if err != nil {
			return errors.Wrapf(err, "failed to convert CPU profile for PID %s", job.PID)
		}
		flameJson, err := m.convertRawToJson(profileRaw)
		if err != nil {
			return errors.Wrapf(err, "failed to convert raw profile to JSON for PID %s", job.PID)
		}
		file.Write(resultFilePath, flameJson)

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
		file.Write(resultFilePath, fmt.Sprintf("heap dump\n %s \n cpu dump\n %s", heapRaw, profileRaw))
	} else {
		return errors.New("unsupported output type for Go pprof profiler")
	}
	// Finally, publish the file as before
	return m.publisher.Do(job.Compressor, resultFilePath, job.OutputType)
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
