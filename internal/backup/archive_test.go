package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

	if got, want := resolveStoragePath("host:///var/backups"), filepath.Join("/tmp", "var", "backups"); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}

	if got, want := resolveStoragePath("host:///../etc"), filepath.Join("/tmp", "etc"); got != want {
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

func TestRestoreBackup(t *testing.T) {
	t.Parallel()

	storageDir := t.TempDir()
	archiveName := "cluster-backup-restore.tar.gz"
	writeRestoreArchive(t, filepath.Join(storageDir, archiveName))

	scheme := runtime.NewScheme()
	registerUnstructuredType(scheme, schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Namespace"})
	registerUnstructuredType(scheme, schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"})

	dynamicClient := fake.NewSimpleDynamicClient(scheme)
	bm := &BackupManager{DynamicClient: dynamicClient}

	result, err := bm.RestoreBackup(context.Background(), storageDir, archiveName)
	if err != nil {
		t.Fatalf("RestoreBackup returned error: %v", err)
	}

	if result.ResourcesApplied != 2 {
		t.Fatalf("expected 2 resources applied, got %d", result.ResourcesApplied)
	}

	namespaceGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	if _, err := dynamicClient.Resource(namespaceGVR).Get(context.Background(), "restore-ns", metav1.GetOptions{}); err != nil {
		t.Fatalf("expected namespace to exist: %v", err)
	}

	configMapGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	cm, err := dynamicClient.Resource(configMapGVR).Namespace("restore-ns").Get(context.Background(), "sample-config", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected configmap to exist: %v", err)
	}

	if cm.GetNamespace() != "restore-ns" {
		t.Fatalf("expected configmap namespace restore-ns, got %s", cm.GetNamespace())
	}
}

func writeRestoreArchive(t *testing.T, archivePath string) {
	t.Helper()

	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("failed to create archive: %v", err)
	}
	defer file.Close()

	gz := gzip.NewWriter(file)
	defer gz.Close()

	tarWriter := tar.NewWriter(gz)
	defer tarWriter.Close()

	writeJSONTarEntry(t, tarWriter, "cluster/v1/namespaces/restore-ns.json", map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata": map[string]interface{}{
			"name": "restore-ns",
		},
	})

	writeJSONTarEntry(t, tarWriter, "namespaces/restore-ns/v1/configmaps/sample-config.json", map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]interface{}{
			"name": "sample-config",
		},
		"data": map[string]string{
			"key": "value",
		},
	})
}

func writeJSONTarEntry(t *testing.T, tw *tar.Writer, name string, obj interface{}) {
	data, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal test object %s: %v", name, err)
	}

	header := &tar.Header{
		Name: name,
		Mode: 0o644,
		Size: int64(len(data)),
	}

	if err := tw.WriteHeader(header); err != nil {
		t.Fatalf("failed to write tar header %s: %v", name, err)
	}

	if _, err := tw.Write(data); err != nil {
		t.Fatalf("failed to write tar data %s: %v", name, err)
	}
}

func registerUnstructuredType(scheme *runtime.Scheme, gvk schema.GroupVersionKind) {
	scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	listGVK := schema.GroupVersionKind{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind + "List"}
	scheme.AddKnownTypeWithName(listGVK, &unstructured.UnstructuredList{})
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
