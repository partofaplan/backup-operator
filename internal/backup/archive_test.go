package backup

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
)

func TestCleanupArchivesRetentionAndMax(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bm := &BackupManager{}

	createArchiveFile(t, dir, "cluster-backup-20240101-000000.tar.gz", 48*time.Hour)
	createArchiveFile(t, dir, "cluster-backup-20250101-010000.tar.gz", 2*time.Hour)
	createArchiveFile(t, dir, "cluster-backup-20250102-010000.tar.gz", time.Hour)
	createArchiveFile(t, dir, "cluster-backup-20250103-010000.tar.gz", 0)

	retention := 1
	maxArchives := 2

	if err := bm.CleanupArchives(dir, &retention, &maxArchives); err != nil {
		t.Fatalf("CleanupArchives returned error: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	expected := []string{
		"cluster-backup-20250102-010000.tar.gz",
		"cluster-backup-20250103-010000.tar.gz",
	}

	if len(names) != len(expected) {
		t.Fatalf("expected %d archives, got %d (%v)", len(expected), len(names), names)
	}
	for i, name := range expected {
		if names[i] != name {
			t.Fatalf("expected archive %q at position %d, got %q", name, i, names[i])
		}
	}
}

func TestCleanupArchivesMissingDirectory(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "missing")
	bm := &BackupManager{}

	if err := bm.CleanupArchives(path, nil, nil); err != nil {
		t.Fatalf("expected no error for missing directory, got %v", err)
	}
}

func TestResolveStoragePath(t *testing.T) {
	t.Parallel()

	if got, want := resolveStoragePath("/var/backups"), "/var/backups"; got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}

	if got, want := resolveStoragePath("host:///var/backups"), filepath.Join("/host-mounts", "var", "backups"); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}

	if got, want := resolveStoragePath("host:///../etc"), filepath.Join("/host-mounts", "etc"); got != want {
		t.Fatalf("expected traversal-safe path %q, got %q", want, got)
	}
}

func TestGetNamespacesToBackupExcludes(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed adding corev1 to scheme: %v", err)
	}

	objects := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "custom"}},
	}

	dynamicClient := fake.NewSimpleDynamicClient(scheme, objects...)
	bm := &BackupManager{DynamicClient: dynamicClient}

	opts := BackupOptions{ExcludeNamespaces: []string{"kube-system"}}
	namespaces, err := bm.getNamespacesToBackup(context.Background(), opts)
	if err != nil {
		t.Fatalf("getNamespacesToBackup returned error: %v", err)
	}

	if len(namespaces) != 2 {
		t.Fatalf("expected 2 namespaces, got %d (%v)", len(namespaces), namespaces)
	}

	got := make(map[string]struct{})
	for _, ns := range namespaces {
		got[ns] = struct{}{}
	}

	for _, want := range []string{"custom", "default"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("expected namespace %q to be present (got %v)", want, namespaces)
		}
	}
}

func createArchiveFile(t *testing.T, dir, name string, age time.Duration) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, name), []byte("test"), 0o644); err != nil {
		t.Fatalf("failed writing archive %s: %v", name, err)
	}

	modTime := time.Now().Add(-age)
	if err := os.Chtimes(filepath.Join(dir, name), modTime, modTime); err != nil {
		t.Fatalf("failed setting modtime for %s: %v", name, err)
	}
}
