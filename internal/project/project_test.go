package project

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fernandolisboa/showcase-dev/internal/auth"
	"github.com/fernandolisboa/showcase-dev/internal/runcontract"
	"github.com/fernandolisboa/showcase-dev/internal/store"
)

// fakeStore is an in-memory Store for handler tests — no database. It keys Projects
// by owner so it can exercise the per-owner uniqueness and ownership scoping the
// real store enforces in SQL.
type fakeStore struct {
	byOwner     map[int64][]store.Project
	owners      map[string]store.Owner // by username, for the portfolio lookup
	nextID      int
	failGet     bool // force a non-NotFound error path on GetOwnerProject
	failPublish bool // force PublishProject to fail (after a successful build)
}

func newFakeStore() *fakeStore {
	return &fakeStore{byOwner: map[int64][]store.Project{}, owners: map[string]store.Owner{}}
}

func (f *fakeStore) OwnerByUsername(_ context.Context, username string) (store.Owner, error) {
	o, ok := f.owners[username]
	if !ok {
		return store.Owner{}, store.ErrNotFound
	}
	return o, nil
}

func (f *fakeStore) ListPublishedProjectsByUsername(_ context.Context, username string) ([]store.Project, error) {
	o, ok := f.owners[username]
	if !ok {
		return nil, nil
	}
	var pub []store.Project
	for _, p := range f.byOwner[o.ID] {
		if p.Published {
			pub = append(pub, p)
		}
	}
	return pub, nil
}

func (f *fakeStore) CreateProject(_ context.Context, ownerID int64, name string, manifest []byte) (store.Project, error) {
	for _, p := range f.byOwner[ownerID] {
		if p.Name == name {
			return store.Project{}, store.ErrProjectNameTaken
		}
	}
	f.nextID++
	p := store.Project{
		ID:       fmt.Sprintf("test-project-%d", f.nextID),
		OwnerID:  ownerID,
		Name:     name,
		Manifest: manifest,
	}
	f.byOwner[ownerID] = append(f.byOwner[ownerID], p)
	return p, nil
}

func (f *fakeStore) UpdateProject(_ context.Context, ownerID int64, id, name string, manifest []byte) (store.Project, error) {
	// Per-owner unique name: renaming onto another of this Owner's Projects collides.
	for _, p := range f.byOwner[ownerID] {
		if p.Name == name && p.ID != id {
			return store.Project{}, store.ErrProjectNameTaken
		}
	}
	for i := range f.byOwner[ownerID] {
		if f.byOwner[ownerID][i].ID == id {
			// Editing the manifest returns the Project to draft (mirrors the store's
			// conditional reset); a pure rename keeps it published.
			if !bytes.Equal(f.byOwner[ownerID][i].Manifest, manifest) {
				f.byOwner[ownerID][i].Published = false
				f.byOwner[ownerID][i].CommitSHA = ""
			}
			f.byOwner[ownerID][i].Name = name
			f.byOwner[ownerID][i].Manifest = manifest
			return f.byOwner[ownerID][i], nil
		}
	}
	return store.Project{}, store.ErrProjectNotFound
}

func (f *fakeStore) GetOwnerProject(_ context.Context, ownerID int64, id string) (store.Project, error) {
	if f.failGet {
		return store.Project{}, errors.New("boom")
	}
	for _, p := range f.byOwner[ownerID] {
		if p.ID == id {
			return p, nil
		}
	}
	return store.Project{}, store.ErrProjectNotFound
}

func (f *fakeStore) ListProjectsByOwner(_ context.Context, ownerID int64) ([]store.Project, error) {
	return f.byOwner[ownerID], nil
}

func (f *fakeStore) StartBuild(_ context.Context, ownerID int64, id string) (store.Project, error) {
	for i := range f.byOwner[ownerID] {
		if f.byOwner[ownerID][i].ID == id {
			if f.byOwner[ownerID][i].BuildState == "building" {
				return store.Project{}, store.ErrBuildInProgress
			}
			f.byOwner[ownerID][i].BuildState = "building"
			f.byOwner[ownerID][i].BuildError = ""
			return f.byOwner[ownerID][i], nil
		}
	}
	return store.Project{}, store.ErrProjectNotFound
}

func (f *fakeStore) MarkBuildFailed(_ context.Context, ownerID int64, id, buildError string) error {
	for i := range f.byOwner[ownerID] {
		if f.byOwner[ownerID][i].ID == id {
			f.byOwner[ownerID][i].BuildState = "failed"
			f.byOwner[ownerID][i].BuildError = buildError
			return nil // a failed build never un-publishes
		}
	}
	return store.ErrProjectNotFound
}

func (f *fakeStore) PublishProject(_ context.Context, ownerID int64, id, commitSHA string) error {
	if f.failPublish {
		return errors.New("boom")
	}
	for i := range f.byOwner[ownerID] {
		if f.byOwner[ownerID][i].ID == id {
			f.byOwner[ownerID][i].Published = true
			f.byOwner[ownerID][i].CommitSHA = commitSHA
			f.byOwner[ownerID][i].BuildState = "published"
			f.byOwner[ownerID][i].BuildError = ""
			return nil
		}
	}
	return store.ErrProjectNotFound
}

// validForm is a minimal manifest that passes runcontract.Validate: one ui service.
func validForm() Form {
	return Form{
		Name: "blog",
		Services: []FormService{
			{Name: "web", Repo: "github.com/me/blog", Dockerfile: "Dockerfile", Port: 8080, Role: "ui"},
		},
	}
}

// request builds a request carrying owner in context, the way RequireOwner does.
func request(t *testing.T, method, target string, body any, owner store.Owner) *http.Request {
	t.Helper()
	var r *http.Request
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		r = httptest.NewRequest(method, target, bytes.NewReader(b))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	return r.WithContext(auth.ContextWithOwner(r.Context(), owner))
}

func TestCreateValid(t *testing.T) {
	fs := newFakeStore()
	h := NewHandlers(fs, nil)
	owner := store.Owner{ID: 1}

	rec := httptest.NewRecorder()
	h.Create(rec, request(t, http.MethodPost, "/api/owner/projects", validForm(), owner))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body)
	}
	var got map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["name"] != "blog" || got["published"] != false || got["id"] == "" {
		t.Errorf("unexpected create response: %v", got)
	}

	// The stored manifest must round-trip back into a runcontract.Manifest.
	stored := fs.byOwner[1][0].Manifest
	var m runcontract.Manifest
	if err := json.Unmarshal(stored, &m); err != nil {
		t.Fatalf("stored manifest is not a valid run contract: %v", err)
	}
	if len(m.Services) != 1 || m.Services[0].Name != "web" || m.Services[0].Role != runcontract.RoleUI {
		t.Errorf("stored manifest lost data: %+v", m)
	}
}

func TestCreateInvalidManifestIsRejected(t *testing.T) {
	h := NewHandlers(newFakeStore(), nil)
	owner := store.Owner{ID: 1}

	// No ui service → Validate fails ("exactly one ui service is required").
	form := Form{
		Name:     "api-only",
		Services: []FormService{{Name: "api", Repo: "r", Dockerfile: "Dockerfile", Port: 80, Role: "api", PathPrefix: "/api"}},
	}
	rec := httptest.NewRecorder()
	h.Create(rec, request(t, http.MethodPost, "/api/owner/projects", form, owner))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an invalid manifest", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ui service") {
		t.Errorf("expected the manifest validation message, got %q", rec.Body.String())
	}
}

func TestCreateMissingNameIsRejected(t *testing.T) {
	h := NewHandlers(newFakeStore(), nil)
	form := validForm()
	form.Name = "   "
	rec := httptest.NewRecorder()
	h.Create(rec, request(t, http.MethodPost, "/api/owner/projects", form, store.Owner{ID: 1}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a blank name", rec.Code)
	}
}

func TestCreateUnsupportedEnvSourceIsRejected(t *testing.T) {
	h := NewHandlers(newFakeStore(), nil)
	form := validForm()
	form.Env = []FormEnv{{Name: "SECRET", Source: "owner"}} // deferred (ADR-0003)
	rec := httptest.NewRecorder()
	h.Create(rec, request(t, http.MethodPost, "/api/owner/projects", form, store.Owner{ID: 1}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an owner-sourced env", rec.Code)
	}
}

func TestCreateUnknownFieldIsRejected(t *testing.T) {
	h := NewHandlers(newFakeStore(), nil)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/owner/projects", strings.NewReader(`{"name":"x","nope":1}`))
	r = r.WithContext(auth.ContextWithOwner(r.Context(), store.Owner{ID: 1}))
	h.Create(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown field (DisallowUnknownFields)", rec.Code)
	}
}

func TestCreateDuplicateNameIsConflict(t *testing.T) {
	fs := newFakeStore()
	h := NewHandlers(fs, nil)
	owner := store.Owner{ID: 1}

	first := httptest.NewRecorder()
	h.Create(first, request(t, http.MethodPost, "/api/owner/projects", validForm(), owner))
	if first.Code != http.StatusCreated {
		t.Fatalf("first create = %d, want 201", first.Code)
	}
	second := httptest.NewRecorder()
	h.Create(second, request(t, http.MethodPost, "/api/owner/projects", validForm(), owner))
	if second.Code != http.StatusConflict {
		t.Fatalf("duplicate name = %d, want 409", second.Code)
	}
}

func TestCreateRequiresOwner(t *testing.T) {
	h := NewHandlers(newFakeStore(), nil)
	rec := httptest.NewRecorder()
	// No owner in context (as if RequireOwner were bypassed).
	r := httptest.NewRequest(http.MethodPost, "/api/owner/projects", strings.NewReader(`{}`))
	h.Create(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without an Owner in context", rec.Code)
	}
}

func TestListIsOwnerScoped(t *testing.T) {
	fs := newFakeStore()
	h := NewHandlers(fs, nil)
	alice, bob := store.Owner{ID: 1}, store.Owner{ID: 2}

	h.Create(httptest.NewRecorder(), request(t, http.MethodPost, "/api/owner/projects", validForm(), alice))
	bobForm := validForm()
	bobForm.Name = "bobs-thing"
	h.Create(httptest.NewRecorder(), request(t, http.MethodPost, "/api/owner/projects", bobForm, bob))

	rec := httptest.NewRecorder()
	h.List(rec, request(t, http.MethodGet, "/api/owner/projects", nil, alice))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		Projects []map[string]any `json:"projects"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Projects) != 1 || got.Projects[0]["name"] != "blog" {
		t.Errorf("List returned another Owner's projects or wrong set: %v", got.Projects)
	}
}

func TestGetFoundAndCrossOwner(t *testing.T) {
	fs := newFakeStore()
	h := NewHandlers(fs, nil)
	alice, bob := store.Owner{ID: 1}, store.Owner{ID: 2}

	create := httptest.NewRecorder()
	h.Create(create, request(t, http.MethodPost, "/api/owner/projects", validForm(), alice))
	var created map[string]any
	_ = json.NewDecoder(create.Body).Decode(&created)
	id := created["id"].(string)

	// Alice reads her own → 200.
	ok := httptest.NewRecorder()
	getReq := request(t, http.MethodGet, "/api/owner/projects/"+id, nil, alice)
	getReq.SetPathValue("id", id)
	h.Get(ok, getReq)
	if ok.Code != http.StatusOK {
		t.Fatalf("owner Get = %d, want 200", ok.Code)
	}

	// Bob reads Alice's id → 404 (no IDOR, no existence oracle).
	cross := httptest.NewRecorder()
	crossReq := request(t, http.MethodGet, "/api/owner/projects/"+id, nil, bob)
	crossReq.SetPathValue("id", id)
	h.Get(cross, crossReq)
	if cross.Code != http.StatusNotFound {
		t.Fatalf("cross-owner Get = %d, want 404", cross.Code)
	}
}

func TestGetEchoesManifestAsForm(t *testing.T) {
	fs := newFakeStore()
	h := NewHandlers(fs, nil)
	owner := store.Owner{ID: 1}

	form := validForm()
	form.DB = &FormDB{Engine: "postgres", Version: "17"}
	form.Env = []FormEnv{{Name: "LOG_LEVEL", Source: "static", Value: "info"}}
	id := createProject(t, h, form, owner)

	rec := httptest.NewRecorder()
	r := request(t, http.MethodGet, "/api/owner/projects/"+id, nil, owner)
	r.SetPathValue("id", id)
	h.Get(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("Get = %d, body=%s", rec.Code, rec.Body)
	}

	var got struct {
		Name     string `json:"name"`
		Manifest Form   `json:"manifest"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The echoed Form carries the Project name (a column, not a manifest field) so the
	// edit UI can PUT it straight back.
	if got.Manifest.Name != "blog" {
		t.Errorf("echoed form Name = %q, want the project name %q", got.Manifest.Name, "blog")
	}
	if len(got.Manifest.Services) != 1 || got.Manifest.Services[0].Name != "web" || got.Manifest.Services[0].Role != "ui" {
		t.Errorf("echoed form lost the service: %+v", got.Manifest.Services)
	}
	if got.Manifest.DB == nil || got.Manifest.DB.Engine != "postgres" {
		t.Errorf("echoed form lost the db: %+v", got.Manifest.DB)
	}
	if len(got.Manifest.Env) != 1 || got.Manifest.Env[0].Name != "LOG_LEVEL" || got.Manifest.Env[0].Value != "info" {
		t.Errorf("echoed form lost env: %+v", got.Manifest.Env)
	}
	// The echoed Form must itself be a valid create/update body — it round-trips.
	if _, err := got.Manifest.toManifest(); err != nil {
		t.Errorf("echoed form does not map back to a manifest: %v", err)
	}
}

// fakeBuilder is a ProjectBuilder that records whether it ran and returns a fixed
// commit or error.
type fakeBuilder struct {
	commit string
	err    error
	called bool
}

func (f *fakeBuilder) BuildProject(_ context.Context, _, _ string, _ runcontract.Manifest) (string, error) {
	f.called = true
	return f.commit, f.err
}

// createProject is a helper that creates a project via the handler and returns its id.
func createProject(t *testing.T, h *Handlers, form Form, owner store.Owner) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Create(rec, request(t, http.MethodPost, "/api/owner/projects", form, owner))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, body=%s", rec.Code, rec.Body)
	}
	var created map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	return created["id"].(string)
}

func publish(t *testing.T, h *Handlers, id string, owner store.Owner) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r := request(t, http.MethodPost, "/api/owner/projects/"+id+"/publish", nil, owner)
	r.SetPathValue("id", id)
	h.Publish(rec, r)
	return rec
}

func TestPublishSuccess(t *testing.T) {
	fs := newFakeStore()
	fb := &fakeBuilder{commit: "abc123"}
	h := NewHandlers(fs, fb)
	owner := store.Owner{ID: 1, GitHubLogin: "me"} // validForm's repo is github.com/me/blog
	id := createProject(t, h, validForm(), owner)

	rec := publish(t, h, id, owner)
	if rec.Code != http.StatusOK {
		t.Fatalf("publish = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var got map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&got)
	if got["published"] != true || got["commitSha"] != "abc123" || got["buildState"] != "published" {
		t.Errorf("unexpected publish response: %v", got)
	}
	if stored := fs.byOwner[1][0]; !stored.Published || stored.CommitSHA != "abc123" || stored.BuildState != "published" {
		t.Errorf("project not persisted as published: %+v", stored)
	}
}

func TestPublishBuildFailureDoesNotPublish(t *testing.T) {
	fs := newFakeStore()
	h := NewHandlers(fs, &fakeBuilder{err: errors.New("RUN npm ci failed: exit 1")})
	owner := store.Owner{ID: 1, GitHubLogin: "me"}
	id := createProject(t, h, validForm(), owner)

	rec := publish(t, h, id, owner)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("build-fail publish = %d, want 422", rec.Code)
	}
	if stored := fs.byOwner[1][0]; stored.Published {
		t.Error("a project whose build failed must not be published")
	} else if stored.BuildState != "failed" || stored.BuildError == "" {
		t.Errorf("a failed build must record build_state='failed' + an error, got %+v", stored)
	}
}

func TestPublishWhileBuildingIs409(t *testing.T) {
	fs := newFakeStore()
	fb := &fakeBuilder{commit: "abc123"}
	h := NewHandlers(fs, fb)
	owner := store.Owner{ID: 1, GitHubLogin: "me"}
	id := createProject(t, h, validForm(), owner)
	// Simulate a build already in flight (as a concurrent publish would have left it).
	fs.byOwner[1][0].BuildState = "building"

	rec := publish(t, h, id, owner)
	if rec.Code != http.StatusConflict {
		t.Fatalf("publish while building = %d, want 409", rec.Code)
	}
	if fb.called {
		t.Error("must not start a second build while one is already in flight")
	}
}

func TestPublishFailedRebuildKeepsPublished(t *testing.T) {
	fs := newFakeStore()
	fb := &fakeBuilder{commit: "abc123"}
	h := NewHandlers(fs, fb)
	owner := store.Owner{ID: 1, GitHubLogin: "me"}
	id := createProject(t, h, validForm(), owner)
	if rec := publish(t, h, id, owner); rec.Code != http.StatusOK {
		t.Fatalf("initial publish = %d, body=%s", rec.Code, rec.Body)
	}

	// A failing RE-build must not un-publish the live version (a failed build never
	// un-publishes — the old build keeps playing while build_state reports the failure).
	fb.err = errors.New("RUN build failed")
	rec := publish(t, h, id, owner)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("failed re-build = %d, want 422", rec.Code)
	}
	if stored := fs.byOwner[1][0]; !stored.Published || stored.CommitSHA != "abc123" || stored.BuildState != "failed" {
		t.Errorf("a failed re-build must keep the prior publish live: %+v", stored)
	}
}

func TestPublishPersistFailureSettlesState(t *testing.T) {
	fs := newFakeStore()
	fs.failPublish = true // the build succeeds but recording the publish fails
	h := NewHandlers(fs, &fakeBuilder{commit: "abc123"})
	owner := store.Owner{ID: 1, GitHubLogin: "me"}
	id := createProject(t, h, validForm(), owner)

	rec := publish(t, h, id, owner)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("publish with a failing persist = %d, want 500", rec.Code)
	}
	// The row must settle to 'failed' (retryable), not be stranded in 'building' (which
	// would 409 every retry).
	if stored := fs.byOwner[1][0]; stored.BuildState != "failed" || stored.BuildError == "" {
		t.Errorf("a persist failure must settle to 'failed', not strand in 'building': %+v", stored)
	}
}

func TestPublishWithoutBuilderIs501(t *testing.T) {
	fs := newFakeStore()
	h := NewHandlers(fs, nil) // no builder configured
	owner := store.Owner{ID: 1, GitHubLogin: "me"}
	id := createProject(t, h, validForm(), owner)
	if rec := publish(t, h, id, owner); rec.Code != http.StatusNotImplemented {
		t.Fatalf("publish without a builder = %d, want 501", rec.Code)
	}
}

func TestPublishRejectsCrossOwnerRepoWithoutBuilding(t *testing.T) {
	fs := newFakeStore()
	fb := &fakeBuilder{commit: "x"}
	h := NewHandlers(fs, fb)
	owner := store.Owner{ID: 1, GitHubLogin: "me"}
	form := validForm()
	form.Services[0].Repo = "github.com/someoneelse/proj" // not the owner's repo
	id := createProject(t, h, form, owner)

	rec := publish(t, h, id, owner)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("cross-owner repo publish = %d, want 422", rec.Code)
	}
	if fb.called {
		t.Error("must reject a non-owner repo BEFORE attempting a build")
	}
}

func TestPublishNotOwnerIs404(t *testing.T) {
	fs := newFakeStore()
	h := NewHandlers(fs, &fakeBuilder{commit: "x"})
	alice := store.Owner{ID: 1, GitHubLogin: "me"}
	id := createProject(t, h, validForm(), alice)
	// Bob tries to publish Alice's project.
	bob := store.Owner{ID: 2, GitHubLogin: "bob"}
	if rec := publish(t, h, id, bob); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-owner publish = %d, want 404", rec.Code)
	}
}

func TestPublishRequiresOwner(t *testing.T) {
	h := NewHandlers(newFakeStore(), &fakeBuilder{})
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/owner/projects/x/publish", nil)
	r.SetPathValue("id", "x")
	h.Publish(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("publish without an Owner = %d, want 401", rec.Code)
	}
}

func update(t *testing.T, h *Handlers, id string, body any, owner store.Owner) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r := request(t, http.MethodPut, "/api/owner/projects/"+id, body, owner)
	r.SetPathValue("id", id)
	h.Update(rec, r)
	return rec
}

func TestUpdateReplacesManifest(t *testing.T) {
	fs := newFakeStore()
	h := NewHandlers(fs, nil)
	owner := store.Owner{ID: 1}
	id := createProject(t, h, validForm(), owner)

	edited := validForm()
	edited.Services[0].Port = 3000 // change the manifest
	rec := update(t, h, id, edited, owner)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d, body=%s", rec.Code, rec.Body)
	}
	var got struct {
		Manifest Form `json:"manifest"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&got)
	if len(got.Manifest.Services) != 1 || got.Manifest.Services[0].Port != 3000 {
		t.Errorf("update did not replace the manifest: %+v", got.Manifest.Services)
	}
}

func TestUpdateUnpublishesWhenManifestChanges(t *testing.T) {
	fs := newFakeStore()
	h := NewHandlers(fs, &fakeBuilder{commit: "abc123"})
	owner := store.Owner{ID: 1, GitHubLogin: "me"} // validForm's repo is github.com/me/blog
	id := createProject(t, h, validForm(), owner)
	if rec := publish(t, h, id, owner); rec.Code != http.StatusOK {
		t.Fatalf("publish setup = %d, body=%s", rec.Code, rec.Body)
	}

	edited := validForm()
	edited.Services[0].Port = 3000 // a real manifest change
	rec := update(t, h, id, edited, owner)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d, body=%s", rec.Code, rec.Body)
	}
	var got map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&got)
	if got["published"] != false || got["commitSha"] != "" {
		t.Errorf("editing the manifest must return the Project to draft, got %v", got)
	}
	if stored := fs.byOwner[1][0]; stored.Published || stored.CommitSHA != "" {
		t.Errorf("draft reset not persisted: %+v", stored)
	}
}

func TestUpdatePureRenameKeepsPublished(t *testing.T) {
	fs := newFakeStore()
	h := NewHandlers(fs, &fakeBuilder{commit: "abc123"})
	owner := store.Owner{ID: 1, GitHubLogin: "me"}
	id := createProject(t, h, validForm(), owner)
	if rec := publish(t, h, id, owner); rec.Code != http.StatusOK {
		t.Fatalf("publish setup = %d, body=%s", rec.Code, rec.Body)
	}

	renamed := validForm() // identical services → manifest unchanged
	renamed.Name = "blog-renamed"
	rec := update(t, h, id, renamed, owner)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d, body=%s", rec.Code, rec.Body)
	}
	var got map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&got)
	if got["name"] != "blog-renamed" || got["published"] != true || got["commitSha"] != "abc123" {
		t.Errorf("a pure rename must stay published at the same commit, got %v", got)
	}
}

func TestUpdateCrossOwnerIs404(t *testing.T) {
	fs := newFakeStore()
	h := NewHandlers(fs, nil)
	alice, bob := store.Owner{ID: 1}, store.Owner{ID: 2}
	id := createProject(t, h, validForm(), alice)

	if rec := update(t, h, id, validForm(), bob); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-owner update = %d, want 404 (no IDOR)", rec.Code)
	}
}

func TestUpdateInvalidManifestIsRejected(t *testing.T) {
	fs := newFakeStore()
	h := NewHandlers(fs, nil)
	owner := store.Owner{ID: 1}
	id := createProject(t, h, validForm(), owner)

	bad := validForm()
	bad.Services[0].Role = "api" // no ui service left → Validate fails
	bad.Services[0].PathPrefix = "/api"
	rec := update(t, h, id, bad, owner)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("update with an invalid manifest = %d, want 400", rec.Code)
	}
}

func TestUpdateNameCollisionIsConflict(t *testing.T) {
	fs := newFakeStore()
	h := NewHandlers(fs, nil)
	owner := store.Owner{ID: 1}
	createProject(t, h, validForm(), owner) // "blog"
	other := validForm()
	other.Name = "other"
	otherID := createProject(t, h, other, owner)

	rename := validForm()
	rename.Name = "blog" // collides with the first project
	if rec := update(t, h, otherID, rename, owner); rec.Code != http.StatusConflict {
		t.Fatalf("rename onto an existing name = %d, want 409", rec.Code)
	}
}

func TestUpdateRequiresOwner(t *testing.T) {
	h := NewHandlers(newFakeStore(), nil)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/owner/projects/x", strings.NewReader(`{}`))
	r.SetPathValue("id", "x")
	h.Update(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("update without an Owner = %d, want 401", rec.Code)
	}
}

func portfolio(t *testing.T, h *Handlers, username string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/portfolio/"+username, nil)
	r.SetPathValue("username", username)
	h.Portfolio(rec, r)
	return rec
}

func TestPortfolioListsOnlyPublished(t *testing.T) {
	fs := newFakeStore()
	h := NewHandlers(fs, &fakeBuilder{commit: "c"})
	// GitHubLogin must match validForm's repo owner ("me") so publish passes.
	alice := store.Owner{ID: 1, Username: "alice", GitHubLogin: "me"}
	fs.owners["alice"] = alice

	published := createProject(t, h, validForm(), alice)
	draftForm := validForm()
	draftForm.Name = "draft"
	createProject(t, h, draftForm, alice) // left unpublished
	if rec := publish(t, h, published, alice); rec.Code != http.StatusOK {
		t.Fatalf("publish setup = %d, body=%s", rec.Code, rec.Body)
	}

	rec := portfolio(t, h, "Alice") // case-insensitive
	if rec.Code != http.StatusOK {
		t.Fatalf("portfolio = %d, body=%s", rec.Code, rec.Body)
	}
	var got struct {
		Owner    map[string]any   `json:"owner"`
		Projects []map[string]any `json:"projects"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&got)
	if got.Owner["username"] != "alice" {
		t.Errorf("owner = %v", got.Owner)
	}
	if len(got.Projects) != 1 || got.Projects[0]["name"] != "blog" {
		t.Errorf("portfolio must list only the published project, got %v", got.Projects)
	}
}

func TestPortfolioUnknownUserIs404(t *testing.T) {
	h := NewHandlers(newFakeStore(), nil)
	if rec := portfolio(t, h, "nobody"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown user = %d, want 404", rec.Code)
	}
}

func TestPortfolioEmptyWhenNothingPublished(t *testing.T) {
	fs := newFakeStore()
	h := NewHandlers(fs, &fakeBuilder{})
	alice := store.Owner{ID: 1, Username: "alice", GitHubLogin: "me"}
	fs.owners["alice"] = alice
	createProject(t, h, validForm(), alice) // draft only

	rec := portfolio(t, h, "alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d, want 200 with an empty list", rec.Code)
	}
	var got struct {
		Projects []map[string]any `json:"projects"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&got)
	if len(got.Projects) != 0 {
		t.Errorf("a draft must not appear in the public portfolio: %v", got.Projects)
	}
}

func TestCheckOwnerRepos(t *testing.T) {
	man := func(repos ...string) runcontract.Manifest {
		var m runcontract.Manifest
		for i, r := range repos {
			role := runcontract.RoleAPI
			pfx := "/api"
			if i == 0 {
				role, pfx = runcontract.RoleUI, ""
			}
			m.Services = append(m.Services, runcontract.Service{Name: fmt.Sprintf("s%d", i), Repo: r, Role: role, PathPrefix: pfx})
		}
		return m
	}
	if err := checkOwnerRepos("me", man("github.com/me/app")); err != nil {
		t.Errorf("own single repo should pass: %v", err)
	}
	if err := checkOwnerRepos("Me", man("github.com/me/app", "me/app")); err != nil {
		t.Errorf("own repo, two services one repo (case-insensitive) should pass: %v", err)
	}
	if err := checkOwnerRepos("me", man("github.com/someoneelse/app")); err == nil {
		t.Error("a repo the Owner does not own must be rejected")
	}
	if err := checkOwnerRepos("me", man("github.com/me/app", "github.com/me/other")); err == nil {
		t.Error("multiple repos must be rejected (single-repo MVP)")
	}
	if err := checkOwnerRepos("me", man("not a repo")); err == nil {
		t.Error("an unparseable repo must be rejected")
	}
}

func TestGetStoreErrorIs500(t *testing.T) {
	fs := newFakeStore()
	fs.failGet = true
	h := NewHandlers(fs, nil)
	rec := httptest.NewRecorder()
	r := request(t, http.MethodGet, "/api/owner/projects/x", nil, store.Owner{ID: 1})
	r.SetPathValue("id", "x")
	h.Get(rec, r)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on a store error", rec.Code)
	}
}
