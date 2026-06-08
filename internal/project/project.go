// Package project is the Owner-facing project configuration feature (#19): the HTTP
// handlers an Owner uses to create, list, and read their Projects, plus the mapping
// from the public Owner form to the internal run contract (runcontract.Manifest).
// The curated Form (form.go) exists so the API never leaks runcontract's tag-less Go
// field names and so this layer decides which manifest features an Owner may set.
// It persists through a narrow subset of *store.Store (the Store interface), so the
// handlers are testable without a database. Resolving a stored Project for the
// Runner (the play path) lives in a sibling file added with the build slice.
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
}

// Handlers serve the Owner project endpoints. Construct with NewHandlers and mount
// each method behind auth.RequireOwner (they read the Owner from context).
type Handlers struct {
	store Store
}

// NewHandlers wires the project handlers to a Store.
func NewHandlers(s Store) *Handlers { return &Handlers{store: s} }

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

// projectView is the JSON shape returned for a Project. Timestamps and the stored
// manifest are intentionally omitted for now — echoing the manifest back as an
// editable Form pairs with the edit UI in a later slice; here the Owner sees the
// Project's identity and publish state.
func projectView(p store.Project) map[string]any {
	return map[string]any{
		"id":        p.ID,
		"name":      p.Name,
		"published": p.Published,
	}
}
