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
	byOwner map[int64][]store.Project
	nextID  int
	failGet bool // force a non-NotFound error path
}

func newFakeStore() *fakeStore { return &fakeStore{byOwner: map[int64][]store.Project{}} }

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
	h := NewHandlers(fs)
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
	h := NewHandlers(newFakeStore())
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
	h := NewHandlers(newFakeStore())
	form := validForm()
	form.Name = "   "
	rec := httptest.NewRecorder()
	h.Create(rec, request(t, http.MethodPost, "/api/owner/projects", form, store.Owner{ID: 1}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a blank name", rec.Code)
	}
}

func TestCreateUnsupportedEnvSourceIsRejected(t *testing.T) {
	h := NewHandlers(newFakeStore())
	form := validForm()
	form.Env = []FormEnv{{Name: "SECRET", Source: "owner"}} // deferred (ADR-0003)
	rec := httptest.NewRecorder()
	h.Create(rec, request(t, http.MethodPost, "/api/owner/projects", form, store.Owner{ID: 1}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an owner-sourced env", rec.Code)
	}
}

func TestCreateUnknownFieldIsRejected(t *testing.T) {
	h := NewHandlers(newFakeStore())
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
	h := NewHandlers(fs)
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
	h := NewHandlers(newFakeStore())
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
	h := NewHandlers(fs)
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
	h := NewHandlers(fs)
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

func TestGetStoreErrorIs500(t *testing.T) {
	fs := newFakeStore()
	fs.failGet = true
	h := NewHandlers(fs)
	rec := httptest.NewRecorder()
	r := request(t, http.MethodGet, "/api/owner/projects/x", nil, store.Owner{ID: 1})
	r.SetPathValue("id", "x")
	h.Get(rec, r)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on a store error", rec.Code)
	}
}
