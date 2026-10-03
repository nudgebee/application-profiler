package profiler

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/nudgebee/application-profiler/api"
	"github.com/nudgebee/application-profiler/internal/agent/job"
	"github.com/nudgebee/application-profiler/internal/agent/profiler/common"
	executil "github.com/nudgebee/application-profiler/internal/agent/util/exec"
	"github.com/nudgebee/application-profiler/internal/agent/util/publish"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test_austinStoppedOnSignal — austin ends every exposure-limited run by
// returning -SIGINT, so treating that as a failure would reject every
// successful profile.
func Test_austinStoppedOnSignal(t *testing.T) {
	t.Run("254 is the normal end of an exposure-limited run", func(t *testing.T) {
		err := exec.Command("sh", "-c", "exit 254").Run()
		require.Error(t, err)
		assert.True(t, austinStoppedOnSignal(err))
	})

	t.Run("a real failure is still a failure", func(t *testing.T) {
		err := exec.Command("sh", "-c", "exit 1").Run()
		require.Error(t, err)
		assert.False(t, austinStoppedOnSignal(err))
	})

	t.Run("nil", func(t *testing.T) {
		assert.False(t, austinStoppedOnSignal(nil))
	})
}

// Test_convertMojo — austin 4 writes the binary MOJO format, which neither
// the raw artefact nor flamegraph.pl can read; anything else must be passed
// through untouched so the conversion is safe across austin versions.
func Test_convertMojo(t *testing.T) {
	t.Run("MOJO output is converted in place", func(t *testing.T) {
		dir := t.TempDir()
		profile := filepath.Join(dir, "profile.raw")
		require.NoError(t, os.WriteFile(profile, append(mojoMagic, 1, 2, 3), 0o600))

		commander := executil.NewFakeCommander()
		commander.On("Command").Return(exec.Command("sh", "-c", "echo 'P1;T1;f.py:main:1 42' > "+profile+".txt"))
		m := &austinPythonManager{commander: commander, publisher: publish.NewFakePublisher()}

		require.NoError(t, m.convertMojo(profile))

		converted, err := os.ReadFile(profile)
		require.NoError(t, err)
		assert.Equal(t, "P1;T1;f.py:main:1 42\n", string(converted))
		assert.NoFileExists(t, profile+".txt", "the intermediate file should be renamed, not left behind")
	})

	t.Run("text output is left alone", func(t *testing.T) {
		dir := t.TempDir()
		profile := filepath.Join(dir, "profile.raw")
		require.NoError(t, os.WriteFile(profile, []byte("# austin: 3.7.0\nP1;T1;f.py:main:1 42\n"), 0o600))

		commander := executil.NewFakeCommander()
		commander.On("Command").Return(exec.Command("false"))
		m := &austinPythonManager{commander: commander, publisher: publish.NewFakePublisher()}

		require.NoError(t, m.convertMojo(profile))
		assert.Equal(t, 0, commander.On("Command").InvokedTimes(), "no conversion should run for text output")
	})

	t.Run("an empty profile is not mistaken for MOJO", func(t *testing.T) {
		dir := t.TempDir()
		profile := filepath.Join(dir, "profile.raw")
		require.NoError(t, os.WriteFile(profile, nil, 0o600))

		commander := executil.NewFakeCommander()
		commander.On("Command").Return(exec.Command("false"))
		m := &austinPythonManager{commander: commander, publisher: publish.NewFakePublisher()}

		require.NoError(t, m.convertMojo(profile))
	})
}

// fakeProc builds a /proc/<pid> tree: an exe link, a maps file, and the
// files present under the target's root.
func fakeProc(t *testing.T, pid, exe, maps string, rootFiles ...string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, pid, "root"), 0o755))
	require.NoError(t, os.Symlink(exe, filepath.Join(dir, pid, "exe")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, pid, "maps"), []byte(maps), 0o600))
	for _, f := range rootFiles {
		p := filepath.Join(dir, pid, "root", f)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, nil, 0o600))
	}
	return dir
}

func useProcDir(t *testing.T, dir string) {
	t.Helper()
	old := procDir
	procDir = dir
	t.Cleanup(func() { procDir = old })
}

// Test_austinTargetFiles — austin opens the interpreter and libpython by the
// paths in /proc/<pid>/maps, so those are exactly the files to mount.
func Test_austinTargetFiles(t *testing.T) {
	const maps = `55d0c0a00000-55d0c0a01000 r--p 00000000 00:2e 1234 /usr/local/bin/python3.12
7f1c40000000-7f1c40200000 r--p 00000000 00:2e 1235 /usr/local/lib/libpython3.12.so.1.0
7f1c40200000-7f1c40400000 r-xp 00200000 00:2e 1235 /usr/local/lib/libpython3.12.so.1.0
7f1c50000000-7f1c50100000 r-xp 00000000 00:2e 1236 /lib/ld-musl-x86_64.so.1
7f1c60000000-7f1c60100000 r-xp 00000000 00:2e 1237 /usr/local/lib/python3.12/lib-dynload/_json.cpython-312-x86_64-linux-musl.so
7ffd10000000-7ffd10021000 rw-p 00000000 00:00 0 [stack]
`

	t.Run("interpreter first, then libpython, each once", func(t *testing.T) {
		useProcDir(t, fakeProc(t, "42", "/usr/local/bin/python3.12", maps,
			"/usr/local/bin/python3.12", "/usr/local/lib/libpython3.12.so.1.0", "/lib/ld-musl-x86_64.so.1"))

		files, err := austinTargetFiles("42")
		require.NoError(t, err)
		assert.Equal(t, []string{"/usr/local/bin/python3.12", "/usr/local/lib/libpython3.12.so.1.0"}, files)
	})

	t.Run("a file missing from the target's root is skipped", func(t *testing.T) {
		useProcDir(t, fakeProc(t, "42", "/usr/local/bin/python3.12", maps, "/usr/local/bin/python3.12"))

		files, err := austinTargetFiles("42")
		require.NoError(t, err)
		assert.Equal(t, []string{"/usr/local/bin/python3.12"}, files)
	})

	t.Run("a replaced interpreter keeps its path", func(t *testing.T) {
		useProcDir(t, fakeProc(t, "42", "/usr/bin/python3.11 (deleted)", "", "/usr/bin/python3.11"))

		files, err := austinTargetFiles("42")
		require.NoError(t, err)
		assert.Equal(t, []string{"/usr/bin/python3.11"}, files)
	})

	t.Run("an unknown PID is an error", func(t *testing.T) {
		useProcDir(t, t.TempDir())

		_, err := austinTargetFiles("42")
		assert.Error(t, err)
	})
}

func Test_austinPythonCommand(t *testing.T) {
	j := &job.ProfilingJob{Interval: 30 * time.Second}

	t.Run("no target files runs austin directly", func(t *testing.T) {
		cmd := austinPythonCommand(executil.NewCommander(), j, "42", "/tmp/out", nil)
		assert.Equal(t, []string{"austin", "-p", "42", "-o", "/tmp/out", "-x", "30", "-m"}, cmd.Args)
	})

	t.Run("target files run austin in a private mount namespace", func(t *testing.T) {
		files := []string{"/usr/local/bin/python3.12", "/usr/local/lib/libpython3.12.so.1.0"}
		cmd := austinPythonCommand(executil.NewCommander(), j, "42", "/tmp/out", files)
		assert.Equal(t, []string{
			"unshare", "-m", "--propagation", "private", "sh", "-c", austinInTargetFSScript, "sh",
			"42", "2", "/usr/local/bin/python3.12", "/usr/local/lib/libpython3.12.so.1.0",
			"-p", "42", "-o", "/tmp/out", "-x", "30", "-m",
		}, cmd.Args)
	})
}

// Test_austinErrorMessage uses the stderr austin 4.0.0 actually prints when
// it cannot read the interpreter.
func Test_austinErrorMessage(t *testing.T) {
	stderr := "\x1b[1m              _   _      \x1b[0m\n\x1b[1m __ _ _  _ __| |_(_)_ _  \x1b[0m\n" +
		"\x1b[1m/ _` | || (_-<  _| | ' \\ \x1b[0m\n\x1b[1m\\__,_|\\_,_/__/\\__|_|_||_|\x1b[0m \x1b[1;36m4.0.0\x1b[0m [musl-gcc 13.3.0]\n\n" +
		"🔢 Cannot determine the version of the Python interpreter. This could be due\n" +
		"to the binary not being an actual Python binary, like uWSGI, or a version that\n" +
		"this version of Austin does not support. If you are sure that the binary is a\n" +
		"supported Python binary, please report an issue at\n\n    🌐 https://github.com/P403n1x87/austin/issues\n"

	assert.Equal(t, "Cannot determine the version of the Python interpreter. This could be due to the binary "+
		"not being an actual Python binary, like uWSGI, or a version that this version of Austin does not support.",
		austinErrorMessage(stderr))
	assert.Equal(t, "Permission denied", austinErrorMessage("Permission denied\n"))
	assert.Equal(t, "no error output", austinErrorMessage(""))
}

func Test_austinSampleCount(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "profile.raw")
	require.NoError(t, os.WriteFile(profile, []byte("# austin: 4.0.0\n# mode: memory\n\nP1;T0:1;/app.py:<module>:6 131072\nP1;T0:1;/app.py:<module>:6 65536\n"), 0o600))

	n, err := austinSampleCount(profile)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
}

// Test_invoke_noMemoryGrowth — memory mode only samples growth, so a steady
// process gives a header and nothing else. A raw profile says so and still
// publishes; any other output fails with the same explanation.
func Test_invoke_noMemoryGrowth(t *testing.T) {
	setup := func(t *testing.T, output api.OutputType) (*austinPythonManager, publish.FakePublisher, *job.ProfilingJob, string) {
		t.Helper()
		tmp := t.TempDir()
		oldTmp := common.TmpDir
		common.TmpDir = func() string { return tmp }
		t.Cleanup(func() { common.TmpDir = oldTmp })
		useProcDir(t, t.TempDir())

		j := &job.ProfilingJob{Tool: api.Austin, OutputType: output, Interval: 30 * time.Second, Iteration: 1}
		raw := common.GetResultFile(tmp, j.Tool, api.Raw, "42", j.Iteration)
		commander := executil.NewFakeCommander()
		commander.On("Command").Return(exec.Command("sh", "-c", "printf '# austin: 4.0.0\\n# mode: memory\\n' > "+raw))
		publisher := publish.NewFakePublisher()
		publisher.On("Do").Return(nil)
		return &austinPythonManager{commander: commander, publisher: publisher}, publisher, j, raw
	}

	t.Run("raw output is annotated and published", func(t *testing.T) {
		m, publisher, j, raw := setup(t, api.Raw)

		err, _ := m.invoke(j, "42")
		require.NoError(t, err)
		assert.Equal(t, 1, publisher.On("Do").InvokedTimes())
		content, err := os.ReadFile(raw)
		require.NoError(t, err)
		assert.Contains(t, string(content), "# no memory growth observed during the 30s window")
	})

	t.Run("flamegraph output fails with the reason", func(t *testing.T) {
		m, publisher, j, _ := setup(t, api.FlameGraph)

		err, _ := m.invoke(j, "42")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no memory growth observed during the 30s window")
		assert.Equal(t, 0, publisher.On("Do").InvokedTimes())
	})
}
