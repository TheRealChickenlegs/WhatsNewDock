package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/whatsnewdock/whatsnewdock/internal/server/auth"
	"github.com/whatsnewdock/whatsnewdock/internal/store"
)

// Runtime-settings keys persisted in the settings table. These override the
// equivalent configuration values once saved via the UI.
const (
	setChangelogCount     = "settings.changelog_count"
	setIncludePreReleases = "settings.include_prereleases"
	setIgnoreImages       = "settings.ignore_images"
	setGitHubToken        = "settings.github_token"
	setGitLabToken        = "settings.gitlab_token"
	setUpdateInterval     = "settings.update_interval"
)

func (s *Server) loadRuntimeSettings() error {
	if v, err := s.st.GetSetting(setChangelogCount); err == nil && v != "" {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			s.cfg.Updates.ChangelogCount = n
		}
	}
	if v, err := s.st.GetSetting(setIncludePreReleases); err == nil && v != "" {
		s.cfg.Updates.IncludePreReleases = v == "1" || v == "true"
	}
	if v, err := s.st.GetSetting(setIgnoreImages); err == nil && v != "" {
		s.cfg.Updates.IgnoreImages = splitCSV(v)
	}
	if v, err := s.st.GetSetting(setGitHubToken); err == nil && v != "" {
		s.cfg.Updates.GitHubToken = v
		s.ch.SetGitHubToken(v)
	}
	if v, err := s.st.GetSetting(setGitLabToken); err == nil && v != "" {
		s.cfg.Updates.GitLabToken = v
		s.ch.SetGitLabToken(v)
	}
	return nil
}

func (s *Server) saveRuntimeSettings() error {
	if err := s.st.SetSetting(setChangelogCount, strconv.Itoa(s.cfg.Updates.ChangelogCount)); err != nil {
		return err
	}
	if err := s.st.SetSetting(setIncludePreReleases, strconv.FormatBool(s.cfg.Updates.IncludePreReleases)); err != nil {
		return err
	}
	if err := s.st.SetSetting(setIgnoreImages, strings.Join(s.cfg.Updates.IgnoreImages, ",")); err != nil {
		return err
	}
	if s.cfg.Updates.GitHubToken != "" {
		if err := s.st.SetSetting(setGitHubToken, s.cfg.Updates.GitHubToken); err != nil {
			return err
		}
	}
	if s.cfg.Updates.GitLabToken != "" {
		if err := s.st.SetSetting(setGitLabToken, s.cfg.Updates.GitLabToken); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"changelog_count":     s.cfg.Updates.ChangelogCount,
		"include_prereleases": s.cfg.Updates.IncludePreReleases,
		"ignore_images":       s.cfg.Updates.IgnoreImages,
		"update_interval":     s.cfg.Updates.Interval.String(),
		"github_token_set":    s.cfg.Updates.GitHubToken != "",
		"gitlab_token_set":    s.cfg.Updates.GitLabToken != "",
		"auth_mode":           string(s.cfg.Auth.Mode),
		"oidc_enabled":        s.auth.OIDCEnabled(),
		"oidc_issuer":         s.cfg.Auth.OIDC.IssuerURL,
		"enable_recreate":     s.cfg.Docker.EnableRecreate,
		"base_url":            s.cfg.BaseURL,
	})
}

type settingsRequest struct {
	ChangelogCount     *int     `json:"changelog_count"`
	IncludePreReleases *bool    `json:"include_prereleases"`
	IgnoreImages       []string `json:"ignore_images"`
	UpdateInterval     *string  `json:"update_interval"`
	GitHubToken        string   `json:"github_token"`
	GitLabToken        string   `json:"gitlab_token"`
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var req settingsRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.ChangelogCount != nil {
		if *req.ChangelogCount < 1 || *req.ChangelogCount > 100 {
			writeError(w, http.StatusBadRequest, "changelog_count must be 1..100")
			return
		}
		s.cfg.Updates.ChangelogCount = *req.ChangelogCount
	}
	if req.IncludePreReleases != nil {
		s.cfg.Updates.IncludePreReleases = *req.IncludePreReleases
	}
	if req.IgnoreImages != nil {
		s.cfg.Updates.IgnoreImages = req.IgnoreImages
	}
	if req.UpdateInterval != nil && *req.UpdateInterval != "" {
		d, err := time.ParseDuration(*req.UpdateInterval)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid update_interval")
			return
		}
		s.cfg.Updates.Interval = d
	}
	// Tokens: only update when a non-empty value is supplied.
	if req.GitHubToken != "" {
		s.cfg.Updates.GitHubToken = req.GitHubToken
		s.ch.SetGitHubToken(req.GitHubToken)
	}
	if req.GitLabToken != "" {
		s.cfg.Updates.GitLabToken = req.GitLabToken
		s.ch.SetGitLabToken(req.GitLabToken)
	}
	if err := s.saveRuntimeSettings(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- User management ------------------------------------------------------

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.st.ListUsers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if users == nil {
		users = []store.User{}
	}
	writeJSON(w, http.StatusOK, users)
}

type createUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "username required and password must be >= 8 chars")
		return
	}
	if req.Role != auth.RoleAdmin && req.Role != auth.RoleViewer {
		req.Role = auth.RoleViewer
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	u := &store.User{Username: req.Username, Role: req.Role, PasswordHash: hash}
	if err := s.st.CreateUser(u); err != nil {
		writeError(w, http.StatusConflict, "username already exists")
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Role     string `json:"role"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Role != "" {
		if req.Role != auth.RoleAdmin && req.Role != auth.RoleViewer {
			writeError(w, http.StatusBadRequest, "invalid role")
			return
		}
		if err := s.st.UpdateUserRole(id, req.Role); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if req.Password != "" {
		if len(req.Password) < 8 {
			writeError(w, http.StatusBadRequest, "password must be >= 8 chars")
			return
		}
		hash, err := auth.HashPassword(req.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := s.st.UpdateUserPassword(id, hash); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u := s.currentUser(r)
	// Prevent deleting yourself.
	if u != nil {
		if target, err := s.st.GetUserByUsername(u.Username); err == nil && target != nil && target.ID == id {
			writeError(w, http.StatusBadRequest, "cannot delete your own account")
			return
		}
	}
	if err := s.st.DeleteUser(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func splitCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
