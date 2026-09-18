package profiler

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

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
