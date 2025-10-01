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

package controller

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	backupv1alpha1 "github.com/zachperkins/backup-operator/api/v1alpha1"
	"github.com/zachperkins/backup-operator/internal/backup"
)

const (
	backupFinalizer = "backup.backup.io/finalizer"
)

// ClusterBackupReconciler reconciles a ClusterBackup object
type ClusterBackupReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	BackupManager *backup.BackupManager
}

// +kubebuilder:rbac:groups=backup.backup.io,resources=clusterbackups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=backup.backup.io,resources=clusterbackups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=backup.backup.io,resources=clusterbackups/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=*,verbs=get;list
// +kubebuilder:rbac:groups="*",resources=*,verbs=get;list

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ClusterBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the ClusterBackup instance
	clusterBackup := &backupv1alpha1.ClusterBackup{}
	if err := r.Get(ctx, req.NamespacedName, clusterBackup); err != nil {
		if errors.IsNotFound(err) {
			// Object not found, return without error
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get ClusterBackup")
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !clusterBackup.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, clusterBackup)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(clusterBackup, backupFinalizer) {
		controllerutil.AddFinalizer(clusterBackup, backupFinalizer)
		if err := r.Update(ctx, clusterBackup); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Check if backup has already been completed
	if clusterBackup.Status.Phase == "Completed" || clusterBackup.Status.Phase == "Failed" {
		// If there's a schedule, requeue for next run
		if clusterBackup.Spec.Schedule != "" {
			// TODO: Implement cron scheduling
			return ctrl.Result{RequeueAfter: time.Hour}, nil
		}
		// One-time backup already done
		return ctrl.Result{}, nil
	}

	// Update status to Running if not already set
	if clusterBackup.Status.Phase == "" || clusterBackup.Status.Phase == "Pending" {
		clusterBackup.Status.Phase = "Running"
		now := metav1.Now()
		clusterBackup.Status.StartTime = &now
		clusterBackup.Status.Message = "Backup in progress"
		if err := r.Status().Update(ctx, clusterBackup); err != nil {
			log.Error(err, "Failed to update status to Running")
			return ctrl.Result{}, err
		}
	}

	// Perform the backup
	result, err := r.performBackup(ctx, clusterBackup)
	if err != nil {
		log.Error(err, "Backup failed")
		clusterBackup.Status.Phase = "Failed"
		clusterBackup.Status.Message = fmt.Sprintf("Backup failed: %v", err)
		now := metav1.Now()
		clusterBackup.Status.CompletionTime = &now
		backup.SetCondition(&clusterBackup.Status.Conditions, "Ready", metav1.ConditionFalse, "BackupFailed", err.Error())

		if statusErr := r.Status().Update(ctx, clusterBackup); statusErr != nil {
			log.Error(statusErr, "Failed to update status after backup failure")
		}
		return ctrl.Result{}, err
	}

	// Update status with success
	clusterBackup.Status.Phase = "Completed"
	clusterBackup.Status.ResourceCount = result.ResourceCount
	clusterBackup.Status.BackupLocation = result.FilePath
	clusterBackup.Status.Message = fmt.Sprintf("Successfully backed up %d resources", result.ResourceCount)
	now := metav1.Now()
	clusterBackup.Status.CompletionTime = &now
	clusterBackup.Status.LastBackupTime = &now
	backup.SetCondition(&clusterBackup.Status.Conditions, "Ready", metav1.ConditionTrue, "BackupCompleted", "Backup completed successfully")

	if err := r.Status().Update(ctx, clusterBackup); err != nil {
		log.Error(err, "Failed to update status after successful backup")
		return ctrl.Result{}, err
	}

	log.Info("Backup completed successfully", "resourceCount", result.ResourceCount, "location", result.FilePath)

	// Run retention cleanup if configured
	if clusterBackup.Spec.RetentionDays != nil || clusterBackup.Spec.MaxArchives != nil {
		if err := r.BackupManager.CleanupArchives(clusterBackup.Spec.StoragePath, clusterBackup.Spec.RetentionDays, clusterBackup.Spec.MaxArchives); err != nil {
			log.Error(err, "Failed to cleanup old archives")
		}
	}

	// If there's a schedule, requeue for next run
	if clusterBackup.Spec.Schedule != "" {
		// Try to parse schedule as a duration (e.g., "24h"). If parsing fails, fallback to 1h requeue.
		if d, err := time.ParseDuration(clusterBackup.Spec.Schedule); err == nil {
			return ctrl.Result{RequeueAfter: d}, nil
		}
		// TODO: Implement proper cron scheduling
		return ctrl.Result{RequeueAfter: time.Hour}, nil
	}

	return ctrl.Result{}, nil
}

// performBackup executes the backup operation
func (r *ClusterBackupReconciler) performBackup(ctx context.Context, clusterBackup *backupv1alpha1.ClusterBackup) (*backup.BackupResult, error) {
	log := logf.FromContext(ctx)

	includeClusterResources := true
	if clusterBackup.Spec.IncludeClusterResources != nil {
		includeClusterResources = *clusterBackup.Spec.IncludeClusterResources
	}

	opts := backup.BackupOptions{
		IncludeNamespaces:       clusterBackup.Spec.IncludeNamespaces,
		ExcludeNamespaces:       clusterBackup.Spec.ExcludeNamespaces,
		IncludeClusterResources: includeClusterResources,
		ResourceTypes:           clusterBackup.Spec.ResourceTypes,
	}

	// If no specific resource types specified, use defaults
	if len(opts.ResourceTypes) == 0 {
		opts.ResourceTypes = backup.GetDefaultResourceTypes()
	}

	log.Info("Starting backup operation", "options", opts)

	return r.BackupManager.CreateBackup(ctx, clusterBackup.Spec.StoragePath, opts)
}

// handleDeletion handles cleanup when the ClusterBackup is being deleted
func (r *ClusterBackupReconciler) handleDeletion(ctx context.Context, clusterBackup *backupv1alpha1.ClusterBackup) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if controllerutil.ContainsFinalizer(clusterBackup, backupFinalizer) {
		// If configured, remove archives created by this ClusterBackup
		if clusterBackup.Spec.DeleteOnDelete != nil && *clusterBackup.Spec.DeleteOnDelete {
			log.Info("Deleting archives for ClusterBackup", "name", clusterBackup.Name, "storagePath", clusterBackup.Spec.StoragePath)
			// Attempt to delete all archives in the storage path by setting maxArchives=0
			zero := 0
			if err := r.BackupManager.CleanupArchives(clusterBackup.Spec.StoragePath, nil, &zero); err != nil {
				log.Error(err, "Failed to delete archives for ClusterBackup", "name", clusterBackup.Name)
			}
		}

		// Remove finalizer
		controllerutil.RemoveFinalizer(clusterBackup, backupFinalizer)
		if err := r.Update(ctx, clusterBackup); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&backupv1alpha1.ClusterBackup{}).
		Named("clusterbackup").
		Complete(r)
}
