package jvm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	"github.com/nudgebee/application-profiler/internal/agent/util/publish"
	"github.com/nudgebee/application-profiler/pkg/util/file"
	"github.com/nudgebee/application-profiler/pkg/util/log"
	"github.com/pkg/errors"
)

const (
	asyncProfilerDir = "/tmp/async-profiler"
	profilerSh       = asyncProfilerDir + "/profiler.sh"
	// profilerLib is the path profiler.sh hands to the target JVM to dlopen.
	// The image ships the glibc build under this name and the musl build
	// beside it; selectProfilerLibrary swaps them when the target is musl.
	profilerLib                   = asyncProfilerDir + "/build/libasyncProfiler.so"
	profilerLibMusl               = asyncProfilerDir + "/build/libasyncProfiler-musl.so"
	asyncProfilerDelayBetweenJobs = 2 * time.Second
)

var asyncProfilerCommand = func(commander executil.Commander, job *job.ProfilingJob, pid string, fileName string) *exec.Cmd {
	interval := strconv.Itoa(int(job.Interval.Seconds()))
	event := string(job.Event)
	output := string(job.OutputType)
	if job.OutputType == api.Raw {
		// overrides to collapsed type since it is the type defined be async-profiler, which it is what we want
		output = string(api.Collapsed)
	}
	args := []string{"-o", output, "-d", interval, "-f", fileName, "-e", event, "--fdtransfer", pid}
	return commander.Command(profilerSh, args...)
}

var asyncProfilerStopCommand = func(commander executil.Commander, job *job.ProfilingJob, pid string) *exec.Cmd {
	return commander.Command(profilerSh, "stop", pid)
}

type AsyncProfiler struct {
	targetPIDs []string
	delay      time.Duration
	AsyncProfilerManager
}

type AsyncProfilerManager interface {
	removeTmpDir() error
	linkTmpDirToTargetTmpDir(string) error
	copyProfilerToTmpDir() error
	selectProfilerLibrary(string) error
	chownProfilerToTarget(string) error
	invoke(*job.ProfilingJob, string) (error, time.Duration)
	cleanUp(*job.ProfilingJob, string)
}

type asyncProfilerManager struct {
	commander executil.Commander
	publisher publish.Publisher
}

func NewAsyncProfiler(commander executil.Commander, publisher publish.Publisher) *AsyncProfiler {
	return &AsyncProfiler{
		delay: asyncProfilerDelayBetweenJobs,
		AsyncProfilerManager: &asyncProfilerManager{
			commander: commander,
			publisher: publisher,
		},
	}
}

func (j *AsyncProfiler) SetUp(job *job.ProfilingJob) error {
	// PIDs first: everything below is staged through the target's own mount
	// namespace, which we can only reach via one of its PIDs.
	if stringUtils.IsNotBlank(job.PID) {
		j.targetPIDs = []string{job.PID}
	} else {
		pids, err := util.GetCandidatePIDs(job)
		if err != nil {
			return err
		}
		log.DebugLogLn(fmt.Sprintf("The PIDs to be profiled: %s", pids))
		j.targetPIDs = pids
	}

	// Every PID of a container shares its mount namespace, so any of them
	// resolves the same filesystem.
	targetFs := util.TargetRootFS(j.targetPIDs[0])
	log.DebugLogLn(fmt.Sprintf("The target filesystem is: %s", targetFs))

	if err := j.removeTmpDir(); err != nil {
		return err
	}

	targetTmpDir := filepath.Join(targetFs, "tmp")
	// remove previous files from a previous profiling
	file.RemoveAll(targetTmpDir, config.ProfilingPrefix+string(job.OutputType))

	if err := j.linkTmpDirToTargetTmpDir(targetTmpDir); err != nil {
		return err
	}

	if err := j.copyProfilerToTmpDir(); err != nil {
		return err
	}

	if err := j.selectProfilerLibrary(targetFs); err != nil {
		return err
	}

	// The JVM dlopens the library and writes the profile itself, as whatever
	// user it runs as — root-owned staging is unreadable/unwritable for it.
	return j.chownProfilerToTarget(j.targetPIDs[0])
}

// targetUsesMusl reports whether the target container's root filesystem is
// musl-based (alpine and friends), by looking for the musl dynamic loader.
func targetUsesMusl(targetFs string) bool {
	// An empty root would make the patterns below relative to our own
	// working directory — and this image is alpine, so every target would
	// look musl-based. Treat "unknown" as the shipped default (glibc).
	if targetFs == "" {
		return false
	}
	for _, dir := range []string{"lib", "usr/lib"} {
		if matches, _ := filepath.Glob(filepath.Join(targetFs, dir, "ld-musl-*.so.1")); len(matches) > 0 {
			return true
		}
	}
	return false
}

func (j *asyncProfilerManager) removeTmpDir() error {
	return os.RemoveAll(common.TmpDir())
}

func (j *asyncProfilerManager) linkTmpDirToTargetTmpDir(targetTmpDir string) error {
	return os.Symlink(targetTmpDir, common.TmpDir())
}

func (j *asyncProfilerManager) copyProfilerToTmpDir() error {
	cmd := j.commander.Command("cp", "-r", "/app/async-profiler", common.TmpDir())
	return cmd.Run()
}

// chownProfilerToTarget hands the staged directory to the user the target runs
// as. We stage as root; the JVM then has to read libasyncProfiler.so and write
// its own output file into that directory, and most hardened images do not run
// as root.
func (j *asyncProfilerManager) chownProfilerToTarget(pid string) error {
	uid, gid, err := util.TargetCredentials(pid)
	if err != nil {
		return err
	}
	if uid == "0" && gid == "0" {
		return nil
	}
	log.DebugLogLn(fmt.Sprintf("Handing the staged profiler to %s:%s", uid, gid))
	cmd := j.commander.Command("chown", "-R", uid+":"+gid, asyncProfilerDir)
	return cmd.Run()
}

// selectProfilerLibrary points libasyncProfiler.so at the build matching the
// target's libc. The library is dlopen'd by the target JVM rather than by us,
// so a mismatch fails the attach with "libc.musl-x86_64.so.1: cannot open
// shared object file" — and the profile comes back empty with no clue why.
// The image ships the glibc build under the default name, so only musl
// targets need the swap.
func (j *asyncProfilerManager) selectProfilerLibrary(targetFs string) error {
	if !targetUsesMusl(targetFs) {
		return nil
	}
	log.DebugLogLn("The target is musl-based; using the musl build of libasyncProfiler.so")
	cmd := j.commander.Command("cp", "-f", profilerLibMusl, profilerLib)
	return cmd.Run()
}

func (j *AsyncProfiler) Invoke(job *job.ProfilingJob) (error, time.Duration) {
	start := time.Now()

	pool := pond.New(len(j.targetPIDs), 0, pond.MinWorkers(len(j.targetPIDs)))
	defer pool.StopAndWait()

	// create a task group associated to a context
	group, _ := pool.GroupContext(context.Background())

	// submit tasks to profile
	for _, pid := range j.targetPIDs {
		pid := pid
		group.Submit(func() error {
			err, _ := j.invoke(job, pid)
			return err
		})
		// wait a bit between jobs for not overloading the system
		time.Sleep(j.delay)
	}

	// wait for all tasks to finish
	err := group.Wait()

	return err, time.Since(start)
}

func (j *asyncProfilerManager) invoke(job *job.ProfilingJob, pid string) (error, time.Duration) {
	start := time.Now()
	var out bytes.Buffer
	var stderr bytes.Buffer

	resultFileName := common.GetResultFile(common.TmpDir(), job.Tool, job.OutputType, pid, job.Iteration)
	cmd := asyncProfilerCommand(j.commander, job, pid, resultFileName)
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		log.ErrorLogLn(out.String())
		return errors.Wrapf(err, "could not launch profiler: %s", stderr.String()), time.Since(start)
	}
	log.DebugLogLn(out.String())

	return j.publisher.Do(job.Compressor, resultFileName, job.OutputType), time.Since(start)
}

func (j *AsyncProfiler) CleanUp(job *job.ProfilingJob) error {
	for _, pid := range j.targetPIDs {
		j.cleanUp(job, pid)
	}

	err := os.RemoveAll(asyncProfilerDir)
	if err != nil {
		log.WarningLogLn(fmt.Sprintf("async-profiler folder could not be removed: %s", err))
	}
	file.RemoveAll(common.TmpDir(), config.ProfilingPrefix+string(job.OutputType))

	return nil
}

func (j *asyncProfilerManager) cleanUp(job *job.ProfilingJob, pid string) {
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd := asyncProfilerStopCommand(j.commander, job, pid)
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		log.WarningLogLn(stderr.String())
	}
	_, _ = fmt.Fprint(io.Discard, out.String())
}
