package health

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDocker lets a test script the responses of Status(name) per call.
type fakeDocker struct {
	mu     sync.Mutex
	byName map[string]string
	errFn  func(name string) error
	calls  atomic.Int64
}

func (f *fakeDocker) Status(_ context.Context, name string) (string, error) {
	f.calls.Add(1)
	if f.errFn != nil {
		if err := f.errFn(name); err != nil {
			return "", err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byName[name], nil
}

func (f *fakeDocker) set(name, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.byName == nil {
		f.byName = make(map[string]string)
	}
	f.byName[name] = status
}

// classify is the pure function — easy to fully cover without any DB.

func TestClassify(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "stopped"},
		{"exited", "stopped"},
		{"dead", "failed"},
		{"paused", "stopped"},
		{"running", ""},
		{"created", ""},
		{"restarting", ""},
		{"unknown-state", ""},
	}
	for _, tc := range cases {
		got := classify(tc.in)
		if got != tc.want {
			t.Errorf("classify(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestConfigSane(t *testing.T) {
	got := Config{Interval: 0}.sane()
	if got.Interval != 30*time.Second {
		t.Errorf("expected 30s default interval; got %v", got.Interval)
	}
	if got.StatusTimeout != 5*time.Second {
		t.Errorf("expected 5s default status timeout; got %v", got.StatusTimeout)
	}

	got = Config{Interval: 100 * time.Millisecond}.sane()
	if got.Interval != 30*time.Second {
		t.Errorf("sub-second interval should be clamped to default; got %v", got.Interval)
	}

	got = Config{Interval: 5 * time.Minute, StatusTimeout: time.Second}.sane()
	if got.Interval != 5*time.Minute || got.StatusTimeout != time.Second {
		t.Errorf("explicit values should pass through; got %+v", got)
	}
}

// fakeDocker behaviour test — independent of the worker plumbing.
func TestFakeDockerStatus(t *testing.T) {
	f := &fakeDocker{}
	f.set("foo", "running")
	if got, _ := f.Status(context.Background(), "foo"); got != "running" {
		t.Errorf("expected running, got %q", got)
	}
	if got, _ := f.Status(context.Background(), "missing"); got != "" {
		t.Errorf("expected empty for missing, got %q", got)
	}
	if calls := f.calls.Load(); calls != 2 {
		t.Errorf("expected 2 calls, got %d", calls)
	}
}

// fakeDocker error path.
func TestFakeDockerError(t *testing.T) {
	want := errors.New("daemon down")
	f := &fakeDocker{
		errFn: func(string) error { return want },
	}
	if _, err := f.Status(context.Background(), "any"); !errors.Is(err, want) {
		t.Errorf("expected error, got %v", err)
	}
}

// reconcile() / sweep() / Run() tests need a postgres pool. Those live
// alongside the existing internal/test integration suite — see
// internal/test/health_test.go for the full-stack worker checks.
