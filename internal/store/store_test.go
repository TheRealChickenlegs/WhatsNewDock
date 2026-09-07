package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	now := time.Now().UTC()
	srv := &Server{ID: "srv1", Name: "local", IsLocal: true, Status: "online", LastSeen: now, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateServer(srv); err != nil {
		t.Fatalf("create server: %v", err)
	}

	stacks := []*Stack{{Name: "web", Kind: StackCompose}}
	containers := []*Container{
		{
			DockerID:   "deadbeef",
			Name:       "nginx",
			Image:      "nginx:1.25",
			ImageName:  "docker.io/library/nginx",
			ImageTag:   "1.25",
			Registry:   "docker.io",
			Repository: "library/nginx",
			State:      "running",
			Running:    true,
			StackName:  "web",
			Labels:     map[string]string{"com.docker.compose.project": "web"},
			UpdatedAt:  now,
			CreatedAt:  now,
		},
	}

	if err := st.ReplaceServerSnapshot(srv, stacks, containers); err != nil {
		t.Fatalf("replace snapshot: %v", err)
	}

	// Containers listed with their stack resolved.
	got, err := st.ListContainers(ContainerFilter{})
	if err != nil {
		t.Fatalf("list containers: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 container, got %d", len(got))
	}
	if got[0].StackName != "web" || got[0].StackID == "" {
		t.Errorf("stack not resolved: name=%q id=%q", got[0].StackName, got[0].StackID)
	}

	// Update record.
	upd := &Update{ContainerID: got[0].ID, CurrentTag: "1.25", LatestTag: "1.28", VersionsBehind: 3, Source: "github", CheckedAt: now}
	if err := st.UpsertUpdate(upd); err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	got2, err := st.GetContainer(got[0].ID)
	if err != nil {
		t.Fatalf("get container: %v", err)
	}
	if got2.Update == nil || got2.Update.LatestTag != "1.28" {
		t.Errorf("expected update latest=1.28, got %+v", got2.Update)
	}

	// Pinned state preserved across a re-report.
	if err := st.SetContainerPinned(got[0].ID, true); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if err := st.ReplaceServerSnapshot(srv, stacks, containers); err != nil {
		t.Fatalf("re-report: %v", err)
	}
	got3, _ := st.GetContainer(got[0].ID)
	if !got3.Pinned {
		t.Error("expected pinned state to persist across snapshot replacement")
	}

	// Releases.
	if err := st.UpsertReleases("gh:owner/repo", []Release{
		{Tag: "v1.28.0", Title: "One", PublishedAt: now},
		{Tag: "v1.27.0", Title: "Two", PublishedAt: now.Add(-time.Hour)},
	}); err != nil {
		t.Fatalf("upsert releases: %v", err)
	}
	rels, err := st.ListReleases("gh:owner/repo", 0)
	if err != nil || len(rels) != 2 {
		t.Fatalf("list releases: %d %v", len(rels), err)
	}
}

func TestUserRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	u := &User{Username: "admin", Role: "admin", PasswordHash: "hash"}
	if err := st.CreateUser(u); err != nil {
		t.Fatalf("create user: %v", err)
	}
	got, err := st.GetUserByUsername("admin")
	if err != nil || got == nil {
		t.Fatalf("get user: %v %v", got, err)
	}
	if got.Role != "admin" {
		t.Errorf("role = %q", got.Role)
	}
}
