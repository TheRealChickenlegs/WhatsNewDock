package server

import (
	"context"
	"net/http"
	"time"

	"github.com/whatsnewdock/whatsnewdock/internal/dockerx"
	"github.com/whatsnewdock/whatsnewdock/internal/selfupdate"
)

// handleSelfUpdate reports what the app knows about its own updates. It never
// performs the check itself: the periodic loop owns that, so opening the UI
// cannot be turned into an API-rate-limit amplifier.
func (s *Server) handleSelfUpdate(w http.ResponseWriter, r *http.Request) {
	res := s.selfUpdate.Cached()
	out := map[string]any{
		"current":          res.Current,
		"latest":           res.Latest,
		"update_available": res.UpdateAvailable,
		"checked_at":       res.CheckedAt,
		"enabled":          s.cfg.SelfUpdate.Enabled,
		"interval":         s.selfUpdateInterval().String(),
		"repo":             s.cfg.SelfUpdate.Repo,
		// Applying an update needs the Docker client and a container we can
		// identify; the UI hides the button when either is missing.
		"can_apply":     s.docker != nil && dockerx.SelfContainerID() != "",
		"current_image": s.selfUpdateImage(r.Context()),
	}
	if res.URL != "" {
		out["url"] = res.URL
	}
	if res.Name != "" {
		out["name"] = res.Name
	}
	if !res.PublishedAt.IsZero() {
		out["published_at"] = res.PublishedAt
	}
	if res.Error != "" {
		out["error"] = res.Error
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSelfUpdateRun reports the live agent-then-controller update, so the UI
// can follow it up to the moment this process is replaced.
func (s *Server) handleSelfUpdateRun(w http.ResponseWriter, r *http.Request) {
	run := s.selfRun.get()
	if run == nil {
		writeJSON(w, http.StatusOK, map[string]any{"active": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": !run.Finished, "run": run})
}

// handleCheckSelfUpdate runs a check on demand (admin only).
func (s *Server) handleCheckSelfUpdate(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res := s.selfUpdate.Check(ctx)
	writeJSON(w, http.StatusOK, res)
}

// handleApplySelfUpdate redeploys this app onto the newest release.
//
// The work is done by a short-lived helper container, because the stop/rename/
// create/start sequence kills whoever is driving it — and that would be us.
// This handler returns before the app goes down, so the UI can tell the user
// what is about to happen.
func (s *Server) handleApplySelfUpdate(w http.ResponseWriter, r *http.Request) {
	if s.docker == nil {
		writeError(w, http.StatusConflict, "self-update needs a Docker connection; this instance has none")
		return
	}
	// A build update has no version pair (Latest is empty by design), so the
	// only thing that decides whether there is anything to do is the signal.
	res := s.selfUpdate.Cached()
	if !res.UpdateAvailable {
		writeError(w, http.StatusConflict, "no newer version is known; run a check first")
		return
	}
	selfID := dockerx.SelfContainerID()

	// Redeploy onto the image reference we are already using, with the tag
	// swapped: that keeps the registry and repository the operator chose rather
	// than assuming ghcr.io.
	// The check already worked out what to deploy: the release tag for a new
	// release, or the same reference re-pulled for a rebuilt tag.
	target := res.Target
	if s.cfg.SelfUpdate.Image != "" {
		target = s.cfg.SelfUpdate.Image
	}
	if target == "" {
		writeError(w, http.StatusConflict, "cannot determine which image to deploy")
		return
	}

	if selfID == "" {
		writeError(w, http.StatusConflict,
			"this instance cannot identify its own container, so it cannot redeploy itself — "+
				"update the deployment with `docker compose pull && docker compose up -d`")
		return
	}

	// The agents go first; the controller is replaced only once they are back.
	// The run is reported through /api/v1/selfupdate/run, because this process
	// will not be around to answer by the time it finishes.
	var body struct {
		Force bool `json:"force"`
	}
	_ = readJSON(r, &body) // an empty body is fine

	run := s.startSelfUpdateRun(
		selfupdate.Result{Current: res.Current, Target: target, Kind: res.Kind, UpdateAvailable: true},
		body.Force)
	writeJSON(w, http.StatusAccepted, run)
}

func selfUpdateActor(r *http.Request, s *Server) string {
	if u := s.currentUser(r); u != nil {
		return u.Username
	}
	return "system"
}
