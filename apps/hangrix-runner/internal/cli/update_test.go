package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/client"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/store"
	runnerv1 "github.com/hangrix/hangrix/gen/go/hangrix/runner/v1"
	"github.com/hangrix/hangrix/gen/go/hangrix/runner/v1/runnerv1connect"
)

// fakeRunnerService stubs every Connect RPC the runner client knows
// about with CodeUnimplemented so a test accidentally exercising one
// gets a loud failure instead of a silent zero. Bootstrap is NOT here
// — it moved off Connect to plain JSON over /api/runner/bootstrap
// (see binary.go in apps/hangrix), so the test server mounts a chi
// route for it directly in newUpdateTestServer.
type fakeRunnerService struct{}

func (s *fakeRunnerService) Enroll(context.Context, *connect.Request[runnerv1.EnrollRequest]) (*connect.Response[runnerv1.EnrollResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("Enroll not stubbed"))
}

func (s *fakeRunnerService) Heartbeat(context.Context, *connect.Request[runnerv1.HeartbeatRequest]) (*connect.Response[runnerv1.HeartbeatResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("Heartbeat not stubbed"))
}

func (s *fakeRunnerService) Tasks(context.Context, *connect.Request[runnerv1.TasksRequest]) (*connect.Response[runnerv1.TasksResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("Tasks not stubbed"))
}

func (s *fakeRunnerService) ListCleanupTasks(context.Context, *connect.Request[runnerv1.ListCleanupTasksRequest]) (*connect.Response[runnerv1.ListCleanupTasksResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("ListCleanupTasks not stubbed"))
}

func (s *fakeRunnerService) MarkCleanupDone(context.Context, *connect.Request[runnerv1.MarkCleanupDoneRequest]) (*connect.Response[runnerv1.MarkCleanupDoneResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("MarkCleanupDone not stubbed"))
}

func (s *fakeRunnerService) ListStopTasks(context.Context, *connect.Request[runnerv1.ListStopTasksRequest]) (*connect.Response[runnerv1.ListStopTasksResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("ListStopTasks not stubbed"))
}

func (s *fakeRunnerService) MarkStopDone(context.Context, *connect.Request[runnerv1.MarkStopDoneRequest]) (*connect.Response[runnerv1.MarkStopDoneResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("MarkStopDone not stubbed"))
}

func (s *fakeRunnerService) MarkWorkflowJobRunning(context.Context, *connect.Request[runnerv1.MarkWorkflowJobRunningRequest]) (*connect.Response[runnerv1.MarkWorkflowJobRunningResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("MarkWorkflowJobRunning not stubbed"))
}

func (s *fakeRunnerService) AppendWorkflowJobLog(context.Context, *connect.Request[runnerv1.AppendWorkflowJobLogRequest]) (*connect.Response[runnerv1.AppendWorkflowJobLogResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("AppendWorkflowJobLog not stubbed"))
}

func (s *fakeRunnerService) ReportWorkflowStepResult(context.Context, *connect.Request[runnerv1.ReportWorkflowStepResultRequest]) (*connect.Response[runnerv1.ReportWorkflowStepResultResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("ReportWorkflowStepResult not stubbed"))
}

func (s *fakeRunnerService) TerminateWorkflowJob(context.Context, *connect.Request[runnerv1.TerminateWorkflowJobRequest]) (*connect.Response[runnerv1.TerminateWorkflowJobResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("TerminateWorkflowJob not stubbed"))
}

func (s *fakeRunnerService) RegisterWorkflowJobPhase(context.Context, *connect.Request[runnerv1.RegisterWorkflowJobPhaseRequest]) (*connect.Response[runnerv1.RegisterWorkflowJobPhaseResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("RegisterWorkflowJobPhase not stubbed"))
}

func (s *fakeRunnerService) MarkWorkflowJobPhaseRunning(context.Context, *connect.Request[runnerv1.MarkWorkflowJobPhaseRunningRequest]) (*connect.Response[runnerv1.MarkWorkflowJobPhaseRunningResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("MarkWorkflowJobPhaseRunning not stubbed"))
}

func (s *fakeRunnerService) TerminateWorkflowJobPhase(context.Context, *connect.Request[runnerv1.TerminateWorkflowJobPhaseRequest]) (*connect.Response[runnerv1.TerminateWorkflowJobPhaseResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("TerminateWorkflowJobPhase not stubbed"))
}

// authMW wraps a handler with a "must be Bearer <expected>" check.
// Used to assert the runner's Connect calls + binary download both
// carry the agent token. Returns 401 otherwise so the runner client
// surfaces the error the same way as a real server would.
func authMW(expected string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if got != "Bearer "+expected {
			http.Error(w, "missing/wrong auth: "+got, http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// newUpdateTestServer wires a chi.Router with:
//   - GET /api/runner/bootstrap → JSON encoding of the supplied
//     payload (matches the real server: Bootstrap moved off Connect
//     to plain HTTP so a stale-Connect runner can still self-update).
//   - GET /api/runner/binaries/{name} → raw bytes + X-Hangrix-SHA256
//     header (binary download stays HTTP per design).
//   - The Connect service is mounted too so any accidental Connect
//     RPC hits the loud Unimplemented stub instead of 404'ing.
//
// All three branches require Bearer <agentToken>. Returns a started
// httptest.Server and counters the caller can inspect.
func newUpdateTestServer(t *testing.T, agentToken string, bp *client.BootstrapPayload, binBody []byte, binSHA string) (server *httptest.Server, bootstrapHits, downloadHits *int) {
	t.Helper()
	stub := &fakeRunnerService{}
	connPath, connHandler := runnerv1connect.NewRunnerServiceHandler(stub)

	var bh, dh int
	bootstrapHits, downloadHits = &bh, &dh

	r := chi.NewRouter()
	r.Mount(connPath, authMW(agentToken, connHandler))
	r.Get("/api/runner/bootstrap", authMW(agentToken, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bh++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(bp)
	})).ServeHTTP)
	r.Get("/api/runner/binaries/{name}", authMW(agentToken, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dh++
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Hangrix-SHA256", binSHA)
		_, _ = w.Write(binBody)
	})).ServeHTTP)

	server = httptest.NewServer(r)
	return server, bootstrapHits, downloadHits
}

// TestUpdateReplacesBinary stands up a fake platform that serves a
// pretend hangrix-runner build for the test runtime's GOOS/GOARCH,
// then asks `Update` to replace a stub binary in a temp dir with it.
// The test exercises:
//   - state load
//   - bootstrap fetch (Connect) + asset lookup
//   - SHA check (mismatch → install path)
//   - DownloadBinary call with bearer auth (plain HTTP)
//   - swapBinary atomic rename
func TestUpdateReplacesBinary(t *testing.T) {
	// No t.Parallel — uses t.Setenv to redirect selfPath().
	const agentToken = "hgxa_test_token"
	newBytes := []byte("fake-runner-binary-payload\n")
	newSum := sha256.Sum256(newBytes)
	newSHA := hex.EncodeToString(newSum[:])
	asset := fmt.Sprintf("hangrix-runner_%s_%s", runtime.GOOS, runtime.GOARCH)
	binaryPath := "/api/runner/binaries/" + asset

	bp := &client.BootstrapPayload{
		BaseURL: "http://platform.example",
		Binaries: map[string]client.BinaryInfo{
			asset: {
				URL:    binaryPath,
				Name:   "hangrix-runner",
				GOOS:   runtime.GOOS,
				GOARCH: runtime.GOARCH,
				SHA256: newSHA,
				Size:   int64(len(newBytes)),
			},
		},
		PollWaitSec:  25,
		HeartbeatSec: 20,
	}
	platform, bootstrapHits, downloadHits := newUpdateTestServer(t, agentToken, bp, newBytes, newSHA)
	defer platform.Close()

	stateDir := t.TempDir()
	if err := store.Save(stateDir, &store.State{
		Server:     platform.URL,
		RunnerID:   42,
		RunnerName: "test-runner",
		AgentToken: agentToken,
	}); err != nil {
		t.Fatalf("save state: %v", err)
	}

	// Stand up a stub "binary" in a temp dir and aim Update at it via
	// the HANGRIX_TEST_SELF_PATH override. This decouples the test from
	// the real go-test binary, which we obviously don't want to swap.
	binDir := t.TempDir()
	exe := filepath.Join(binDir, "hangrix-runner")
	oldBytes := []byte("old-runner-binary\n")
	if err := os.WriteFile(exe, oldBytes, 0o755); err != nil {
		t.Fatalf("write stub binary: %v", err)
	}
	t.Setenv(testSelfPathEnv, exe)

	cfg := &config.Config{StateDir: stateDir}
	if err := Update(context.Background(), cfg); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("read replaced binary: %v", err)
	}
	if string(got) != string(newBytes) {
		t.Fatalf("binary not replaced: got %q want %q", got, newBytes)
	}
	if *bootstrapHits != 1 {
		t.Fatalf("bootstrap hits: got %d want 1", *bootstrapHits)
	}
	if *downloadHits != 1 {
		t.Fatalf("download hits: got %d want 1", *downloadHits)
	}

	// Second run with the same server-side SHA must be a no-op: no
	// download, but bootstrap is still hit (Update always refreshes so
	// it can see a server rollback).
	if err := Update(context.Background(), cfg); err != nil {
		t.Fatalf("Update idempotent run: %v", err)
	}
	if *bootstrapHits != 2 {
		t.Fatalf("second bootstrap hits: got %d want 2", *bootstrapHits)
	}
	if *downloadHits != 1 {
		t.Fatalf("second download hits: got %d want 1 (should not redownload)", *downloadHits)
	}

	// --force makes it redownload even on SHA match.
	cfg.Force = true
	if err := Update(context.Background(), cfg); err != nil {
		t.Fatalf("Update --force: %v", err)
	}
	if *downloadHits != 2 {
		t.Fatalf("forced download hits: got %d want 2", *downloadHits)
	}
}

// TestUpdateRejectsSHAMismatch verifies that bytes whose digest doesn't
// match the bootstrap claim are refused. This is the only line of
// defense against a tampered binary endpoint: bootstrap auth proves the
// payload metadata came from the platform; the body hash check proves
// the bytes we're about to install are those metadata's bytes.
func TestUpdateRejectsSHAMismatch(t *testing.T) {
	// No t.Parallel — uses t.Setenv to redirect selfPath().
	const agentToken = "hgxa_test_token"
	asset := fmt.Sprintf("hangrix-runner_%s_%s", runtime.GOOS, runtime.GOARCH)
	binaryPath := "/api/runner/binaries/" + asset
	claimedSHA := strings.Repeat("0", 64)

	bp := &client.BootstrapPayload{
		Binaries: map[string]client.BinaryInfo{
			asset: {
				URL:    binaryPath,
				SHA256: claimedSHA,
			},
		},
	}
	platform, _, _ := newUpdateTestServer(t, agentToken, bp, []byte("totally-different-bytes"), "")
	defer platform.Close()

	stateDir := t.TempDir()
	if err := store.Save(stateDir, &store.State{
		Server: platform.URL, RunnerID: 1, RunnerName: "x", AgentToken: agentToken,
	}); err != nil {
		t.Fatalf("save state: %v", err)
	}
	binDir := t.TempDir()
	exe := filepath.Join(binDir, "hangrix-runner")
	if err := os.WriteFile(exe, []byte("untouched"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv(testSelfPathEnv, exe)

	err := Update(context.Background(), &config.Config{StateDir: stateDir})
	if err == nil {
		t.Fatalf("expected SHA mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("unexpected error: %v", err)
	}

	got, _ := os.ReadFile(exe)
	if string(got) != "untouched" {
		t.Fatalf("binary should not have been overwritten on SHA mismatch; got %q", got)
	}
}

// TestSwapBinaryAtomic exercises the install path on its own — a tmp
// file in the destination directory, atomic rename, 0755 perm bit.
func TestSwapBinaryAtomic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	exe := filepath.Join(dir, "hangrix-runner")
	if err := os.WriteFile(exe, []byte("v1"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := swapBinary(exe, []byte("v2-and-then-some")); err != nil {
		t.Fatalf("swap: %v", err)
	}
	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "v2-and-then-some" {
		t.Fatalf("body: got %q want %q", got, "v2-and-then-some")
	}
	info, err := os.Stat(exe)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o755 {
		t.Fatalf("mode: got %o want 0755", mode)
	}
	// No leftover .hangrix-runner.* tmp files in the directory.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".hangrix-runner.") {
			t.Fatalf("tmp file leaked: %s", e.Name())
		}
	}
}
