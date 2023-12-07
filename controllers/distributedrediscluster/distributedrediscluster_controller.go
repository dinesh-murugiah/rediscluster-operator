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

package distributedrediscluster

import (
	"context"
	"time"

	rediskunv1alpha1 "github.com/dinesh-murugiah/rediscluster-operator/api/v1alpha1"
	"github.com/dinesh-murugiah/rediscluster-operator/controllers/heal"
	clustermanger "github.com/dinesh-murugiah/rediscluster-operator/controllers/manager"
	config "github.com/dinesh-murugiah/rediscluster-operator/redisconfig"
	"github.com/dinesh-murugiah/rediscluster-operator/resources/configmaps"
	"github.com/dinesh-murugiah/rediscluster-operator/resources/statefulsets"
	utils "github.com/dinesh-murugiah/rediscluster-operator/utils/commonutils"
	"github.com/dinesh-murugiah/rediscluster-operator/utils/exec"
	"github.com/dinesh-murugiah/rediscluster-operator/utils/k8sutil"
	"github.com/dinesh-murugiah/rediscluster-operator/utils/redisutil"
	"github.com/go-logr/logr"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var (
	logl = log.Log.WithName("controller_distributedrediscluster")

	controllerFlagSet *pflag.FlagSet
	// maxConcurrentReconciles is the maximum number of concurrent Reconciles which can be run. Defaults to 4.
	maxConcurrentReconciles int
	// reconcileTime is the delay between reconciliations. Defaults to 60s.
	reconcileTime int

	pred predicate.Funcs
)

func init() {
	controllerFlagSet = pflag.NewFlagSet("controller", pflag.ExitOnError)
	controllerFlagSet.IntVar(&maxConcurrentReconciles, "ctr-maxconcurrent", 4, "the maximum number of concurrent Reconciles which can be run. Defaults to 4.")
	controllerFlagSet.IntVar(&reconcileTime, "ctr-reconciletime", 60, "")
}

func FlagSet() *pflag.FlagSet {
	return controllerFlagSet
}

// DistributedRedisClusterReconciler reconciles a DistributedRedisCluster object
type DistributedRedisClusterReconciler struct {
	client.Client
	Scheme                *runtime.Scheme
	Ensurer               clustermanger.IEnsureResource
	Checker               clustermanger.ICheck
	Execer                exec.IExec
	StatefulSetController k8sutil.IStatefulSetControl
	ServiceController     k8sutil.IServiceControl
	PdbController         k8sutil.IPodDisruptionBudgetControl
	PvcController         k8sutil.IPvcControl
	CrController          k8sutil.ICustomResource
	PodController         k8sutil.IPodControl
	NodeController        k8sutil.INodeControl
}

//+kubebuilder:rbac:groups=redis.kun,resources=distributedredisclusters,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=redis.kun,resources=distributedredisclusters/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=redis.kun,resources=distributedredisclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;create;update

func redisclustercontrollerPredfunction() predicate.Funcs {

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

// SetupWithManager sets up the controller with the Manager.
func (r *DistributedRedisClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&rediskunv1alpha1.DistributedRedisCluster{}, builder.WithPredicates(redisclustercontrollerPredfunction())).
		WithOptions(controller.Options{MaxConcurrentReconciles: maxConcurrentReconciles}).
		Complete(r)
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the DistributedRedisCluster object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile
func (r *DistributedRedisClusterReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	reqLogger := logl.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	reqLogger.Info("Reconciling DistributedRedisCluster")

	// Fetch the DistributedRedisCluster instance
	instance := &rediskunv1alpha1.DistributedRedisCluster{}
	err := r.Client.Get(context.TODO(), request.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	syncCtx := &syncContext{
		cluster:   instance,
		reqLogger: reqLogger,
	}
	var isStsCreated bool = false
	isStsCreated, err = r.ensureCluster(syncCtx)
	if err != nil {
		switch GetType(err) {
		case StopRetry:
			reqLogger.Info("invalid", "err", err)
			return reconcile.Result{}, nil
		}
		reqLogger.WithValues("err", err).Info("ensureCluster")
		newStatus := instance.Status.DeepCopy()
		if instance.Status.HAStatus == "" || isStsCreated {
			SetHAStatus(newStatus, rediskunv1alpha1.HaStatusCreating)
		}
		SetClusterScaling(newStatus, err.Error())
		r.updateClusterIfNeed(instance, newStatus, reqLogger)
		return reconcile.Result{RequeueAfter: requeueAfter}, nil
	} else {
		var newStatus *rediskunv1alpha1.DistributedRedisClusterStatus
		newStatus = instance.Status.DeepCopy()
		if instance.Status.SecretStatus == "" {
			secretVersions, err := configmaps.Getsecretversions(reqLogger, r.Client, instance)
			if err != nil {
				SetSecretStatus(newStatus, "aclconferror")
			} else {
				SetSecretStatus(newStatus, "aclconfdone")
				newStatus.SecretsVer = make(map[string]string)
				for key, value := range secretVersions {
					newStatus.SecretsVer[key] = value
				}
			}
		}
		if instance.Status.HAStatus == "" || isStsCreated {
			SetHAStatus(newStatus, "hacreating")
		}
		r.updateClusterIfNeed(instance, newStatus, reqLogger)
	}

	matchLabels := getLabels(instance)
	redisClusterPods, err := r.StatefulSetController.GetStatefulSetPodsByLabels(instance.Namespace, matchLabels)
	if err != nil {
		return reconcile.Result{}, Kubernetes.Wrap(err, "GetStatefulSetPods")
	}

	syncCtx.pods = clusterPods(redisClusterPods.Items)
	reqLogger.V(6).Info("debug cluster pods", "", syncCtx.pods)
	syncCtx.healer = clustermanger.NewHealer(&heal.CheckAndHeal{
		Logger:     reqLogger,
		PodControl: k8sutil.NewPodController(r.Client),
		Pods:       syncCtx.pods,
		DryRun:     false,
	})
	err = r.waitPodReady(syncCtx)
	if err != nil {
		switch GetType(err) {
		case Kubernetes:
			return reconcile.Result{}, err
		}
		reqLogger.WithValues("err", err).Info("waitPodReady")
		newStatus := instance.Status.DeepCopy()
		SetClusterScaling(newStatus, err.Error())
		r.updateClusterIfNeed(instance, newStatus, reqLogger)
		return reconcile.Result{RequeueAfter: requeueAfter}, nil
	}

	password, err := statefulsets.GetRedisClusterAdminPassword(r.Client, syncCtx.cluster, reqLogger)
	if err != nil {
		return reconcile.Result{}, Kubernetes.Wrap(err, "getClusterPassword")
	}

	context := context.Background()

	admin, err := newRedisAdmin(syncCtx.pods, password, config.RedisConf(), reqLogger, context)
	if err != nil {
		return reconcile.Result{}, Redis.Wrap(err, "newRedisAdmin")
	}
	defer admin.Close(context)

	secretver, err := r.checkandUpdatePassword(admin, syncCtx, context)
	newsecretStatus := instance.Status.DeepCopy()
	if err != nil {
		reqLogger.WithValues("err", err).Info("error checking / updating password")
		SetSecretStatus(newsecretStatus, "ACLupdateERR")
	}
	if secretver != nil {
		SetSecretStatus(newsecretStatus, "ACLupdateSuccess")
		newsecretStatus.SecretsVer = make(map[string]string)
		for key, value := range secretver {
			newsecretStatus.SecretsVer[key] = value
		}
	} else {
		if err == nil {
			SetSecretStatus(newsecretStatus, "ACLintact")
		}
	}
	r.updateClusterIfNeed(instance, newsecretStatus, reqLogger)
	//reqLogger.WithValues("adminpassword:", password).Info("please check adminpass")
	// Setting this context for request flow

	clusterInfos, err := admin.GetClusterInfos(context)

	if err != nil {
		if clusterInfos.Status == redisutil.ClusterInfosPartial {
			return reconcile.Result{}, Redis.Wrap(err, "GetClusterInfos")
		}
	}

	requeue, err := syncCtx.healer.Heal(instance, clusterInfos, admin, context)
	if err != nil {
		return reconcile.Result{}, Redis.Wrap(err, "Heal")
	}
	if requeue {
		return reconcile.Result{RequeueAfter: requeueAfter}, nil
	}

	syncCtx.admin = admin
	syncCtx.clusterInfos = clusterInfos
	err = r.waitForClusterJoin(syncCtx, context)
	if err != nil {
		switch GetType(err) {
		case Requeue:
			reqLogger.WithValues("err", err).Info("requeue")
			return reconcile.Result{RequeueAfter: requeueAfter}, nil
		}
		newStatus := instance.Status.DeepCopy()
		SetClusterFailed(newStatus, err.Error())
		r.updateClusterIfNeed(instance, newStatus, reqLogger)
		return reconcile.Result{}, err
	}

	// mark .Status.Restore.Phase = RestorePhaseRestart, will
	// remove init container and restore volume that referenced in stateulset for
	// dump RDB file from backup, then the redis master node will be restart.
	if instance.IsRestoreFromBackup() && instance.IsRestoreRunning() {
		reqLogger.Info("update restore redis cluster cr")
		instance.Status.Restore.Phase = rediskunv1alpha1.RestorePhaseRestart
		if err := r.CrController.UpdateCRStatus(instance); err != nil {
			return reconcile.Result{}, err
		}
		if err := r.Ensurer.UpdateRedisStatefulsets(instance, getLabels(instance)); err != nil {
			return reconcile.Result{}, err
		}
		waiter := &waitStatefulSetUpdating{
			name:                  "waitMasterNodeRestarting",
			timeout:               60 * time.Second,
			tick:                  5 * time.Second,
			statefulSetController: r.StatefulSetController,
			cluster:               instance,
		}
		if err := waiting(waiter, syncCtx.reqLogger); err != nil {
			return reconcile.Result{}, err
		}
		return reconcile.Result{Requeue: true}, nil
	}

	// restore succeeded, then update cr and wait for the next Reconcile loop
	if instance.IsRestoreFromBackup() && instance.IsRestoreRestarting() {
		reqLogger.Info("update restore redis cluster cr")
		instance.Status.Restore.Phase = rediskunv1alpha1.RestorePhaseSucceeded
		if err := r.CrController.UpdateCRStatus(instance); err != nil {
			return reconcile.Result{}, err
		}
		// set ClusterReplicas = Backup.Status.ClusterReplicas,
		// next Reconcile loop the statefulSet's replicas will increase by ClusterReplicas, then start the slave node
		//Dinesh Todo This flow Needs FIX ####, commented out below to fix status fileds
		/*
			instance.Spec.ClusterReplicas = instance.Status.Restore.Backup.Status.ClusterReplicas
			if err := r.crController.UpdateCR(instance); err != nil {
				return reconcile.Result{}, err
			}
		*/
		return reconcile.Result{}, nil
	}

	if err := admin.SetConfigIfNeed(context, instance.Spec.Config); err != nil {
		return reconcile.Result{}, Redis.Wrap(err, "SetConfigIfNeed")
	}

	// Un optimial calling of buildClusterStatus , Donot remove this from here as its used for cluster creation process
	status := buildClusterStatus(clusterInfos, syncCtx.pods, instance, reqLogger, r.Client, true)
	if is := r.isScalingDown(instance, reqLogger); is {
		SetClusterRebalancing(status, "scaling down")
	}
	reqLogger.V(4).Info("buildClusterStatus", "status", status)
	r.updateClusterIfNeed(instance, status, reqLogger)

	instance.Status = *status
	if needClusterOperation(instance, reqLogger) {
		reqLogger.Info(">>>>>> clustering")
		err = r.syncCluster(syncCtx, context)
		if err != nil {
			newStatus := instance.Status.DeepCopy()
			SetClusterFailed(newStatus, err.Error())
			r.updateClusterIfNeed(instance, newStatus, reqLogger)
			return reconcile.Result{}, err
		}
	}

	newClusterInfos, err := admin.GetClusterInfos(context)
	if err != nil {
		if clusterInfos.Status == redisutil.ClusterInfosPartial {
			return reconcile.Result{}, Redis.Wrap(err, "GetClusterInfos")
		} else if clusterInfos.Status == redisutil.ClusterInfosInconsistent {
			reqLogger.Info("after syncCluster clusterInfos inconsistent resync after 10 seconds")
			newStatus := instance.Status.DeepCopy()
			SetClusterScaling(newStatus, err.Error())
			r.updateClusterIfNeed(instance, newStatus, reqLogger)
			return reconcile.Result{RequeueAfter: 10 * time.Second}, nil
		}
	}
	newStatus := buildClusterStatus(newClusterInfos, syncCtx.pods, instance, reqLogger, r.Client, true)
	haStatus, _ := buildHAStatus(newClusterInfos, syncCtx.pods, instance, reqLogger, r.Client)
	SetHAStatus(newStatus, haStatus)
	SetClusterOK(newStatus, "OK")
	r.updateClusterIfNeed(instance, newStatus, reqLogger)

	requeue, err = r.checkandUpdatePods(admin, syncCtx, context)
	if err != nil {
		newStatus := instance.Status.DeepCopy()
		SetClusterFailed(newStatus, err.Error())
		r.updateClusterIfNeed(instance, newStatus, reqLogger)
		return reconcile.Result{}, err
	}

	if requeue {
		return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
	}

	return reconcile.Result{RequeueAfter: time.Duration(reconcileTime) * time.Second}, nil
}

func (r *DistributedRedisClusterReconciler) isScalingDown(cluster *rediskunv1alpha1.DistributedRedisCluster, reqLogger logr.Logger) bool {
	stsList, err := r.StatefulSetController.ListStatefulSetByLabels(cluster.Namespace, getLabels(cluster))
	if err != nil {
		reqLogger.Error(err, "ListStatefulSetByLabels")
		return false
	}
	if len(stsList.Items) > int(cluster.Spec.MasterSize) {
		return true
	}
	return false
}
