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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ClusterBackupSpec defines the desired state of ClusterBackup
type ClusterBackupSpec struct {
	// StoragePath defines where the backup archive will be stored
	// This can be a local path or a cloud storage URL (e.g., s3://bucket/path)
	// +kubebuilder:validation:Required
	StoragePath string `json:"storagePath"`

	// IncludeNamespaces specifies which namespaces to include in the backup
	// If empty, all namespaces will be backed up
	// +optional
	IncludeNamespaces []string `json:"includeNamespaces,omitempty"`

	// ExcludeNamespaces specifies namespaces to exclude from the backup
	// +optional
	ExcludeNamespaces []string `json:"excludeNamespaces,omitempty"`

	// IncludeClusterResources specifies whether to backup cluster-scoped resources
	// like ClusterRoles, ClusterRoleBindings, PersistentVolumes, etc.
	// +kubebuilder:default:=true
	// +optional
	IncludeClusterResources *bool `json:"includeClusterResources,omitempty"`

	// ResourceTypes specifies which resource types to backup
	// If empty, common resource types will be backed up
	// +optional
	ResourceTypes []string `json:"resourceTypes,omitempty"`

	// Schedule defines a cron schedule for automatic backups
	// If empty, backup runs once when the resource is created
	// +optional
	Schedule string `json:"schedule,omitempty"`
}

// ClusterBackupStatus defines the observed state of ClusterBackup.
type ClusterBackupStatus struct {
	// Phase represents the current phase of the backup (Pending, Running, Completed, Failed)
	// +optional
	Phase string `json:"phase,omitempty"`

	// StartTime is the time when the backup started
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is the time when the backup completed
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// BackupLocation is the final location of the backup archive
	// +optional
	BackupLocation string `json:"backupLocation,omitempty"`

	// ResourceCount is the number of resources backed up
	// +optional
	ResourceCount int `json:"resourceCount,omitempty"`

	// Message provides additional information about the backup status
	// +optional
	Message string `json:"message,omitempty"`

	// LastBackupTime is the timestamp of the last successful backup (for scheduled backups)
	// +optional
	LastBackupTime *metav1.Time `json:"lastBackupTime,omitempty"`

	// conditions represent the current state of the ClusterBackup resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ClusterBackup is the Schema for the clusterbackups API
type ClusterBackup struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of ClusterBackup
	// +required
	Spec ClusterBackupSpec `json:"spec"`

	// status defines the observed state of ClusterBackup
	// +optional
	Status ClusterBackupStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// ClusterBackupList contains a list of ClusterBackup
type ClusterBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterBackup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterBackup{}, &ClusterBackupList{})
}
