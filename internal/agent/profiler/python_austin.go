package profiler

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

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
	unshareLocation           = "unshare"
)

// austinInTargetFSScript runs austin with the target's interpreter files
// mounted at the paths austin will look for them.
//
// austin finds the interpreter (and libpython, for a shared build) by the
// path it reads from /proc/<pid>/maps, then opens that path in its OWN mount
// namespace — not under /proc/<pid>/root. In this container that path is
// either missing (a target on any other Python than ours fails with "Cannot
// determine the version of the Python interpreter") or, worse, our own
// interpreter (a python:3.14 target would be read through our python3.14).
//
// The bind can't come straight from the target: the kernel refuses a bind
// whose source lives in another mount namespace (EINVAL). So each file is
// copied out first and the copy is bound, inside a private mount namespace
// so neither the mounts nor the shadowing of our own python leak into the
// rest of the agent (mojo2austin runs on it).
//
// Positional args: <copy dir> <n> <source 1> <path 1> ... <source n>
// <path n> <austin args...>.
const austinInTargetFSScript = `set -e
dir=$1; n=$2; shift 2
i=0
while [ "$i" -lt "$n" ]; do
	src=$1; p=$2; shift 2
	c="$dir$p"
	mkdir -p "$(dirname "$c")" "$(dirname "$p")"
	cp "$src" "$c"
	[ -e "$p" ] || touch "$p"
	mount --bind "$c" "$p"
	i=$((i+1))
done
exec ` + austinLocation + ` "$@"`

// mojoMagic prefixes austin's binary output format.
var mojoMagic = []byte{'M', 'O', 'J', 3}

// procDir is where the target's /proc entries are read from; a var so tests
// can point it at a fake tree.
var procDir = "/proc"

// sourceReadable reports whether a target file can be copied out. A var
// because /proc/<pid>/exe and map_files are magic links a fake tree can't
// reproduce: they resolve to the mapped inode, not to the path they print.
var sourceReadable = func(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

var austinPythonCommand = func(commander executil.Commander, job *job.ProfilingJob, pid string, fileName string, copyDir string, targetFiles []austinTargetFile) *exec.Cmd {
	interval := strconv.Itoa(int(job.Interval.Seconds()))
	austinArgs := []string{"-p", pid, "-o", fileName, "-x", interval, "-m"}
	if len(targetFiles) == 0 {
		return commander.Command(austinLocation, austinArgs...)
	}
	args := []string{"-m", "--propagation", "private", "sh", "-c", austinInTargetFSScript, "sh", copyDir, strconv.Itoa(len(targetFiles))}
	for _, f := range targetFiles {
		args = append(args, f.Source, f.Path)
	}
	args = append(args, austinArgs...)
	return commander.Command(unshareLocation, args...)
}

// austinTargetFile is one file austin opens in the target: Path is where
// austin looks for it, Source where the exact file the process mapped can be
// read from this container.
type austinTargetFile struct {
	Path   string
	Source string
}

// austinTargetFiles returns the files austin opens by the path it finds in
// /proc/<pid>/maps: the interpreter binary and, for a shared build,
// libpython.
//
// Each is read through a magic link to the inode the process actually
// mapped (/proc/<pid>/exe for the interpreter, /proc/<pid>/map_files/<range>
// for libpython), not through /proc/<pid>/root<path>. A file replaced on disk
// since the process started (maps then says "(deleted)") would otherwise be
// read from its replacement, and austin would resolve its symbols against
// the wrong build.
func austinTargetFiles(pid string) ([]austinTargetFile, error) {
	exe, err := os.Readlink(filepath.Join(procDir, pid, "exe"))
	if err != nil {
		return nil, errors.Wrapf(err, "could not resolve the interpreter of PID %s", pid)
	}
	candidates := []austinTargetFile{{
		Path:   strings.TrimSuffix(exe, " (deleted)"),
		Source: filepath.Join(procDir, pid, "exe"),
	}}

	maps, err := os.ReadFile(filepath.Join(procDir, pid, "maps")) //nolint:gosec // path built from procDir and a numeric PID
	if err != nil {
		return nil, errors.Wrapf(err, "could not read the memory map of PID %s", pid)
	}
	for _, line := range strings.Split(string(maps), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		path := strings.TrimSuffix(strings.Join(fields[5:], " "), " (deleted)")
		if strings.HasPrefix(filepath.Base(path), "libpython") {
			candidates = append(candidates, austinTargetFile{
				Path:   path,
				Source: filepath.Join(procDir, pid, "map_files", fields[0]),
			})
		}
	}

	var files []austinTargetFile
	seen := map[string]bool{}
	for _, f := range candidates {
		if seen[f.Path] || !filepath.IsAbs(f.Path) {
			continue
		}
		seen[f.Path] = true
		if !sourceReadable(f.Source) {
			continue
		}
		files = append(files, f)
	}
	return files, nil
}

// austinCopyDir is where the target's interpreter files are copied for one
// PID.
func austinCopyDir(pid string) string {
	return filepath.Join(common.TmpDir(), "austin-target-"+pid)
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
	err := common.ProfilePIDs(p.targetPIDs, p.delay, func(pid string) error {
		err, _ := p.invoke(job, pid)
		return err
	})
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
	// Without the target's interpreter files austin can only ever fail, but
	// still run it: its own error says more than ours would.
	targetFiles, err := austinTargetFiles(pid)
	if err != nil {
		log.DebugLogLn(err.Error())
	}
	binary := "unknown binary"
	copyDir := austinCopyDir(pid)
	if len(targetFiles) > 0 {
		binary = targetFiles[0].Path
		// The copies are bound only inside austin's own mount namespace,
		// which is gone once it exits; libpython alone can be tens of MB.
		defer func() { _ = os.RemoveAll(copyDir) }()
	}
	cmd := austinPythonCommand(p.commander, job, pid, fileName, copyDir, targetFiles)
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err != nil && !austinStoppedOnSignal(err) {
		log.ErrorLogLn(out.String())
		return errors.Errorf("could not launch profiler: austin (PID %s, %s): %s (%s)",
			pid, binary, austinErrorMessage(stderr.String()), err), time.Since(start)
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
		return errors.Errorf("no samples collected: austin (PID %s, %s): %s",
			pid, binary, austinErrorMessage(stderr.String())), time.Since(start)
	}

	// Memory mode records a sample only when the process's memory grows, so
	// a process at steady state legitimately yields a header and nothing
	// else. Say so: a header-only artefact reads as a broken profile, and the
	// flamegraph path would call it "low cpu load".
	samples, err := austinSampleCount(fileName)
	if err != nil {
		return errors.Wrap(err, "could not read the profile"), time.Since(start)
	}
	if samples == 0 {
		msg := fmt.Sprintf("no memory growth observed during the %s window: austin's memory mode records "+
			"allocations that grow the process's memory, so a process at steady state yields no samples",
			job.Interval)
		if job.OutputType != api.Raw {
			return errors.Errorf("PID %s: %s", pid, msg), time.Since(start)
		}
		if err := appendLine(fileName, "# "+msg); err != nil {
			return errors.Wrap(err, "could not annotate the profile"), time.Since(start)
		}
	}

	// result file name is composed by the job info and the pid
	resultFileName := common.GetResultFile(common.TmpDir(), job.Tool, job.OutputType, pid, job.Iteration)
	if job.OutputType == api.FlameGraph {
		err = p.handleFlamegraph(job, flamegraph.Get(job), fileName, resultFileName)
		if err != nil {
			// Not nil: nothing is published for this PID, and a run that
			// ends without a result must not report success.
			return errors.Wrap(err, "could not generate flamegraph"), time.Since(start)
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

var (
	ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	// austinBannerEnd is the version line that closes austin's ASCII-art
	// banner, e.g. "\__,_|\_,_/__/\__|_|_||_| 4.0.0 [musl-gcc 13.3.0]".
	austinBannerEnd = regexp.MustCompile(`\d+\.\d+\.\d+ \[[^\]]*\]`)
)

// austinErrorMessage reduces austin's stderr to the sentence that explains
// the failure: no ANSI colours, no ASCII-art banner, no emoji, and none of
// the "please report an issue" boilerplate after it.
func austinErrorMessage(stderr string) string {
	s := ansiEscape.ReplaceAllString(stderr, "")
	if loc := austinBannerEnd.FindStringIndex(s); loc != nil {
		s = s[loc[1]:]
	}
	if i := strings.Index(s, "If you are sure"); i >= 0 {
		s = s[:i]
	}
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimLeftFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if s == "" {
		return "no error output"
	}
	const maxLen = 300
	if len(s) > maxLen {
		s = s[:maxLen] + "…"
	}
	return s
}

// austinSampleCount counts the sample lines in austin's text output. Every
// sample starts with its process ("P<pid>;"); header and metadata lines
// start with "#".
func austinSampleCount(fileName string) (int, error) {
	f, err := os.Open(fileName) //nolint:gosec // path built by us from TmpDir
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	n := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		if line := scanner.Bytes(); len(line) > 0 && line[0] == 'P' {
			n++
		}
	}
	return n, scanner.Err()
}

func appendLine(fileName, line string) error {
	f, err := os.OpenFile(fileName, os.O_APPEND|os.O_WRONLY, 0) //nolint:gosec // path built by us from TmpDir
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
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
