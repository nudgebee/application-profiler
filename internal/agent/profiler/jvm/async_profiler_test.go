package jvm

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/nudgebee/application-profiler/api"
	"github.com/nudgebee/application-profiler/internal/agent/config"
	"github.com/nudgebee/application-profiler/internal/agent/job"
	"github.com/nudgebee/application-profiler/internal/agent/profiler/common"
	executil "github.com/nudgebee/application-profiler/internal/agent/util/exec"
	"github.com/nudgebee/application-profiler/internal/agent/util/publish"
	"github.com/nudgebee/application-profiler/pkg/util/compressor"
	"github.com/nudgebee/application-profiler/pkg/util/file"
	"github.com/nudgebee/application-profiler/pkg/util/log"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAsyncProfiler_SetUp(t *testing.T) {
	type fields struct {
		AsyncProfiler *AsyncProfiler
	}
	type args struct {
		job *job.ProfilingJob
	}
	tests := []struct {
		name  string
		given func() (fields, args)
		when  func(fields, args) error
		then  func(t *testing.T, err error, fields fields)
	}{
		{
			name: "should setup",
			given: func() (fields, args) {
				asyncProfilerManager := newFakeAsyncProfilerManager()
				asyncProfilerManager.On("removeTmpDir").Return(nil)
				asyncProfilerManager.On("linkTmpDirToTargetTmpDir").Return(nil)
				asyncProfilerManager.On("copyProfilerToTmpDir").Return(nil)
				asyncProfilerManager.On("selectProfilerLibrary").Return(nil)
				asyncProfilerManager.On("chownProfilerToTarget").Return(nil)

				return fields{
						AsyncProfiler: &AsyncProfiler{
							AsyncProfilerManager: asyncProfilerManager,
						},
					}, args{
						job: &job.ProfilingJob{
							Duration:         0,
							ContainerRuntime: api.FakeContainer,
							ContainerID:      "ContainerID",
						},
					}
			},
			when: func(fields fields, args args) error {
				return fields.AsyncProfiler.SetUp(args.job)
			},
			then: func(t *testing.T, err error, fields fields) {
				assert.Nil(t, err)
				assert.Equal(t, []string{"PID_ContainerID"}, fields.AsyncProfiler.targetPIDs)
				assert.Equal(t, 1, fields.AsyncProfiler.AsyncProfilerManager.(FakeAsyncProfilerManager).On("removeTmpDir").InvokedTimes())
				assert.Equal(t, 1, fields.AsyncProfiler.AsyncProfilerManager.(FakeAsyncProfilerManager).On("linkTmpDirToTargetTmpDir").InvokedTimes())
				assert.Equal(t, 1, fields.AsyncProfiler.AsyncProfilerManager.(FakeAsyncProfilerManager).On("copyProfilerToTmpDir").InvokedTimes())
			},
		},
		{
			name: "should setup when PID is given",
			given: func() (fields, args) {
				asyncProfilerManager := newFakeAsyncProfilerManager()
				asyncProfilerManager.On("removeTmpDir").Return(nil)
				asyncProfilerManager.On("linkTmpDirToTargetTmpDir").Return(nil)
				asyncProfilerManager.On("copyProfilerToTmpDir").Return(nil)
				asyncProfilerManager.On("selectProfilerLibrary").Return(nil)
				asyncProfilerManager.On("chownProfilerToTarget").Return(nil)

				return fields{
						AsyncProfiler: &AsyncProfiler{
							AsyncProfilerManager: asyncProfilerManager,
						},
					}, args{
						job: &job.ProfilingJob{
							Duration:         0,
							ContainerRuntime: api.FakeContainer,
							ContainerID:      "ContainerID",
							PID:              "PID_ContainerID",
						},
					}
			},
			when: func(fields fields, args args) error {
				return fields.AsyncProfiler.SetUp(args.job)
			},
			then: func(t *testing.T, err error, fields fields) {
				assert.Nil(t, err)
				assert.Equal(t, []string{"PID_ContainerID"}, fields.AsyncProfiler.targetPIDs)
				assert.Equal(t, 1, fields.AsyncProfiler.AsyncProfilerManager.(FakeAsyncProfilerManager).On("removeTmpDir").InvokedTimes())
				assert.Equal(t, 1, fields.AsyncProfiler.AsyncProfilerManager.(FakeAsyncProfilerManager).On("linkTmpDirToTargetTmpDir").InvokedTimes())
				assert.Equal(t, 1, fields.AsyncProfiler.AsyncProfilerManager.(FakeAsyncProfilerManager).On("copyProfilerToTmpDir").InvokedTimes())
			},
		},
		{
			name: "should fail when the container runtime is unknown",
			given: func() (fields, args) {
				asyncProfilerManager := newFakeAsyncProfilerManager()
				asyncProfilerManager.On("removeTmpDir").Return(nil)
				asyncProfilerManager.On("linkTmpDirToTargetTmpDir").Return(nil)
				asyncProfilerManager.On("copyProfilerToTmpDir").Return(nil)
				asyncProfilerManager.On("selectProfilerLibrary").Return(nil)
				asyncProfilerManager.On("chownProfilerToTarget").Return(nil)

				return fields{
						AsyncProfiler: &AsyncProfiler{
							AsyncProfilerManager: asyncProfilerManager,
						},
					}, args{
						job: &job.ProfilingJob{
							Duration:         0,
							ContainerRuntime: "other",
							ContainerID:      "ContainerID",
						},
					}
			},
			when: func(fields fields, args args) error {
				return fields.AsyncProfiler.SetUp(args.job)
			},
			then: func(t *testing.T, err error, fields fields) {
				assert.NotNil(t, err)
				assert.Equal(t, 0, fields.AsyncProfiler.AsyncProfilerManager.(FakeAsyncProfilerManager).On("removeTmpDir").InvokedTimes())
				assert.Equal(t, 0, fields.AsyncProfiler.AsyncProfilerManager.(FakeAsyncProfilerManager).On("linkTmpDirToTargetTmpDir").InvokedTimes())
				assert.Equal(t, 0, fields.AsyncProfiler.AsyncProfilerManager.(FakeAsyncProfilerManager).On("copyProfilerToTmpDir").InvokedTimes())
			},
		},
		{
			name: "should fail when removing tmp dir fail",
			given: func() (fields, args) {
				asyncProfilerManager := newFakeAsyncProfilerManager()
				asyncProfilerManager.On("removeTmpDir").Return(errors.New("fake error"))

				return fields{
						AsyncProfiler: &AsyncProfiler{
							AsyncProfilerManager: asyncProfilerManager,
						},
					}, args{
						job: &job.ProfilingJob{
							Duration:         0,
							ContainerRuntime: api.FakeContainer,
							ContainerID:      "ContainerID",
						},
					}
			},
			when: func(fields fields, args args) error {
				return fields.AsyncProfiler.SetUp(args.job)
			},
			then: func(t *testing.T, err error, fields fields) {
				assert.NotNil(t, err)
				assert.EqualError(t, err, "fake error")
				assert.Equal(t, 1, fields.AsyncProfiler.AsyncProfilerManager.(FakeAsyncProfilerManager).On("removeTmpDir").InvokedTimes())
				assert.Equal(t, 0, fields.AsyncProfiler.AsyncProfilerManager.(FakeAsyncProfilerManager).On("linkTmpDirToTargetTmpDir").InvokedTimes())
				assert.Equal(t, 0, fields.AsyncProfiler.AsyncProfilerManager.(FakeAsyncProfilerManager).On("copyProfilerToTmpDir").InvokedTimes())
			},
		},
		{
			name: "should fail when link tmp dir to target tmp dir fail",
			given: func() (fields, args) {
				asyncProfilerManager := newFakeAsyncProfilerManager()
				asyncProfilerManager.On("removeTmpDir").Return(nil)
				asyncProfilerManager.On("linkTmpDirToTargetTmpDir").Return(errors.New("fake error"))

				return fields{
						AsyncProfiler: &AsyncProfiler{
							AsyncProfilerManager: asyncProfilerManager,
						},
					}, args{
						job: &job.ProfilingJob{
							Duration:         0,
							ContainerRuntime: api.FakeContainer,
							ContainerID:      "ContainerID",
						},
					}
			},
			when: func(fields fields, args args) error {
				return fields.AsyncProfiler.SetUp(args.job)
			},
			then: func(t *testing.T, err error, fields fields) {
				assert.NotNil(t, err)
				assert.EqualError(t, err, "fake error")
				assert.Equal(t, 1, fields.AsyncProfiler.AsyncProfilerManager.(FakeAsyncProfilerManager).On("removeTmpDir").InvokedTimes())
				assert.Equal(t, 1, fields.AsyncProfiler.AsyncProfilerManager.(FakeAsyncProfilerManager).On("linkTmpDirToTargetTmpDir").InvokedTimes())
				assert.Equal(t, 0, fields.AsyncProfiler.AsyncProfilerManager.(FakeAsyncProfilerManager).On("copyProfilerToTmpDir").InvokedTimes())
			},
		},
		{
			name: "should fail when container PID not found",
			given: func() (fields, args) {
				asyncProfilerManager := newFakeAsyncProfilerManager()
				asyncProfilerManager.On("removeTmpDir").Return(nil)
				asyncProfilerManager.On("linkTmpDirToTargetTmpDir").Return(nil)
				// PIDs are resolved first now — the target's mount namespace is
				// reached through one of them, so nothing can be staged before.

				return fields{
						AsyncProfiler: &AsyncProfiler{
							AsyncProfilerManager: asyncProfilerManager,
						},
					}, args{
						job: &job.ProfilingJob{
							Duration:         0,
							ContainerRuntime: api.FakeContainerWithPIDResultError,
							ContainerID:      "ContainerID",
						},
					}
			},
			when: func(fields fields, args args) error {
				return fields.AsyncProfiler.SetUp(args.job)
			},
			then: func(t *testing.T, err error, fields fields) {
				assert.NotNil(t, err)
				assert.Equal(t, 0, fields.AsyncProfiler.AsyncProfilerManager.(FakeAsyncProfilerManager).On("removeTmpDir").InvokedTimes())
				assert.Equal(t, 0, fields.AsyncProfiler.AsyncProfilerManager.(FakeAsyncProfilerManager).On("linkTmpDirToTargetTmpDir").InvokedTimes())
				assert.Equal(t, 0, fields.AsyncProfiler.AsyncProfilerManager.(FakeAsyncProfilerManager).On("copyProfilerToTmpDir").InvokedTimes())
			},
		},
		{
			name: "should fail when copy profiler to tmp dir fail",
			given: func() (fields, args) {
				asyncProfilerManager := newFakeAsyncProfilerManager()
				asyncProfilerManager.On("removeTmpDir").Return(nil)
				asyncProfilerManager.On("linkTmpDirToTargetTmpDir").Return(nil)
				asyncProfilerManager.On("copyProfilerToTmpDir").Return(errors.New("fake error"))

				return fields{
						AsyncProfiler: &AsyncProfiler{
							AsyncProfilerManager: asyncProfilerManager,
						},
					}, args{
						job: &job.ProfilingJob{
							Duration:         0,
							ContainerRuntime: api.FakeContainer,
							ContainerID:      "ContainerID",
						},
					}
			},
			when: func(fields fields, args args) error {
				return fields.AsyncProfiler.SetUp(args.job)
			},
			then: func(t *testing.T, err error, fields fields) {
				assert.NotNil(t, err)
				assert.EqualError(t, err, "fake error")
				assert.Equal(t, 1, fields.AsyncProfiler.AsyncProfilerManager.(FakeAsyncProfilerManager).On("removeTmpDir").InvokedTimes())
				assert.Equal(t, 1, fields.AsyncProfiler.AsyncProfilerManager.(FakeAsyncProfilerManager).On("linkTmpDirToTargetTmpDir").InvokedTimes())
				assert.Equal(t, 1, fields.AsyncProfiler.AsyncProfilerManager.(FakeAsyncProfilerManager).On("copyProfilerToTmpDir").InvokedTimes())
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			fields, args := tt.given()

			// When
			err := tt.when(fields, args)

			// Then
			tt.then(t, err, fields)
		})
	}
}

func TestAsyncProfiler_Invoke(t *testing.T) {
	type fields struct {
		AsyncProfiler AsyncProfiler
	}
	type args struct {
		job *job.ProfilingJob
	}
	tests := []struct {
		name  string
		given func() (fields, args)
		when  func(fields, args) (error, time.Duration)
		then  func(t *testing.T, err error, fields fields)
	}{
		{
			name: "should publish result",
			given: func() (fields, args) {
				asyncProfilerManager := newFakeAsyncProfilerManager()
				asyncProfilerManager.On("invoke").
					Return(nil, time.Duration(0)).
					Return(nil, time.Duration(0))

				return fields{
						AsyncProfiler: AsyncProfiler{
							AsyncProfilerManager: asyncProfilerManager,
						},
					}, args{
						job: &job.ProfilingJob{
							Duration:         0,
							ContainerRuntime: api.FakeContainer,
							ContainerID:      "ContainerID",
						},
					}
			},
			when: func(fields fields, args args) (error, time.Duration) {
				fields.AsyncProfiler.delay = 0
				fields.AsyncProfiler.targetPIDs = []string{"1000", "2000"}
				return fields.AsyncProfiler.Invoke(args.job)
			},
			then: func(t *testing.T, err error, fields fields) {
				assert.Nil(t, err)
				assert.Equal(t, 2, fields.AsyncProfiler.AsyncProfilerManager.(FakeAsyncProfilerManager).On("invoke").InvokedTimes())
			},
		},
		{
			name: "should invoke fail when invoke fail",
			given: func() (fields, args) {
				asyncProfilerManager := newFakeAsyncProfilerManager()
				asyncProfilerManager.On("invoke").Return(errors.New("fake invoke error"), time.Duration(0))

				return fields{
						AsyncProfiler: AsyncProfiler{
							AsyncProfilerManager: asyncProfilerManager,
						},
					}, args{
						job: &job.ProfilingJob{
							Duration:         0,
							ContainerRuntime: api.FakeContainer,
							ContainerID:      "ContainerID",
							OutputType:       api.FlameGraph,
						},
					}
			},
			when: func(fields fields, args args) (error, time.Duration) {
				fields.AsyncProfiler.delay = 0
				fields.AsyncProfiler.targetPIDs = []string{"1000", "2000"}
				return fields.AsyncProfiler.Invoke(args.job)
			},
			then: func(t *testing.T, err error, fields fields) {
				require.Error(t, err)
				assert.EqualError(t, err, "fake invoke error")
				assert.Equal(t, 1, fields.AsyncProfiler.AsyncProfilerManager.(FakeAsyncProfilerManager).On("invoke").InvokedTimes())
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			fields, args := tt.given()

			// When
			err, _ := tt.when(fields, args)

			// Then
			tt.then(t, err, fields)
		})
	}
}

func TestAsyncProfiler_CleanUp(t *testing.T) {
	type fields struct {
		AsyncProfiler AsyncProfiler
	}
	type args struct {
		job *job.ProfilingJob
	}
	tests := []struct {
		name  string
		given func() (fields, args)
		when  func(fields, args) error
		then  func(t *testing.T, err error, fields fields)
	}{
		{
			name: "should clean up",
			given: func() (fields, args) {
				_ = os.Mkdir(filepath.Join(common.TmpDir(), "async-profiler"), os.ModePerm)
				f := filepath.Join(common.TmpDir(), config.ProfilingPrefix+"flamegraph.html")
				_, _ = os.Create(f)
				_, _ = os.Create(f + compressor.GetExtensionFileByCompressor[compressor.Gzip])
				return fields{
						AsyncProfiler: AsyncProfiler{
							AsyncProfilerManager: newFakeAsyncProfilerManager(),
						},
					}, args{
						job: &job.ProfilingJob{
							UID:              "UID",
							Duration:         0,
							ContainerRuntime: api.FakeContainer,
							ContainerID:      "ContainerID",
							Compressor:       compressor.Gzip,
							Tool:             api.AsyncProfiler,
							OutputType:       api.FlameGraph,
						},
					}
			},
			when: func(fields fields, args args) error {
				return fields.AsyncProfiler.CleanUp(args.job)
			},
			then: func(t *testing.T, err error, fields fields) {
				f := filepath.Join(common.TmpDir(), config.ProfilingPrefix+"flamegraph.html")
				g := filepath.Join(common.TmpDir(), config.ProfilingPrefix+"flamegraph.html"+
					compressor.GetExtensionFileByCompressor[compressor.Gzip])
				assert.False(t, file.Exists(f))
				assert.False(t, file.Exists(g))
				assert.False(t, file.Exists(filepath.Join(common.TmpDir(), "async-profiler")))
				assert.Nil(t, err)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			fields, args := tt.given()

			// When
			err := tt.when(fields, args)

			// Then
			tt.then(t, err, fields)
		})
	}
}

func Test_asyncProfilerManager_copyProfilerToTmpDir(t *testing.T) {
	commander := executil.NewFakeCommander()
	commander.On("Command").Return(exec.Command("ls", common.TmpDir()))
	publisher := publish.NewFakePublisher()
	a := NewAsyncProfiler(commander, publisher)
	assert.Nil(t, a.copyProfilerToTmpDir())
}

// Test_targetUsesMusl — libasyncProfiler.so is dlopen'd by the target JVM, so
// getting this wrong fails the attach with "libc.musl-x86_64.so.1: cannot open
// shared object file" and the profile comes back empty.
func Test_targetUsesMusl(t *testing.T) {
	tests := []struct {
		name   string
		create string // file to create under the fake target rootfs
		want   bool
	}{
		{name: "alpine target", create: "lib/ld-musl-x86_64.so.1", want: true},
		{name: "alpine target on arm", create: "lib/ld-musl-aarch64.so.1", want: true},
		{name: "musl loader under usr/lib", create: "usr/lib/ld-musl-x86_64.so.1", want: true},
		{name: "glibc target", create: "lib/ld-linux-x86-64.so.2", want: false},
		{name: "empty rootfs", create: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.create != "" {
				p := filepath.Join(root, tt.create)
				require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
				_, err := os.Create(p)
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, targetUsesMusl(root))
		})
	}

	// An unresolved root must not make the glob relative to our own working
	// directory: this image is alpine, so that would report every target as
	// musl-based.
	t.Run("empty target root", func(t *testing.T) {
		assert.False(t, targetUsesMusl(""))
	})
}

// Test_asyncProfilerManager_selectProfilerLibrary — a glibc target must be
// left alone (the shipped default is already glibc); only a musl target
// triggers the copy that swaps the library.
func Test_asyncProfilerManager_selectProfilerLibrary(t *testing.T) {
	t.Run("glibc target does not copy", func(t *testing.T) {
		commander := executil.NewFakeCommander()
		commander.On("Command").Return(exec.Command("false"))
		a := NewAsyncProfiler(commander, publish.NewFakePublisher())

		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "lib"), 0o755))
		_, err := os.Create(filepath.Join(root, "lib", "ld-linux-x86-64.so.2"))
		require.NoError(t, err)

		// `false` would error if it ran — a nil result proves it did not.
		assert.Nil(t, a.selectProfilerLibrary(root))
	})

	t.Run("musl target copies the musl build over the default", func(t *testing.T) {
		commander := executil.NewFakeCommander()
		commander.On("Command").Return(exec.Command("true"))
		a := NewAsyncProfiler(commander, publish.NewFakePublisher())

		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "lib"), 0o755))
		_, err := os.Create(filepath.Join(root, "lib", "ld-musl-x86_64.so.1"))
		require.NoError(t, err)

		assert.Nil(t, a.selectProfilerLibrary(root))
		assert.Equal(t, 1, commander.On("Command").InvokedTimes())
	})
}

// Test_asyncProfilerManager_chownProfilerToTarget — we stage as root, but the
// JVM reads the library and writes its own output into that directory as
// whatever user it runs as, which in hardened images is not root.
func Test_asyncProfilerManager_chownProfilerToTarget(t *testing.T) {
	t.Run("a root target needs no chown", func(t *testing.T) {
		requireProcFS(t)
		if os.Getuid() != 0 {
			t.Skip("this test reads our own /proc entry, so it only says root when we are")
		}
		commander := executil.NewFakeCommander()
		commander.On("Command").Return(exec.Command("false"))
		a := NewAsyncProfiler(commander, publish.NewFakePublisher())

		assert.Nil(t, a.chownProfilerToTarget("self"))
		assert.Equal(t, 0, commander.On("Command").InvokedTimes())
	})

	t.Run("a non-root target is handed the staged directory", func(t *testing.T) {
		requireProcFS(t)
		if os.Getuid() == 0 {
			t.Skip("running as root, so our own /proc entry cannot stand in for a non-root target")
		}
		commander := executil.NewFakeCommander()
		commander.On("Command").Return(exec.Command("true"))
		a := NewAsyncProfiler(commander, publish.NewFakePublisher())

		assert.Nil(t, a.chownProfilerToTarget("self"))
		assert.Equal(t, 1, commander.On("Command").InvokedTimes())
	})

	t.Run("an unreadable pid is an error, not a silent skip", func(t *testing.T) {
		commander := executil.NewFakeCommander()
		commander.On("Command").Return(exec.Command("true"))
		a := NewAsyncProfiler(commander, publish.NewFakePublisher())

		assert.NotNil(t, a.chownProfilerToTarget("not-a-pid"))
	})
}

// requireProcFS skips on platforms without /proc — the credentials come from
// /proc/<pid>/status, which only exists on the Linux boxes this runs on.
func requireProcFS(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/proc/self/status"); err != nil {
		t.Skip("no procfs on this platform")
	}
}

func Test_asyncProfilerManager_invoke(t *testing.T) {
	type fields struct {
		AsyncProfiler *AsyncProfiler
	}
	type args struct {
		job *job.ProfilingJob
		pid string
	}
	tests := []struct {
		name  string
		given func() (fields, args)
		when  func(fields, args) (error, time.Duration)
		then  func(t *testing.T, fields fields, err error)
		after func()
	}{
		{
			name: "should invoke",
			given: func() (fields, args) {
				log.SetPrintLogs(true)
				var b bytes.Buffer
				b.Write([]byte("test"))
				file.Write(filepath.Join(common.TmpDir(), config.ProfilingPrefix+"raw-1000.txt"), b.String())
				file.Write(filepath.Join(common.TmpDir(), config.ProfilingPrefix+"flamegraph-1000.svg"), b.String())

				commander := executil.NewFakeCommander()
				commander.On("Command").Return(exec.Command("ls", common.TmpDir()))
				publisher := publish.NewFakePublisher()
				publisher.On("Do").Return(nil)

				return fields{
						AsyncProfiler: NewAsyncProfiler(commander, publisher),
					}, args{
						job: &job.ProfilingJob{
							Duration:         0,
							ContainerRuntime: api.FakeContainer,
							ContainerID:      "ContainerID",
							OutputType:       api.FlameGraph,
							Language:         api.FakeLang,
							Tool:             api.AsyncProfiler,
							Compressor:       compressor.None,
						},
						pid: "1000",
					}
			},
			when: func(fields fields, args args) (error, time.Duration) {
				return fields.AsyncProfiler.invoke(args.job, args.pid)
			},
			then: func(t *testing.T, fields fields, err error) {
				assert.Nil(t, err)
				assert.True(t, file.Exists(filepath.Join(common.TmpDir(), config.ProfilingPrefix+"flamegraph-1000.svg")))
				assert.True(t, fields.AsyncProfiler.AsyncProfilerManager.(*asyncProfilerManager).publisher.(*publish.Fake).On("Do").InvokedTimes() == 1)
			},
			after: func() {
				_ = file.Remove(filepath.Join(common.TmpDir(), config.ProfilingPrefix+"raw-1000.txt"))
				_ = file.Remove(filepath.Join(common.TmpDir(), config.ProfilingPrefix+"flamegraph-1000.svg"))
			},
		},
		{
			name: "should invoke fail when command fail",
			given: func() (fields, args) {
				commander := executil.NewFakeCommander()
				commander.On("Command").Return(&exec.Cmd{})
				publisher := publish.NewFakePublisher()
				publisher.On("Do").Return(nil)

				return fields{
						AsyncProfiler: NewAsyncProfiler(commander, publisher),
					}, args{
						job: &job.ProfilingJob{
							Duration:         0,
							ContainerRuntime: api.FakeContainer,
							ContainerID:      "ContainerID",
							OutputType:       api.FlameGraph,
							Language:         api.Java,
							Tool:             api.AsyncProfiler,
						},
						pid: "1000",
					}
			},
			when: func(fields fields, args args) (error, time.Duration) {
				return fields.AsyncProfiler.invoke(args.job, args.pid)
			},
			then: func(t *testing.T, fields fields, err error) {
				require.Error(t, err)
				assert.True(t, fields.AsyncProfiler.AsyncProfilerManager.(*asyncProfilerManager).publisher.(*publish.Fake).On("Do").InvokedTimes() == 0)
			},
		},
		{
			name: "should invoke fail when publish result fail",
			given: func() (fields, args) {
				log.SetPrintLogs(true)
				var b bytes.Buffer
				b.Write([]byte("test"))
				file.Write(filepath.Join(common.TmpDir(), config.ProfilingPrefix+"raw-1000.txt"), b.String())
				file.Write(filepath.Join(common.TmpDir(), config.ProfilingPrefix+"flamegraph-1000.svg"), b.String())

				commander := executil.NewFakeCommander()
				// mock commander.Command return exec.Command("ls", common.TmpDir())
				commander.On("Command").Return(exec.Command("ls", common.TmpDir()))
				publisher := publish.NewFakePublisher()
				// mock publisher.Do return error
				publisher.On("Do").Return(errors.New("fake publisher with error"))

				return fields{
						AsyncProfiler: NewAsyncProfiler(commander, publisher),
					}, args{
						job: &job.ProfilingJob{
							Duration:             0,
							ContainerRuntime:     api.FakeContainer,
							ContainerRuntimePath: common.TmpDir(),
							ContainerID:          "ContainerID",
							OutputType:           api.FlameGraph,
							Language:             api.FakeLang,
							Tool:                 api.AsyncProfiler,
							Compressor:           compressor.None,
						},
						pid: "1000",
					}
			},
			when: func(fields fields, args args) (error, time.Duration) {
				return fields.AsyncProfiler.invoke(args.job, args.pid)
			},
			then: func(t *testing.T, fields fields, err error) {
				require.Error(t, err)
				assert.ErrorContains(t, err, "fake publisher with error")
				assert.True(t, file.Exists(filepath.Join(common.TmpDir(), config.ProfilingPrefix+"flamegraph-1000.svg")))
				assert.True(t, fields.AsyncProfiler.AsyncProfilerManager.(*asyncProfilerManager).publisher.(*publish.Fake).On("Do").InvokedTimes() == 1)
			},
			after: func() {
				_ = file.Remove(filepath.Join(common.TmpDir(), config.ProfilingPrefix+"raw-1000.txt"))
				_ = file.Remove(filepath.Join(common.TmpDir(), config.ProfilingPrefix+"flamegraph-1000.svg"))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			fields, args := tt.given()

			// When
			err, _ := tt.when(fields, args)

			// Then
			tt.then(t, fields, err)

			if tt.after != nil {
				tt.after()
			}
		})
	}
}

func Test_asyncProfilerManager_cleanUp(t *testing.T) {
	commander := executil.NewFakeCommander()
	commander.On("Command").Return(exec.Command("ls", common.TmpDir()))
	publisher := publish.NewFakePublisher()
	a := NewAsyncProfiler(commander, publisher)
	a.cleanUp(&job.ProfilingJob{
		Duration:         0,
		ContainerRuntime: api.FakeContainer,
		ContainerID:      "ContainerID",
		OutputType:       api.FlameGraph,
		Language:         api.FakeLang,
		Tool:             api.AsyncProfiler,
		Compressor:       compressor.None,
	}, "1000")
	assert.True(t, commander.(*executil.Fake).On("Command").InvokedTimes() == 1)

	commander = executil.NewFakeCommander()
	commander.On("Command").Return(&exec.Cmd{})
	a = NewAsyncProfiler(commander, publisher)
	a.cleanUp(&job.ProfilingJob{
		Duration:         0,
		ContainerRuntime: api.FakeContainer,
		ContainerID:      "ContainerID",
		OutputType:       api.FlameGraph,
		Language:         api.FakeLang,
		Tool:             api.AsyncProfiler,
		Compressor:       compressor.None,
	}, "1000")
	assert.True(t, commander.(*executil.Fake).On("Command").InvokedTimes() == 1)
}
