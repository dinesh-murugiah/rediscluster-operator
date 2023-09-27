/*
Copyright 2023.

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

package redisclusterbackup

import (
	"context"
	"fmt"
	"time"

	batch "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	rediskunv1alpha1 "github.com/dinesh-murugiah/rediscluster-operator/api/v1alpha1"
	redisv1alpha1 "github.com/dinesh-murugiah/rediscluster-operator/api/v1alpha1"
	utils "github.com/dinesh-murugiah/rediscluster-operator/utils/commonutils"
	"github.com/dinesh-murugiah/rediscluster-operator/utils/k8sutil"
	"github.com/go-logr/logr"
	"github.com/robfig/cron"
	"github.com/spf13/pflag"
)

var (
	logl = log.Log.WithName("controller_redisclusterbackup")

	controllerFlagSet *pflag.FlagSet
	// maxConcurrentReconciles is the maximum number of concurrent Reconciles which can be run. Defaults to 1.
	maxConcurrentReconciles int

	pred predicate.Funcs

	jobPred predicate.Funcs
)

const backupFinalizer = "finalizer.backup.redis.kun"

func init() {
	controllerFlagSet = pflag.NewFlagSet("controller", pflag.ExitOnError)
	controllerFlagSet.IntVar(&maxConcurrentReconciles, "backupctr-maxconcurrent", 2, "the maximum number of concurrent Reconciles which can be run. Defaults to 1.")
}

func FlagSet() *pflag.FlagSet {
	return controllerFlagSet
}

// RedisClusterBackupReconciler reconciles a RedisClusterBackup object
type RedisClusterBackupReconciler struct {
	client.Client
	Scheme                *runtime.Scheme
	DirectClient          client.Client
	Recorder              record.EventRecorder
	CrController          k8sutil.ICustomResource
	StatefulSetController k8sutil.IStatefulSetControl
	JobController         k8sutil.IJobControl
}

func redisbackupcontrollerPredfunction() predicate.Funcs {

	pred = predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			// returns false if DistributedRedisCluster is ignored (not managed) by this operator.
			if !utils.ShoudManage(e.ObjectNew) {
				return false
			}
			logl.WithValues("namespace", e.ObjectNew.GetNamespace(), "name", e.ObjectNew.GetName()).V(5).Info("Call UpdateFunc")
			// Ignore updates to CR status in which case metadata.Generation does not change
			if e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration() {
				logl.WithValues("namespace", e.ObjectNew.GetNamespace(), "name", e.ObjectNew.GetName()).Info("Generation change return true",
					"old", e.ObjectOld, "new", e.ObjectNew)
				return true
			}
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			// returns false if DistributedRedisCluster is ignored (not managed) by this operator.
			if !utils.ShoudManage(e.Object) {
				return false
			}
			logl.WithValues("namespace", e.Object.GetNamespace(), "name", e.Object.GetName()).Info("Call DeleteFunc")
			// Evaluates to false if the object has been confirmed deleted.
			return !e.DeleteStateUnknown
		},
		CreateFunc: func(e event.CreateEvent) bool {
			// returns false if DistributedRedisCluster is ignored (not managed) by this operator.
			if !utils.ShoudManage(e.Object) {
				return false
			}
			logl.WithValues("namespace", e.Object.GetNamespace(), "name", e.Object.GetName()).Info("Call CreateFunc")
			return true
		},
	}
	return pred
}

func backupcontrollerjobPredfunction() predicate.Funcs {
	jobPred = predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			logl.WithValues("namespace", e.ObjectNew.GetNamespace(), "name", e.ObjectNew.GetName()).Info("Call Job UpdateFunc")
			if !utils.ShoudManage(e.ObjectNew) {
				logl.WithValues("namespace", e.ObjectNew.GetNamespace(), "name", e.ObjectNew.GetName()).Info("Job UpdateFunc Not Manage")
				return false
			}
			newObj := e.ObjectNew.(*batch.Job)
			if isJobCompleted(newObj) && newObj.DeletionTimestamp == nil {
				return true
			}
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			logl.WithValues("namespace", e.Object.GetNamespace(), "name", e.Object.GetName()).Info("Call Job Delete")
			if !utils.ShoudManage(e.Object) {
				return false
			}
			job, ok := e.Object.(*batch.Job)
			if !ok {
				logl.Error(nil, "Invalid Job object")
				return false
			}
			if job.Status.Succeeded == 0 && job.Status.Failed <= utils.Int32(job.Spec.BackoffLimit) {
				return false
			}
			return false
		},
		CreateFunc: func(e event.CreateEvent) bool {
			logl.WithValues("namespace", e.Object.GetNamespace(), "name", e.Object.GetName()).Info("Call Job CreateFunc")
			if !utils.ShoudManage(e.Object) {
				logl.WithValues("namespace", e.Object.GetNamespace(), "name", e.Object.GetName()).Info("Job CreateFunc Not Manage")
				return false
			}
			job := e.Object.(*batch.Job)
			if job.Status.Succeeded > 0 || job.Status.Failed >= utils.Int32(job.Spec.BackoffLimit) {
				return false
			}
			return false
		},
	}
	return jobPred
}

//+kubebuilder:rbac:groups=redis.kun,resources=redisclusterbackups,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=redis.kun,resources=redisclusterbackups/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=redis.kun,resources=redisclusterbackups/finalizers,verbs=update
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;create;update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the RedisClusterBackup object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile
func (r *RedisClusterBackupReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	reqLogger := logl.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	reqLogger.Info("Reconciling RedisClusterBackup")
	var sched cron.Schedule
	var cronEnabled bool = false
	var backupSchedTime time.Duration

	// Fetch the RedisClusterBackup instance
	instance := &redisv1alpha1.RedisClusterBackup{}
	err := r.Client.Get(context.TODO(), request.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	//// Check if the RedisClusterBackup instance is marked to be deleted, which is
	//// indicated by the deletion timestamp being set.
	//isBackupMarkedToBeDeleted := instance.GetDeletionTimestamp() != nil
	//if isBackupMarkedToBeDeleted {
	//	if contains(instance.GetFinalizers(), backupFinalizer) {
	//		// Run finalization logic for backupFinalizer. If the
	//		// finalization logic fails, don't remove the finalizer so
	//		// that we can retry during the next reconciliation.
	//		if err := r.finalizeBackup(reqLogger, instance); err != nil {
	//			return reconcile.Result{}, err
	//		}
	//
	//		// Remove backupFinalizer. Once all finalizers have been
	//		// removed, the object will be deleted.
	//		instance.SetFinalizers(remove(instance.GetFinalizers(), backupFinalizer))
	//		err := r.client.Update(context.TODO(), instance)
	//		if err != nil {
	//			return reconcile.Result{}, err
	//		}
	//	}
	//	return reconcile.Result{}, nil
	//}

	//// Add finalizer for this CR
	//if !contains(instance.GetFinalizers(), backupFinalizer) {
	//	if err := r.addFinalizer(reqLogger, instance); err != nil {
	//		return reconcile.Result{}, err
	//	}
	//}
	if instance.Spec.BackupCron && len(instance.Spec.BackupSchedule) != 0 {
		reqLogger.Info("Getting backup schedule info")
		sched, err = cron.ParseStandard(instance.Spec.BackupSchedule)
		if err != nil {
			reqLogger.Error(err, "Error Parsing cron schedule")
			return reconcile.Result{}, fmt.Errorf("unparaseable schedule %q: %v", instance.Spec.BackupSchedule, err)
		}
		cronEnabled = true
	}
	err, cronRetry := r.create(reqLogger, instance)
	if cronEnabled && cronRetry {
		backupSchedTime = (sched.Next(time.Now()).Sub(time.Now()))
		reqLogger.Info("cron Retry", "Backup Schedule time", backupSchedTime)
	}
	if cronRetry {
		if err != nil {
			return reconcile.Result{RequeueAfter: backupSchedTime}, err
		} else {
			return reconcile.Result{RequeueAfter: backupSchedTime}, nil
		}

	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RedisClusterBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&rediskunv1alpha1.RedisClusterBackup{}, builder.WithPredicates(redisbackupcontrollerPredfunction())).
		Owns(&batch.Job{}, builder.WithPredicates(backupcontrollerjobPredfunction())).
		WithOptions(controller.Options{MaxConcurrentReconciles: maxConcurrentReconciles}).
		Complete(r)
}

func (r *RedisClusterBackupReconciler) finalizeBackup(reqLogger logr.Logger, b *redisv1alpha1.RedisClusterBackup) error {
	// TODO(user): Add the cleanup steps that the operator
	// needs to do before the CR can be deleted. Examples
	// of finalizers include performing backups and deleting
	// resources that are not owned by this CR, like a PVC.
	reqLogger.Info("Successfully finalized RedisClusterBackup")
	return nil
}

func (r *RedisClusterBackupReconciler) addFinalizer(reqLogger logr.Logger, b *redisv1alpha1.RedisClusterBackup) error {
	reqLogger.Info("Adding Finalizer for the backup")
	b.SetFinalizers(append(b.GetFinalizers(), backupFinalizer))

	// Update CR
	err := r.Client.Update(context.TODO(), b)
	if err != nil {
		reqLogger.Error(err, "Failed to update RedisClusterBackup with finalizer")
		return err
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func remove(list []string, s string) []string {
	for i, v := range list {
		if v == s {
			list = append(list[:i], list[i+1:]...)
		}
	}
	return list
}

func isJobCompleted(newJob *batch.Job) bool {
	if isJobFinished(newJob) {
		logl.WithValues("Request.Namespace", newJob.Namespace).Info("JobFinished", "type", newJob.Status.Conditions[0].Type, "job", newJob.Name)
		return true
	}
	return false
}
