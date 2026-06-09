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
	"time"
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
	UpdateProject(ctx context.Context, ownerID int64, id, name string, manifest []byte) (store.Project, error)
	GetOwnerProject(ctx context.Context, ownerID int64, id string) (store.Project, error)
	ListProjectsByOwner(ctx context.Context, ownerID int64) ([]store.Project, error)
	StartBuild(ctx context.Context, ownerID int64, id string) (store.Project, error)
	MarkBuildFailed(ctx context.Context, ownerID int64, id, buildError string) error
	PublishProject(ctx context.Context, ownerID int64, id, commitSHA string, serviceCommits []byte) error
	ReclaimStuckBuilds(ctx context.Context, perServiceTimeout, grace time.Duration) (int64, error)
	SetProjectSlug(ctx context.Context, ownerID int64, id, slug string) error
	OwnerByUsername(ctx context.Context, username string) (store.Owner, error)
	ListPublishedProjectsByUsername(ctx context.Context, username string) ([]store.Project, error)
	GetPublishedProjectBySlug(ctx context.Context, username, slug string) (store.Project, error)
}

// ProjectBuilder builds every service of a Project's manifest from source at publish,
// returning the commit each service was built at (service name -> commit SHA) so the
// publisher can pin them — a Project whose services span repos gets a commit per repo
// (#50). It is the seam to the image builder + repo clone. An error means the build
// failed — the Project is not published. ownerLogin scopes which installation the clone
// token is minted for. It is synchronous (the Publisher runs it on a background goroutine).
type ProjectBuilder interface {
	BuildProject(ctx context.Context, ownerLogin, projectID string, manifest runcontract.Manifest) (commits map[string]string, err error)
}

// RepoAccessChecker verifies that the signed-in Owner is entitled to publish a repo they
// do not own outright — an org repo, or any repo under another account (#50): it reports
// whether username has access to repoOwner/repoName. The real implementation
// (githubapp.Client) checks the Owner's repository permission through the App installation.
// When nil, only the Owner's own repos (owner == their login) may be published.
type RepoAccessChecker interface {
	HasRepoAccess(ctx context.Context, repoOwner, repoName, username string) (bool, error)
}

// Handlers serve the Owner project endpoints. Construct with NewHandlers and mount
// each method behind auth.RequireOwner (they read the Owner from context).
type Handlers struct {
	store     Store
	publisher *Publisher        // nil => publishing is not configured (501)
	access    RepoAccessChecker // nil => only the Owner's own repos may be published
}

// HandlerOption configures optional Handlers dependencies.
type HandlerOption func(*Handlers)

// WithRepoAccess enables publishing repos the Owner does not own directly (org repos),
// gated by verifying the Owner's GitHub access to each (#50). Without it, only the Owner's
// own repos (owner == their login) may be published.
func WithRepoAccess(a RepoAccessChecker) HandlerOption { return func(h *Handlers) { h.access = a } }

// NewHandlers wires the project handlers to a Store and (optionally) a Publisher. A nil
// publisher leaves create/list/get/update working but makes publish report 501 (no GitHub
// App credentials in dev). Pass WithRepoAccess to allow publishing org repos.
func NewHandlers(s Store, p *Publisher, opts ...HandlerOption) *Handlers {
	h := &Handlers{store: s, publisher: p}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

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

	name, encoded, ok := decodeProjectForm(w, r)
	if !ok {
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

// decodeProjectForm reads a Form request body (the shape Create and Update both accept),
// validates the name, and maps the form to a runcontract.Manifest, validating it with
// the contract's own Validate (the single source of manifest validation). It returns the
// trimmed name and the canonical manifest encoding to persist. On any problem it writes
// the HTTP error response (400 for a bad body / invalid manifest, 500 for an encode
// failure) and returns ok=false, so the caller simply returns.
func decodeProjectForm(w http.ResponseWriter, r *http.Request) (name string, encoded []byte, ok bool) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	var form Form
	if err := dec.Decode(&form); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return "", nil, false
	}

	name = strings.TrimSpace(form.Name)
	if name == "" || utf8.RuneCountInString(name) > maxNameLen {
		http.Error(w, fmt.Sprintf("name is required and must be at most %d characters", maxNameLen), http.StatusBadRequest)
		return "", nil, false
	}

	manifest, err := form.toManifest()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", nil, false
	}
	if err := manifest.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", nil, false
	}
	encoded, err = json.Marshal(manifest)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return "", nil, false
	}
	return name, encoded, true
}

// Update replaces the signed-in Owner's Project configuration (name + manifest). Body: a
// Form (JSON), the same shape Create accepts. It re-maps and re-validates the manifest,
// then replaces it; editing the manifest returns a published Project to draft (the
// published flag and the pinned commit are cleared — the Owner re-publishes to rebuild,
// #51), while a pure rename keeps it published. 400 on a bad body or invalid manifest,
// 404 if the Project is not theirs (or absent), 409 if the new name collides with another
// of their Projects, 200 with the updated view. Must be mounted behind RequireOwner with
// an {id} path value.
func (h *Handlers) Update(w http.ResponseWriter, r *http.Request) {
	owner, ok := auth.OwnerFrom(r.Context())
	if !ok {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}

	name, encoded, ok := decodeProjectForm(w, r)
	if !ok {
		return
	}

	p, err := h.store.UpdateProject(r.Context(), owner.ID, r.PathValue("id"), name, encoded)
	switch {
	case errors.Is(err, store.ErrProjectNotFound):
		http.Error(w, "project not found", http.StatusNotFound)
		return
	case errors.Is(err, store.ErrProjectNameTaken):
		http.Error(w, "a project with that name already exists", http.StatusConflict)
		return
	case err != nil:
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
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

// Publish kicks off a background build of the signed-in Owner's Project from source and
// returns immediately (202) with the Project in 'building' — the build proceeds off the
// request and settles the lifecycle when it finishes (building → published|failed, #49,
// ADR-0003: build at publish, run from cache). The Owner observes progress by re-reading
// the Project (buildState/buildError). Must be mounted behind RequireOwner with an {id}
// path value. 404 if the Project is not theirs, 501 if publishing is not configured (no
// GitHub App credentials), 409 if a build is already in flight, 422 if any service's repo
// is one the Owner may not publish (a repo under another account/org they lack GitHub
// access to, or org repos are not enabled), 202 with the 'building' view once launched.
func (h *Handlers) Publish(w http.ResponseWriter, r *http.Request) {
	owner, ok := auth.OwnerFrom(r.Context())
	if !ok {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	if h.publisher == nil {
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
	// Security: every service's repo must be one the Owner may publish — their own, or (with
	// an access checker) an org repo they have GitHub access to (#50). Services may span
	// several repos, each pinned to its own resolved commit. Checked before marking the
	// build started, so a rejected repo never enters 'building'. Network checks are bounded
	// by the request context.
	if err := checkRepoAccess(r.Context(), h.access, owner.GitHubLogin, m); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}

	// Transition to 'building' first — this also rejects a duplicate publish (409) while a
	// build is already in flight, so only one build of a Project runs at a time (#49). It
	// must precede launching the build so two concurrent requests can't both spawn a build.
	started, err := h.store.StartBuild(r.Context(), owner.ID, p.ID)
	switch {
	case errors.Is(err, store.ErrBuildInProgress):
		http.Error(w, "a build is already in progress for this project", http.StatusConflict)
		return
	case errors.Is(err, store.ErrProjectNotFound):
		http.Error(w, "project not found", http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Launch the build in the background; it settles build_state when it finishes. The
	// Owner gets the 'building' view immediately and polls for the outcome.
	h.publisher.Start(owner.ID, owner.GitHubLogin, p.ID, m)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(projectView(started))
}

// Portfolio is the PUBLIC listing of an Owner's published Projects at /{username}
// (ADR-0005, AC4) — ungated, since a Portfolio is public. 404 if no Owner has
// claimed the username (reserved names are never claimable, so they 404 here too);
// otherwise the Owner's public identity plus their published Projects (possibly an
// empty list). Each Project carries id + name + slug — enough for a Guest to play it (by
// id) and for the client to build /{username}/{slug} links (#51).
func (h *Handlers) Portfolio(w http.ResponseWriter, r *http.Request) {
	username := strings.ToLower(strings.TrimSpace(r.PathValue("username")))
	owner, err := h.store.OwnerByUsername(r.Context(), username)
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	projects, err := h.store.ListPublishedProjectsByUsername(r.Context(), username)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	views := make([]map[string]any, 0, len(projects))
	for _, p := range projects {
		// id (play by UUID) + name + slug (so the client can build /{username}/{slug} links, #51).
		views = append(views, map[string]any{"id": p.ID, "name": p.Name, "slug": p.Slug})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"owner":    map[string]any{"username": owner.Username},
		"projects": views,
	})
}

// SetSlug sets (or, with an empty slug, clears) the URL slug of the signed-in Owner's
// Project (#51). Body: {"slug":"..."}. The slug is editable; changing it does not affect
// publish state (a slug is a URL alias, not build config). 400 on a bad body or an invalid/
// reserved slug, 404 if the Project is not theirs, 409 if the slug is taken by another of
// their Projects, 200 with the updated view. Must be mounted behind RequireOwner with an
// {id} path value.
func (h *Handlers) SetSlug(w http.ResponseWriter, r *http.Request) {
	owner, ok := auth.OwnerFrom(r.Context())
	if !ok {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
	dec.DisallowUnknownFields()
	var body struct {
		Slug string `json:"slug"`
	}
	if err := dec.Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	canonical := ""
	if strings.TrimSpace(body.Slug) != "" {
		c, err := CanonicalSlug(body.Slug)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		canonical = c
	}

	id := r.PathValue("id")
	switch err := h.store.SetProjectSlug(r.Context(), owner.ID, id, canonical); {
	case errors.Is(err, store.ErrProjectNotFound):
		http.Error(w, "project not found", http.StatusNotFound)
		return
	case errors.Is(err, store.ErrProjectSlugTaken):
		http.Error(w, "a project with that slug already exists", http.StatusConflict)
		return
	case err != nil:
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Re-read to echo the updated view. SetProjectSlug returns only an error (it touches no
	// other state), so this is a deliberate second read rather than a RETURNING.
	p, err := h.store.GetOwnerProject(r.Context(), owner.ID, id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(projectView(p))
}

// ProjectBySlug is the PUBLIC resolution of /{username}/{slug} to a published Project
// (#51) — ungated, like the Portfolio. It returns just id + name + slug (enough for a Guest
// to play by id and display it), keeping /api/play strictly id-based so a non-UUID slug
// never reaches the play path. 404 if the username/slug does not resolve to a published
// Project (no draft/existence oracle).
func (h *Handlers) ProjectBySlug(w http.ResponseWriter, r *http.Request) {
	username := strings.ToLower(strings.TrimSpace(r.PathValue("username")))
	slug := strings.ToLower(strings.TrimSpace(r.PathValue("slug")))
	p, err := h.store.GetPublishedProjectBySlug(r.Context(), username, slug)
	switch {
	case errors.Is(err, store.ErrProjectNotFound):
		http.Error(w, "not found", http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": p.ID, "name": p.Name, "slug": p.Slug})
}

// checkRepoAccess is the publish authorization gate (#50): every service's repo must be one
// the signed-in Owner may publish. The Owner's OWN repos (parsed owner == their GitHub
// login) are always allowed with no network call. A repo under another account or org is
// allowed only when an access checker is configured AND it confirms the Owner has GitHub
// access to that repo (which also requires the App be installed, so the platform can build
// it). Without a checker, foreign repos are rejected. Services may span repos (each pinned
// to its own commit). Every failure is surfaced to the Owner as 422. The ownership fast
// path matters for security too: it can never be a confused-deputy, since a repo the Owner
// literally owns needs no external check.
func checkRepoAccess(ctx context.Context, access RepoAccessChecker, ownerLogin string, m runcontract.Manifest) error {
	for _, svc := range m.Services {
		owner, repo, err := githubapp.ParseRepo(svc.Repo)
		if err != nil {
			return fmt.Errorf("service %q: %v", svc.Name, err)
		}
		if strings.EqualFold(owner, ownerLogin) {
			continue // the Owner's own repo — always allowed
		}
		if access == nil {
			return fmt.Errorf("service %q: %s/%s is not your repository — building repos under another account or org is not enabled", svc.Name, owner, repo)
		}
		ok, err := access.HasRepoAccess(ctx, owner, repo, ownerLogin)
		if err != nil {
			if errors.Is(err, githubapp.ErrNoInstallation) {
				return fmt.Errorf("service %q: the Showcase GitHub App is not installed on %s/%s", svc.Name, owner, repo)
			}
			return fmt.Errorf("service %q: could not verify your access to %s/%s — please try again", svc.Name, owner, repo)
		}
		if !ok {
			return fmt.Errorf("service %q: you do not have access to %s/%s", svc.Name, owner, repo)
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

// projectView is the JSON shape returned for a Project: its identity, publish state, the
// build lifecycle (buildState/buildError, #49), the built commit (once published), and
// the stored run contract echoed back as an editable Form under "manifest" so the edit
// UI can round-trip it (#51). The Form's
// Name is set from the Project name (it is a column, not a manifest field). The stored
// manifest is canonical, validated JSON (the write path Marshals a Validated
// runcontract.Manifest), so the decode does not fail in practice; if it ever did, the
// manifest is omitted rather than failing the whole view (e.g. a List of many projects).
func projectView(p store.Project) map[string]any {
	v := map[string]any{
		"id":         p.ID,
		"name":       p.Name,
		"slug":       p.Slug,
		"published":  p.Published,
		"commitSha":  p.CommitSHA,
		"buildState": p.BuildState,
		"buildError": p.BuildError,
	}
	var m runcontract.Manifest
	if err := json.Unmarshal(p.Manifest, &m); err == nil {
		form := formFromManifest(m)
		form.Name = p.Name
		v["manifest"] = form
	}
	return v
}
