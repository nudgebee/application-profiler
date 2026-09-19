package util

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTargetRootFS locks the path we reach the target through. The runtime's
// overlay directory is NOT equivalent: anything mounted over the container's
// root (an emptyDir at /tmp is the common one) resolves differently inside the
// container, and a file staged through the overlay is invisible to it.
func TestTargetRootFS(t *testing.T) {
	assert.Equal(t, "/proc/1234/root", TargetRootFS("1234"))
	assert.Equal(t, "/proc/1234/root/tmp", filepath.Join(TargetRootFS("1234"), "tmp"))
}

// TestTargetCredentials — we stage as root, but the target reads the library
// and writes its own output, so ownership has to follow the target.
func TestTargetCredentials(t *testing.T) {
	t.Run("reads our own process", func(t *testing.T) {
		if _, err := os.Stat("/proc/self/status"); err != nil {
			t.Skip("no procfs on this platform")
		}
		uid, gid, err := TargetCredentials("self")
		require.NoError(t, err)
		assert.Equal(t, strconv.Itoa(os.Getuid()), uid)
		assert.Equal(t, strconv.Itoa(os.Getgid()), gid)
	})

	t.Run("unknown pid", func(t *testing.T) {
		_, _, err := TargetCredentials("not-a-pid")
		assert.Error(t, err)
	})
}
