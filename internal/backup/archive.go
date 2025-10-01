/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sort"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
)

// BackupManager handles the backup operations
type BackupManager struct {
	Config          *rest.Config
	DynamicClient   dynamic.Interface
	DiscoveryClient discovery.DiscoveryInterface
}

// BackupOptions contains configuration for a backup operation
type BackupOptions struct {
	IncludeNamespaces       []string
	ExcludeNamespaces       []string
	IncludeClusterResources bool
	ResourceTypes           []string
}

// BackupResult contains the results of a backup operation
type BackupResult struct {
	ResourceCount int
	FilePath      string
	Error         error
}

// NewBackupManager creates a new BackupManager
func NewBackupManager(config *rest.Config) (*BackupManager, error) {
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery client: %w", err)
	}

	return &BackupManager{
		Config:          config,
		DynamicClient:   dynamicClient,
		DiscoveryClient: discoveryClient,
	}, nil
}

// CreateBackup performs a full cluster backup
func (bm *BackupManager) CreateBackup(ctx context.Context, storagePath string, opts BackupOptions) (*BackupResult, error) {
	log := ctrl.LoggerFrom(ctx)
	log.Info("Starting cluster backup", "storagePath", storagePath)

	// Create temporary directory for backup files
	tempDir, err := os.MkdirTemp("", "cluster-backup-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	resourceCount := 0

	// Discover all API resources
	apiResourceLists, err := bm.DiscoveryClient.ServerPreferredResources()
	if err != nil {
		log.Error(err, "Warning: Error discovering some API resources (continuing anyway)")
	}

	// Collect resources
	for _, apiResourceList := range apiResourceLists {
		if apiResourceList == nil {
			continue
		}

		gv, err := schema.ParseGroupVersion(apiResourceList.GroupVersion)
		if err != nil {
			log.Error(err, "Failed to parse group version", "groupVersion", apiResourceList.GroupVersion)
			continue
		}

		for _, apiResource := range apiResourceList.APIResources {
			// Skip subresources (like "pods/status")
			if strings.Contains(apiResource.Name, "/") {
				continue
			}

			// Skip resources that can't be listed
			if !contains(apiResource.Verbs, "list") {
				continue
			}

			// Filter resource types if specified
			if len(opts.ResourceTypes) > 0 && !contains(opts.ResourceTypes, apiResource.Kind) {
				continue
			}

			gvr := gv.WithResource(apiResource.Name)

			// Handle namespaced vs cluster-scoped resources
			if apiResource.Namespaced {
				// Backup namespaced resources
				namespaces, err := bm.getNamespacesToBackup(ctx, opts)
				if err != nil {
					return nil, fmt.Errorf("failed to get namespaces: %w", err)
				}

				for _, ns := range namespaces {
					count, err := bm.backupResource(ctx, gvr, ns, tempDir)
					if err != nil {
						log.Error(err, "Failed to backup resource", "gvr", gvr, "namespace", ns)
						continue
					}
					resourceCount += count
				}
			} else if opts.IncludeClusterResources {
				// Backup cluster-scoped resources
				count, err := bm.backupResource(ctx, gvr, "", tempDir)
				if err != nil {
					log.Error(err, "Failed to backup cluster resource", "gvr", gvr)
					continue
				}
				resourceCount += count
			}
		}
	}

	// Create archive
	archivePath, err := bm.createArchive(tempDir, storagePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create archive: %w", err)
	}

	log.Info("Backup completed successfully", "resourceCount", resourceCount, "archivePath", archivePath)

	return &BackupResult{
		ResourceCount: resourceCount,
		FilePath:      archivePath,
	}, nil
}

// getNamespacesToBackup returns the list of namespaces to backup based on options
func (bm *BackupManager) getNamespacesToBackup(ctx context.Context, opts BackupOptions) ([]string, error) {
	// If specific namespaces are included, use those
	if len(opts.IncludeNamespaces) > 0 {
		return opts.IncludeNamespaces, nil
	}

	// Otherwise, get all namespaces and filter exclusions
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	list, err := bm.DynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var namespaces []string
	for _, item := range list.Items {
		ns := item.GetName()
		if !contains(opts.ExcludeNamespaces, ns) {
			namespaces = append(namespaces, ns)
		}
	}

	return namespaces, nil
}

// backupResource backs up a specific resource type
func (bm *BackupManager) backupResource(ctx context.Context, gvr schema.GroupVersionResource, namespace, tempDir string) (int, error) {
	log := ctrl.LoggerFrom(ctx)

	var list *unstructured.UnstructuredList
	var err error

	if namespace != "" {
		list, err = bm.DynamicClient.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	} else {
		list, err = bm.DynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{})
	}

	if err != nil {
		return 0, err
	}

	if len(list.Items) == 0 {
		return 0, nil
	}

	// Create directory structure
	var dirPath string
	if namespace != "" {
		dirPath = filepath.Join(tempDir, "namespaces", namespace, gvr.Group, gvr.Version, gvr.Resource)
	} else {
		dirPath = filepath.Join(tempDir, "cluster", gvr.Group, gvr.Version, gvr.Resource)
	}

	if err := os.MkdirAll(dirPath, 0755); err != nil {
		return 0, err
	}

	// Save each resource
	count := 0
	for _, item := range list.Items {
		// Remove managed fields and other runtime data
		cleanResource(&item)

		data, err := json.MarshalIndent(item.Object, "", "  ")
		if err != nil {
			log.Error(err, "Failed to marshal resource", "name", item.GetName())
			continue
		}

		filename := filepath.Join(dirPath, fmt.Sprintf("%s.json", item.GetName()))
		if err := os.WriteFile(filename, data, 0644); err != nil {
			log.Error(err, "Failed to write resource file", "filename", filename)
			continue
		}
		count++
	}

	return count, nil
}

// cleanResource removes runtime fields that shouldn't be in backups
func cleanResource(obj *unstructured.Unstructured) {
	// Remove managed fields
	unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")

	// Remove resource version and UID as they are cluster-specific
	unstructured.RemoveNestedField(obj.Object, "metadata", "resourceVersion")
	unstructured.RemoveNestedField(obj.Object, "metadata", "uid")
	unstructured.RemoveNestedField(obj.Object, "metadata", "selfLink")
	unstructured.RemoveNestedField(obj.Object, "metadata", "creationTimestamp")
	unstructured.RemoveNestedField(obj.Object, "metadata", "generation")

	// Remove status as it will be regenerated
	unstructured.RemoveNestedField(obj.Object, "status")
}

// createArchive creates a tar.gz archive from the backup directory
func (bm *BackupManager) createArchive(sourceDir, storagePath string) (string, error) {
	// Resolve host:// storage path to an in-container mount path if applicable.
	// Convention: host://<absolute-path> maps to /host-mounts/<absolute-path-without-leading-slash>
	resolvedStoragePath := storagePath
	if strings.HasPrefix(storagePath, "host://") {
		hostPath := strings.TrimPrefix(storagePath, "host://")
		// sanitize leading slash
		hostPath = strings.TrimPrefix(hostPath, "/")
		resolvedStoragePath = filepath.Join("/host-mounts", hostPath)
	}

	// Ensure storage directory exists
	storageDir := resolvedStoragePath
	if err := os.MkdirAll(storageDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create storage directory: %w", err)
	}

	// Create archive file with timestamp
	timestamp := time.Now().Format("20060102-150405")
	archivePath := filepath.Join(resolvedStoragePath, fmt.Sprintf("cluster-backup-%s.tar.gz", timestamp))

	file, err := os.Create(archivePath)
	if err != nil {
		return "", fmt.Errorf("failed to create archive file: %w", err)
	}
	defer file.Close()

	// Create gzip writer
	gzWriter := gzip.NewWriter(file)
	defer gzWriter.Close()

	// Create tar writer
	tarWriter := tar.NewWriter(gzWriter)
	defer tarWriter.Close()

	// Walk through source directory
	err = filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Create tar header
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}

		// Update header name to be relative to source directory
		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		header.Name = relPath

		// Write header
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}

		// If not a regular file, skip
		if !info.Mode().IsRegular() {
			return nil
		}

		// Write file content
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		if _, err := io.Copy(tarWriter, file); err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return "", fmt.Errorf("failed to create tar archive: %w", err)
	}

	return archivePath, nil
}

// CleanupArchives removes old archives based on retention days and max archives
func (bm *BackupManager) CleanupArchives(storagePath string, retentionDays *int, maxArchives *int) error {
	resolvedStoragePath := storagePath
	if strings.HasPrefix(storagePath, "host://") {
		hostPath := strings.TrimPrefix(storagePath, "host://")
		hostPath = strings.TrimPrefix(hostPath, "/")
		resolvedStoragePath = filepath.Join("/host-mounts", hostPath)
	}

	entries, err := os.ReadDir(resolvedStoragePath)
	if err != nil {
		return fmt.Errorf("failed to read storage directory: %w", err)
	}

	// collect archive files with info
	var files []os.DirEntry
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), "cluster-backup-") && strings.HasSuffix(e.Name(), ".tar.gz") {
			files = append(files, e)
		}
	}

	// sort by name (timestamp in name gives chronological order)
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })

	// Apply retentionDays
	if retentionDays != nil {
		cutoff := time.Now().Add(-time.Duration(*retentionDays) * 24 * time.Hour)
		for _, f := range files {
			fi, err := f.Info()
			if err != nil {
				continue
			}
			if fi.ModTime().Before(cutoff) {
				os.Remove(filepath.Join(resolvedStoragePath, f.Name()))
			}
		}
	}

	// Re-read and enforce maxArchives if needed
	if maxArchives != nil {
		entries2, err := os.ReadDir(resolvedStoragePath)
		if err != nil {
			return nil
		}
		var files2 []os.DirEntry
		for _, e := range entries2 {
			if e.IsDir() {
				continue
			}
			if strings.HasPrefix(e.Name(), "cluster-backup-") && strings.HasSuffix(e.Name(), ".tar.gz") {
				files2 = append(files2, e)
			}
		}
		sort.Slice(files2, func(i, j int) bool { return files2[i].Name() < files2[j].Name() })
		if len(files2) > *maxArchives {
			// delete oldest first
			toDelete := len(files2) - *maxArchives
			for i := 0; i < toDelete; i++ {
				os.Remove(filepath.Join(resolvedStoragePath, files2[i].Name()))
			}
		}
	}

	return nil
}

// contains checks if a slice contains a string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// GetDefaultResourceTypes returns a list of common Kubernetes resource types to backup
func GetDefaultResourceTypes() []string {
	return []string{
		// Core resources
		"Namespace",
		"ConfigMap",
		"Secret",
		"Service",
		"ServiceAccount",
		"PersistentVolume",
		"PersistentVolumeClaim",
		"Pod",

		// Workloads
		"Deployment",
		"StatefulSet",
		"DaemonSet",
		"ReplicaSet",
		"Job",
		"CronJob",

		// Networking
		"Ingress",
		"NetworkPolicy",
		"Endpoints",

		// RBAC
		"Role",
		"RoleBinding",
		"ClusterRole",
		"ClusterRoleBinding",

		// Storage
		"StorageClass",

		// Custom Resources (will attempt to backup all CRDs)
		"CustomResourceDefinition",
	}
}

// SetCondition updates or adds a condition to the status
func SetCondition(conditions *[]metav1.Condition, conditionType string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	condition := metav1.Condition{
		Type:               conditionType,
		Status:             status,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            message,
	}

	meta.SetStatusCondition(conditions, condition)
}
