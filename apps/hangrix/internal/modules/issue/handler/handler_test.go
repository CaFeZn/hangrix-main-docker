package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	agentsessiondomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	orgdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	repoinfra "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/infra"
	userdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
)

// ---------------------------------------------------------------------------
// test fakes for state-transition tests
// ---------------------------------------------------------------------------

type fakePatchStore struct {
	domain.Store
	getByNumber     func(ctx context.Context, repoID, number int64) (*domain.Issue, error)
	getByID         func(ctx context.Context, id int64) (*domain.Issue, error)
	updateState     func(ctx context.Context, id int64, state domain.State, mergeSHA string) (*domain.Issue, error)
	updateTitleBody func(ctx context.Context, id int64, title, body string) (*domain.Issue, error)
	createEvent     func(ctx context.Context, issueID int64, kind domain.EventKind, payload []byte, actorID int64, actorName string) (*domain.Event, error)
	listOpenDesc    func(ctx context.Context, rootID int64) ([]*domain.OpenDescendant, error)
}

func (f *fakePatchStore) GetByNumber(ctx context.Context, repoID, number int64) (*domain.Issue, error) {
	return f.getByNumber(ctx, repoID, number)
}
func (f *fakePatchStore) GetByID(ctx context.Context, id int64) (*domain.Issue, error) {
	return f.getByID(ctx, id)
}
func (f *fakePatchStore) UpdateState(ctx context.Context, id int64, state domain.State, mergeSHA string) (*domain.Issue, error) {
	return f.updateState(ctx, id, state, mergeSHA)
}
func (f *fakePatchStore) UpdateTitleBody(ctx context.Context, id int64, title, body string) (*domain.Issue, error) {
	return f.updateTitleBody(ctx, id, title, body)
}
func (f *fakePatchStore) CreateEvent(ctx context.Context, issueID int64, kind domain.EventKind, payload []byte, actorID int64, actorName string) (*domain.Event, error) {
	return f.createEvent(ctx, issueID, kind, payload, actorID, actorName)
}
func (f *fakePatchStore) ListOpenDescendants(ctx context.Context, rootID int64) ([]*domain.OpenDescendant, error) {
	return f.listOpenDesc(ctx, rootID)
}

// fakeSpawner captures the last TriggerInput passed to OnTrigger.
type fakeSpawner struct {
	lastTrigger *agentsessiondomain.TriggerInput
}

func (s *fakeSpawner) OnTrigger(_ context.Context, in agentsessiondomain.TriggerInput) ([]agentsessiondomain.SpawnedSession, error) {
	s.lastTrigger = &in
	return nil, nil
}

func (s *fakeSpawner) LoadHostConfig(_ context.Context, repoID int64) (*agentsconfig.HostConfig, error) {
	return nil, nil
}

type fakePatchRepoStore struct {
	repodomain.Store
	getByOwnerAndName func(ownerKind repodomain.OwnerKind, ownerID int64, name string) (*repodomain.Repo, error)
}

func (f *fakePatchRepoStore) GetByOwnerAndName(ctx context.Context, ownerKind repodomain.OwnerKind, ownerID int64, name string) (*repodomain.Repo, error) {
	return f.getByOwnerAndName(ownerKind, ownerID, name)
}

type fakePatchResolver struct {
	resolveOwner func(name string) (*orgdomain.Owner, error)
}

func (f *fakePatchResolver) ResolveOwner(_ context.Context, name string) (*orgdomain.Owner, error) {
	return f.resolveOwner(name)
}
func (f *fakePatchResolver) Membership(_ context.Context, orgID, userID int64) (orgdomain.Role, bool, error) {
	return "", false, nil
}

type patchAuthMiddleware struct {
	user *userdomain.User
}

func (m patchAuthMiddleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := authdomain.WithUser(r.Context(), m.user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
func (m patchAuthMiddleware) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := authdomain.WithUser(r.Context(), m.user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

var (
	testOwner = &userdomain.User{ID: 1, Username: "testuser", Role: userdomain.RoleUser}
	testAdmin = &userdomain.User{ID: 99, Username: "admin", Role: userdomain.RoleAdmin}
	testRepo  = &repodomain.Repo{
		ID:         10,
		OwnerKind:  repodomain.OwnerKindUser,
		OwnerID:    1,
		OwnerName:  "testuser",
		Name:       "testrepo",
		Visibility: repodomain.VisibilityPublic,
	}
)

func newPatchTestHandler(t *testing.T, caller *userdomain.User, issues *fakePatchStore, repos *fakePatchRepoStore, resolver *fakePatchResolver) (*Handler, func()) {
	t.Helper()
	dir := t.TempDir()
	if repos == nil {
		repos = &fakePatchRepoStore{
			getByOwnerAndName: func(ownerKind repodomain.OwnerKind, ownerID int64, name string) (*repodomain.Repo, error) {
				if ownerKind == repodomain.OwnerKindUser && ownerID == 1 && name == "testrepo" {
					return testRepo, nil
				}
				return nil, repodomain.ErrRepoNotFound
			},
		}
	}
	if resolver == nil {
		resolver = &fakePatchResolver{
			resolveOwner: func(name string) (*orgdomain.Owner, error) {
				if name == "testuser" {
					return &orgdomain.Owner{Kind: orgdomain.OwnerKindUser, ID: 1, Name: name}, nil
				}
				return nil, orgdomain.ErrOwnerNotFound
			},
		}
	}
	cfg := &config.Config{Storage: config.StorageConfig{ReposPath: dir}}
	h := &Handler{
		issues:     issues,
		repos:      repos,
		storage:    repoinfra.NewStorage(&repoinfra.StorageDeps{Config: cfg}),
		resolver:   resolver,
		middleware: patchAuthMiddleware{user: caller},
	}
	return h, func() { os.RemoveAll(dir) }
}

func newPatchTestRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	return r
}

func doPatch(url string, body *bytes.Reader) (*http.Response, error) {
	req, err := http.NewRequest("PATCH", url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return http.DefaultClient.Do(req)
}

func patchJSON(v any) *bytes.Reader {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

// ---------------------------------------------------------------------------
// AC-2: reopen rejected with 422
// ---------------------------------------------------------------------------

func TestPatch_ReopenClosed_Returns422(t *testing.T) {
	closedIssue := &domain.Issue{
		ID:     42,
		RepoID: 10,
		Number: 1,
		State:  domain.StateClosed,
	}

	issues := &fakePatchStore{
		getByNumber: func(_ context.Context, repoID, number int64) (*domain.Issue, error) {
			return closedIssue, nil
		},
		listOpenDesc: func(_ context.Context, rootID int64) ([]*domain.OpenDescendant, error) {
			return nil, nil
		},
		updateState: func(_ context.Context, id int64, state domain.State, mergeSHA string) (*domain.Issue, error) {
			t.Error("UpdateState should not be called when reopen is rejected")
			return nil, nil
		},
	}

	h, cleanup := newPatchTestHandler(t, testOwner, issues, nil, nil)
	defer cleanup()
	srv := httptest.NewServer(newPatchTestRouter(h))
	defer srv.Close()

	resp, err := doPatch(srv.URL+"/api/repos/testuser/testrepo/issues/1", patchJSON(map[string]string{"state": "open"}))
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] != "This issue is closed and cannot be reopened." {
		t.Errorf("error = %q", body["error"])
	}
	if body["code"] != "closed_issue_immutable" {
		t.Errorf("code = %q", body["code"])
	}
}

// ---------------------------------------------------------------------------
// AC-3: admin also rejected
// ---------------------------------------------------------------------------

func TestPatch_ReopenClosed_AdminAlsoRejected(t *testing.T) {
	closedIssue := &domain.Issue{
		ID:     42,
		RepoID: 10,
		Number: 1,
		State:  domain.StateClosed,
	}

	issues := &fakePatchStore{
		getByNumber: func(_ context.Context, repoID, number int64) (*domain.Issue, error) {
			return closedIssue, nil
		},
		listOpenDesc: func(_ context.Context, rootID int64) ([]*domain.OpenDescendant, error) {
			return nil, nil
		},
		updateState: func(_ context.Context, id int64, state domain.State, mergeSHA string) (*domain.Issue, error) {
			t.Error("UpdateState should not be called for admin reopen attempt")
			return nil, nil
		},
	}

	h, cleanup := newPatchTestHandler(t, testAdmin, issues, nil, nil)
	defer cleanup()
	srv := httptest.NewServer(newPatchTestRouter(h))
	defer srv.Close()

	resp, err := doPatch(srv.URL+"/api/repos/testuser/testrepo/issues/1", patchJSON(map[string]string{"state": "open"}))
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (admin is NOT exempt)", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["code"] != "closed_issue_immutable" {
		t.Errorf("code = %q, want closed_issue_immutable", body["code"])
	}
}

// ---------------------------------------------------------------------------
// AC-4: forward close unaffected (regression)
// ---------------------------------------------------------------------------

func TestPatch_CloseOpen_NoSubIssues_Success(t *testing.T) {
	openIssue := &domain.Issue{
		ID:         42,
		RepoID:     10,
		Number:     1,
		State:      domain.StateOpen,
		AuthorID:   1,
		AuthorName: "testuser",
	}

	var stateUpdated domain.State
	issues := &fakePatchStore{
		getByNumber: func(_ context.Context, repoID, number int64) (*domain.Issue, error) {
			return openIssue, nil
		},
		listOpenDesc: func(_ context.Context, rootID int64) ([]*domain.OpenDescendant, error) {
			return nil, nil
		},
		updateState: func(_ context.Context, id int64, state domain.State, mergeSHA string) (*domain.Issue, error) {
			stateUpdated = state
			return &domain.Issue{
				ID: 42, RepoID: 10, Number: 1, State: state,
				AuthorID: 1, AuthorName: "testuser",
			}, nil
		},
		createEvent: func(_ context.Context, issueID int64, kind domain.EventKind, payload []byte, actorID int64, actorName string) (*domain.Event, error) {
			return &domain.Event{}, nil
		},
	}

	h, cleanup := newPatchTestHandler(t, testOwner, issues, nil, nil)
	defer cleanup()
	srv := httptest.NewServer(newPatchTestRouter(h))
	defer srv.Close()

	resp, err := doPatch(srv.URL+"/api/repos/testuser/testrepo/issues/1", patchJSON(map[string]string{"state": "closed"}))
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if stateUpdated != domain.StateClosed {
		t.Errorf("UpdateState called with state=%q, want %q", stateUpdated, domain.StateClosed)
	}
}

// ---------------------------------------------------------------------------
// Additional regression: merged → * still 409
// ---------------------------------------------------------------------------

func TestPatch_MergedToOpen_Returns409(t *testing.T) {
	mergedIssue := &domain.Issue{
		ID:     42,
		RepoID: 10,
		Number: 1,
		State:  domain.StateMerged,
	}

	issues := &fakePatchStore{
		getByNumber: func(_ context.Context, repoID, number int64) (*domain.Issue, error) {
			return mergedIssue, nil
		},
		listOpenDesc: func(_ context.Context, rootID int64) ([]*domain.OpenDescendant, error) {
			return nil, nil
		},
	}

	h, cleanup := newPatchTestHandler(t, testOwner, issues, nil, nil)
	defer cleanup()
	srv := httptest.NewServer(newPatchTestRouter(h))
	defer srv.Close()

	resp, err := doPatch(srv.URL+"/api/repos/testuser/testrepo/issues/1", patchJSON(map[string]string{"state": "open"}))
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// sub_issue.merged / sub_issue.closed trigger E2E tests
// ---------------------------------------------------------------------------

func TestPatch_CloseChild_FiresSubIssueClosedOnParent(t *testing.T) {
	parentIssue := &domain.Issue{
		ID:     99,
		RepoID: 10,
		Number: 2,
		State:  domain.StateOpen,
	}
	childIssue := &domain.Issue{
		ID:         42,
		RepoID:     10,
		Number:     1,
		State:      domain.StateOpen,
		ParentID:   99,
		AuthorID:   1,
		AuthorName: "testuser",
	}

	spawner := &fakeSpawner{}
	issues := &fakePatchStore{
		getByNumber: func(_ context.Context, repoID, number int64) (*domain.Issue, error) {
			return childIssue, nil
		},
		getByID: func(_ context.Context, id int64) (*domain.Issue, error) {
			return parentIssue, nil
		},
		listOpenDesc: func(_ context.Context, rootID int64) ([]*domain.OpenDescendant, error) {
			return nil, nil
		},
		updateState: func(_ context.Context, id int64, state domain.State, mergeSHA string) (*domain.Issue, error) {
			return &domain.Issue{
				ID: 42, RepoID: 10, Number: 1, State: state,
				ParentID: 99, AuthorID: 1, AuthorName: "testuser",
			}, nil
		},
		createEvent: func(_ context.Context, issueID int64, kind domain.EventKind, payload []byte, actorID int64, actorName string) (*domain.Event, error) {
			return &domain.Event{}, nil
		},
	}

	h, cleanup := newPatchTestHandler(t, testOwner, issues, nil, nil)
	defer cleanup()
	h.spawner = spawner
	srv := httptest.NewServer(newPatchTestRouter(h))
	defer srv.Close()

	resp, err := doPatch(srv.URL+"/api/repos/testuser/testrepo/issues/1", patchJSON(map[string]string{"state": "closed"}))
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	if spawner.lastTrigger == nil {
		t.Fatalf("expected spawner.OnTrigger to be called, but it wasn't")
	}
	if spawner.lastTrigger.Trigger != agentsconfig.TriggerSubIssueClosed {
		t.Errorf("trigger = %q, want %q", spawner.lastTrigger.Trigger, agentsconfig.TriggerSubIssueClosed)
	}
	if spawner.lastTrigger.CauseKind != agentsessiondomain.CauseKindSubIssueClosed {
		t.Errorf("causeKind = %q, want %q", spawner.lastTrigger.CauseKind, agentsessiondomain.CauseKindSubIssueClosed)
	}
	if spawner.lastTrigger.CauseID != "42" {
		t.Errorf("causeID = %q, want %q", spawner.lastTrigger.CauseID, "42")
	}
	if spawner.lastTrigger.IssueNumber != 2 {
		t.Errorf("issueNumber = %d, want 2 (parent)", spawner.lastTrigger.IssueNumber)
	}
	if spawner.lastTrigger.RepoID != 10 {
		t.Errorf("repoID = %d, want 10", spawner.lastTrigger.RepoID)
	}
}

func TestPatch_CloseChild_NoParent_DoesNotFire(t *testing.T) {
	rootIssue := &domain.Issue{
		ID:         42,
		RepoID:     10,
		Number:     1,
		State:      domain.StateOpen,
		ParentID:   0,
		AuthorID:   1,
		AuthorName: "testuser",
	}

	spawner := &fakeSpawner{}
	issues := &fakePatchStore{
		getByNumber: func(_ context.Context, repoID, number int64) (*domain.Issue, error) {
			return rootIssue, nil
		},
		listOpenDesc: func(_ context.Context, rootID int64) ([]*domain.OpenDescendant, error) {
			return nil, nil
		},
		updateState: func(_ context.Context, id int64, state domain.State, mergeSHA string) (*domain.Issue, error) {
			return &domain.Issue{
				ID: 42, RepoID: 10, Number: 1, State: state,
				ParentID: 0, AuthorID: 1, AuthorName: "testuser",
			}, nil
		},
		createEvent: func(_ context.Context, issueID int64, kind domain.EventKind, payload []byte, actorID int64, actorName string) (*domain.Event, error) {
			return &domain.Event{}, nil
		},
	}

	h, cleanup := newPatchTestHandler(t, testOwner, issues, nil, nil)
	defer cleanup()
	h.spawner = spawner
	srv := httptest.NewServer(newPatchTestRouter(h))
	defer srv.Close()

	resp, err := doPatch(srv.URL+"/api/repos/testuser/testrepo/issues/1", patchJSON(map[string]string{"state": "closed"}))
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	if spawner.lastTrigger != nil {
		t.Errorf("expected spawner NOT to fire for root issue, but got trigger=%q", spawner.lastTrigger.Trigger)
	}
}
