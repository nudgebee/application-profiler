package profiler

import (
	"errors"
	"os/exec"
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

// Test_pprofScrapeError uses the stderr busybox wget (what the images ship)
// actually prints with -q for each failure.
func Test_pprofScrapeError(t *testing.T) {
	exitErr := errors.New("exit status 1")
	const url = "http://127.0.0.1:6060/debug/pprof/profile?seconds=30"
	tests := []struct {
		name   string
		stderr string
		want   string
	}{
		{
			name:   "401 asks for credentials",
			stderr: "wget: server returned error: HTTP/1.1 401 Unauthorized\n",
			want:   "the pprof endpoint on :6060 requires authentication",
		},
		{
			name:   "403 is treated the same",
			stderr: "wget: server returned error: HTTP/1.1 403 Forbidden\n",
			want:   "the pprof endpoint on :6060 requires authentication",
		},
		{
			name:   "404 means pprof is not registered on that port",
			stderr: "wget: server returned error: HTTP/1.0 404 Not Found\n",
			want:   "no /debug/pprof handler on :6060 (is net/http/pprof registered?)",
		},
		{
			name:   "connection refused means nothing listens there",
			stderr: "wget: can't connect to remote host (127.0.0.1): Connection refused\n",
			want:   "nothing listening on :6060",
		},
		{
			name:   "anything else is passed through",
			stderr: "wget: server returned error: HTTP/1.1 500 Internal Server Error\n",
			want: `failed to nsenter+wget "` + url + `" error wget: server returned error: ` +
				`HTTP/1.1 500 Internal Server Error: exit status 1`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.EqualError(t, pprofScrapeError("6060", url, tt.stderr, exitErr), tt.want)
		})
	}
}

// ssOutput fakes `ss -tulnp` run in the target's network namespace.
func ssOutput(lines ...string) *exec.Cmd {
	args := append([]string{"%s\n", "Netid State  Recv-Q Send-Q Local Address:Port Peer Address:Port Process"}, lines...)
	return exec.Command("printf", args...)
}

// wgetFailing fakes wget failing with the given stderr.
func wgetFailing(stderr string) *exec.Cmd {
	return exec.Command("sh", "-c", "printf '%s\\n' \"$1\" >&2; exit 1", "sh", stderr)
}

const listeningOn6060 = `tcp   LISTEN 0      4096       *:6060            *:*    users:(("server",pid=42,fd=3))`

func Test_goPprofManager_fetchProfileFromPID(t *testing.T) {
	setup := func(t *testing.T, output api.OutputType, cmds ...*exec.Cmd) (*goPprofManager, publish.FakePublisher, *job.ProfilingJob) {
		t.Helper()
		tmp := t.TempDir()
		oldTmp := common.TmpDir
		common.TmpDir = func() string { return tmp }
		t.Cleanup(func() { common.TmpDir = oldTmp })

		commander := executil.NewFakeCommander()
		for _, c := range cmds {
			commander.On("Command").Return(c)
		}
		publisher := publish.NewFakePublisher()
		publisher.On("Do").Return(nil)
		j := &job.ProfilingJob{Tool: api.PProf, OutputType: output, Interval: 30 * time.Second, Iteration: 1, PID: "42"}
		return &goPprofManager{commander: commander, publisher: publisher}, publisher, j
	}

	t.Run("an endpoint behind auth says so", func(t *testing.T) {
		m, publisher, j := setup(t, api.Pprof,
			ssOutput(listeningOn6060), wgetFailing("wget: server returned error: HTTP/1.1 401 Unauthorized"))

		err := m.fetchProfileFromPID(j)

		assert.EqualError(t, err, "failed to fetch CPU profile: the pprof endpoint on :6060 requires authentication")
		assert.Equal(t, 0, publisher.On("Do").InvokedTimes())
	})

	t.Run("a heap profile without pprof registered says so", func(t *testing.T) {
		m, publisher, j := setup(t, api.HeapDump,
			ssOutput(listeningOn6060), wgetFailing("wget: server returned error: HTTP/1.1 404 Not Found"))

		err := m.fetchProfileFromPID(j)

		assert.EqualError(t, err,
			"failed to fetch heap profile: no /debug/pprof handler on :6060 (is net/http/pprof registered?)")
		assert.Equal(t, 0, publisher.On("Do").InvokedTimes())
	})

	t.Run("an undetected port says the default was a guess", func(t *testing.T) {
		m, publisher, j := setup(t, api.Pprof,
			ssOutput(), wgetFailing("wget: can't connect to remote host (127.0.0.1): Connection refused"))

		err := m.fetchProfileFromPID(j)

		assert.EqualError(t, err, "could not detect the listening port (no listening port found for PID), "+
			"so tried the default :8080: failed to fetch CPU profile: nothing listening on :8080")
		assert.Equal(t, 0, publisher.On("Do").InvokedTimes())
	})

	t.Run("an undetected port still profiles the default when it answers", func(t *testing.T) {
		m, publisher, j := setup(t, api.Pprof, ssOutput(), exec.Command("true"))

		require.NoError(t, m.fetchProfileFromPID(j))
		assert.Equal(t, 1, publisher.On("Do").InvokedTimes())
	})
}

// TestGoPprofProfiler_Invoke — the reason reaches the run's error under its
// PID, and the shared job is left alone (each PID works on its own copy).
func TestGoPprofProfiler_Invoke(t *testing.T) {
	tmp := t.TempDir()
	oldTmp := common.TmpDir
	common.TmpDir = func() string { return tmp }
	t.Cleanup(func() { common.TmpDir = oldTmp })

	commander := executil.NewFakeCommander()
	commander.On("Command").
		Return(ssOutput()).
		Return(wgetFailing("wget: can't connect to remote host (127.0.0.1): Connection refused"))
	publisher := publish.NewFakePublisher()
	publisher.On("Do").Return(nil)
	p := NewGoPprofProfiler(commander, publisher)
	p.delay = 0
	p.targetPIDs = []string{"42"}
	j := &job.ProfilingJob{Tool: api.PProf, OutputType: api.Pprof, Interval: 30 * time.Second, Iteration: 1}

	err, _ := p.Invoke(j)

	assert.EqualError(t, err, "PID 42: could not detect the listening port (no listening port found for PID), "+
		"so tried the default :8080: failed to fetch CPU profile: nothing listening on :8080")
	assert.Empty(t, j.PID)
}
