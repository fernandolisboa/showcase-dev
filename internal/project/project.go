// Package project is the Owner-facing project configuration feature (#19): the HTTP
// handlers an Owner uses to create, list, read, and publish their Projects, plus the
// mapping from the public Owner form to the internal run contract (runcontract.Manifest).
// The curated Form (form.go) exists so the API never leaks runcontract's tag-less Go
// field names and so this layer decides which manifest features an Owner may set.
// It persists through a narrow subset of *store.Store (the Store interface), so the
// handlers are testable without a database. Resolving a stored Project for the
// Runner to play (the play path) is the Source in source.go.
package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/fernandolisboa/showcase-dev/internal/auth"
	"github.com/fernandolisboa/showcase-dev/internal/githubapp"
	"github.com/fernandolisboa/showcase-dev/internal/runcontract"
	"github.com/fernandolisboa/showcase-dev/internal/store"
)

// maxNameLen bounds the Owner-facing display name. Names are unique per Owner and
// short by intent (a Portfolio handle), so a generous cap is plenty.
const maxNameLen = 100

// maxBody caps the request body. A manifest with several services + env is larger
// than the 1 KiB username body but still small; 64 KiB leaves generous headroom
// while bounding memory per request.
const maxBody = 64 << 10

// Store is the persistence the project handlers need (a subset of *store.Store),
// kept as an interface so handlers are testable without a database — mirroring
// auth.SessionStore.
type Store interface {
	CreateProject(ctx context.Context, ownerID int64, name string, manifest []byte) (store.Project, error)
	GetOwnerProject(ctx context.Context, ownerID int64, id string) (store.Project, error)
	ListProjectsByOwner(ctx context.Context, ownerID int64) ([]store.Project, error)
	PublishProject(ctx context.Context, ownerID int64, id, commitSHA string) error
}

// ProjectBuilder builds every service of a Project's manifest from source at publish,
// returning the commit it built (the headline/UI commit) so the publisher can pin it.
// It is the seam to the image builder + repo clone; nil disables publishing (no
// GitHub App credentials), so the handler degrades to 501 without a database/build of
// untrusted Owner code in dev. An error means the build failed — the Project is not
// published. ownerLogin scopes which installation the clone token is minted for.
type ProjectBuilder interface {
	BuildProject(ctx context.Context, ownerLogin, projectID string, manifest runcontract.Manifest) (commitSHA string, err error)
}

// Handlers serve the Owner project endpoints. Construct with NewHandlers and mount
// each method behind auth.RequireOwner (they read the Owner from context).
type Handlers struct {
	store   Store
	builder ProjectBuilder // nil => publishing is not configured (501)
}

// NewHandlers wires the project handlers to a Store and (optionally) a ProjectBuilder.
// A nil builder leaves create/list/get working but makes publish report 501.
func NewHandlers(s Store, b ProjectBuilder) *Handlers { return &Handlers{store: s, builder: b} }

// Create persists a new Project for the signed-in Owner. Body: a Form (JSON).
// It maps the form to a runcontract.Manifest, validates it (reusing the contract's
// own Validate), and stores the canonical encoding. 400 on a bad body or an invalid
// manifest, 409 if the Owner already has a Project with that name, 201 on success.
// Must be mounted behind RequireOwner.
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	owner, ok := auth.OwnerFrom(r.Context())
	if !ok {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	var form Form
	if err := dec.Decode(&form); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(form.Name)
	if name == "" || utf8.RuneCountInString(name) > maxNameLen {
		http.Error(w, fmt.Sprintf("name is required and must be at most %d characters", maxNameLen), http.StatusBadRequest)
		return
	}

	manifest, err := form.toManifest()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := manifest.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	p, err := h.store.CreateProject(r.Context(), owner.ID, name, encoded)
	switch {
	case errors.Is(err, store.ErrProjectNameTaken):
		http.Error(w, "a project with that name already exists", http.StatusConflict)
		return
	case err != nil:
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(projectView(p))
}

// List returns the signed-in Owner's Projects. Must be mounted behind RequireOwner.
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	owner, ok := auth.OwnerFrom(r.Context())
	if !ok {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	ps, err := h.store.ListProjectsByOwner(r.Context(), owner.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	views := make([]map[string]any, 0, len(ps))
	for _, p := range ps {
		views = append(views, projectView(p))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"projects": views})
}

// Get returns one of the signed-in Owner's Projects by id, 404 if it is not theirs
// (or does not exist). Must be mounted behind RequireOwner with an {id} path value.
func (h *Handlers) Get(w http.ResponseWriter, r *http.Request) {
	owner, ok := auth.OwnerFrom(r.Context())
	if !ok {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	p, err := h.store.GetOwnerProject(r.Context(), owner.ID, r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrProjectNotFound):
		http.Error(w, "project not found", http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(projectView(p))
}

// Publish builds the signed-in Owner's Project from source and, on success, marks it
// published at the built commit (ADR-0003: build at publish, run from cache). It is
// synchronous — the Owner waits for the build — and must be mounted behind
// RequireOwner with an {id} path value. 404 if the Project is not theirs, 501 if
// publishing is not configured (no GitHub App credentials), 422 if the repo is not
// theirs / not a single repo / fails to build, 200 with the published view on success.
func (h *Handlers) Publish(w http.ResponseWriter, r *http.Request) {
	owner, ok := auth.OwnerFrom(r.Context())
	if !ok {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	if h.builder == nil {
		http.Error(w, "publishing is not configured", http.StatusNotImplemented)
		return
	}
	p, err := h.store.GetOwnerProject(r.Context(), owner.ID, r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrProjectNotFound):
		http.Error(w, "project not found", http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var m runcontract.Manifest
	if err := json.Unmarshal(p.Manifest, &m); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Security: an Owner may only publish their OWN repositories, and (MVP) every
	// service must come from one repository so a single commit pins the whole Project.
	if err := checkOwnerRepos(owner.GitHubLogin, m); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}

	commitSHA, err := h.builder.BuildProject(r.Context(), owner.GitHubLogin, p.ID, m)
	if err != nil {
		// A repo the App can't reach is an actionable, terse message; any other
		// failure returns a bounded build log (the build runs the Owner's own code, so
		// its output carries no platform secret — the clone layer already redacts the
		// token from its errors).
		if errors.Is(err, githubapp.ErrNoInstallation) {
			http.Error(w, "the Showcase GitHub App is not installed on that repository", http.StatusUnprocessableEntity)
			return
		}
		http.Error(w, "build failed:\n"+lastBytes(err.Error(), 4<<10), http.StatusUnprocessableEntity)
		return
	}

	if err := h.store.PublishProject(r.Context(), owner.ID, p.ID, commitSHA); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	p.Published = true
	p.CommitSHA = commitSHA
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(projectView(p))
}

// checkOwnerRepos enforces that every service's repo is the signed-in Owner's own
// (parsed owner == their GitHub login) and that all services share a single repo
// (MVP: one commit pins the whole Project). Both are publish preconditions surfaced
// to the Owner as 422. Cross-owner public repos are rejected here, since the
// installation-token boundary only stops PRIVATE repos.
func checkOwnerRepos(ownerLogin string, m runcontract.Manifest) error {
	var first string
	for _, svc := range m.Services {
		owner, repo, err := githubapp.ParseRepo(svc.Repo)
		if err != nil {
			return fmt.Errorf("service %q: %v", svc.Name, err)
		}
		if !strings.EqualFold(owner, ownerLogin) {
			return fmt.Errorf("service %q: %s/%s is not your repository — you can only publish repos you own", svc.Name, owner, repo)
		}
		key := strings.ToLower(owner + "/" + repo)
		switch {
		case first == "":
			first = key
		case key != first:
			return fmt.Errorf("all services must come from one repository for now (found %s and %s)", first, key)
		}
	}
	return nil
}

// lastBytes returns the last max bytes of s (so a long build log stays bounded over
// HTTP), prefixed with an ellipsis when truncated.
func lastBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return "…" + s[len(s)-max:]
}

// projectView is the JSON shape returned for a Project. Timestamps and the stored
// manifest are intentionally omitted for now — echoing the manifest back as an
// editable Form pairs with the edit UI in a later slice; here the Owner sees the
// Project's identity, publish state, and (once published) the built commit.
func projectView(p store.Project) map[string]any {
	return map[string]any{
		"id":        p.ID,
		"name":      p.Name,
		"published": p.Published,
		"commitSha": p.CommitSHA,
	}
}
