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
	"fmt"
	"sort"
	"time"

	rediskunv1alpha1 "github.com/dinesh-murugiah/rediscluster-operator/api/v1alpha1"
	"github.com/dinesh-murugiah/rediscluster-operator/controllers/heal"
	clustermanger "github.com/dinesh-murugiah/rediscluster-operator/controllers/manager"

	//"github.com/dinesh-murugiah/rediscluster-operator/metrics"
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
	//MetricClient          metrics.Recorder
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
		//var newStatus *rediskunv1alpha1.DistributedRedisClusterStatus
		newStatus := instance.Status.DeepCopy()
		if instance.Status.SecretStatus == "" {
			secretVersions, err := configmaps.Getsecretversions(reqLogger, r.Client, instance)
			if err != nil {
				//metric here
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
		//metric here
		return reconcile.Result{}, Kubernetes.Wrap(err, "getClusterPassword")
	}

	context := context.Background()

	admin, err := newRedisAdmin(syncCtx.pods, password, config.RedisConf(), reqLogger, context)
	if err != nil {
		//metric here
		return reconcile.Result{}, Redis.Wrap(err, "newRedisAdmin")
	}
	defer admin.Close(context)

	secretver, err := r.checkandUpdatePassword(admin, syncCtx, context)
	newsecretStatus := instance.Status.DeepCopy()
	if err != nil {
		//metric here
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
	/*
		if clusterInfos.ClusterState == "NOK" && len(clusterInfos.ClusterNokReason) > 0 {
			for key, value := range clusterInfos.ClusterNokReason {
				reqLogger.Info("clusterInfos - cluster not healthy", "NodeIP", key, "Reason", value)
			}
		}
	*/

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
		reqLogger.Info("GetClusterInfos", "clusterInfos.Status", clusterInfos.Status)
		if clusterInfos.Status == redisutil.ClusterInfosPartial {
			return reconcile.Result{RequeueAfter: 10 * time.Second}, nil
		}
	}
	newStatus := buildClusterStatus(newClusterInfos, syncCtx.pods, instance, reqLogger, r.Client, true)
	haStatus, _, _ := buildHAStatus(newClusterInfos, syncCtx.pods, instance, reqLogger, r.Client)
	SetHAStatus(newStatus, haStatus)
	clusterHealthOk := CheackandUpdateClusterHealth(newStatus, newClusterInfos, instance, reqLogger)
	r.updateClusterIfNeed(instance, newStatus, reqLogger)
	// This part of code is very un optimally called , First need to optimise checkandUpdatePods, the above
	// buildClusterStatus has to be called only once , that data needs to be used for calling both checkandUpdatePods and updateLabels
	// and cluster Haheal
	if clusterHealthOk {
		requeue, err = r.checkandUpdatePods(admin, syncCtx, context)
		if err != nil {
			newStatus := instance.Status.DeepCopy()
			SetClusterFailed(newStatus, err.Error())
			r.updateClusterIfNeed(instance, newStatus, reqLogger)
			return reconcile.Result{RequeueAfter: time.Duration(reconcileTime) * time.Second}, nil
		}

		if requeue {
			if newStatus.Status != rediskunv1alpha1.ClusterStatusOnDelete {
				newStatusUpadte := instance.Status.DeepCopy()
				SetClusterUpdating(newStatusUpadte, "Cluster Upgrade in Progress")
				r.updateClusterIfNeed(instance, newStatusUpadte, reqLogger)
			}
			return reconcile.Result{RequeueAfter: 10 * time.Second}, nil
		}
	} else {
		reqLogger.Info("Cluster Health", "NOK", newStatus.Reason)
		return reconcile.Result{RequeueAfter: 20 * time.Second}, nil
	}

	// Ha Healing functionality
	// 1. First check the Ha Status to see if ha needs healing
	//2. check if annotations for ha healing is present and is set to true
	//3. check if all availability zones are present
	// 4. if all zone are  present then, we need to heal the cluster for master and node distrubution
	//5. if all zones are not present then leave it for zone failure condition to recover

	if clusterHealthOk && haStatus == rediskunv1alpha1.HaStatusFailed {
		err := fmt.Errorf("unexpected Error - HaStatusFailed")
		reqLogger.Error(err, "HaStatusFailed")
		return reconcile.Result{RequeueAfter: time.Duration(reconcileTime) * time.Second}, nil
		// 2. If ha needs healing , then check if the cluster is in a stable state
	} else if clusterHealthOk && haStatus == rediskunv1alpha1.HaStatusRedistribute {
		//check if all Zones active now
		isAllactive, _, err := CheckallZonesActive(instance, reqLogger, r.Client)
		if err != nil {
			reqLogger.Error(err, "CheckallZonesActive")
			return reconcile.Result{RequeueAfter: 20 * time.Second}, nil
		}

		// Checking if annotation "reDistributeNode" is true only then it should proceed for nodes rebalancing
		redisClusterCRAnnotations := instance.Annotations
		reDitributeNodes := false
		for key, val := range redisClusterCRAnnotations {
			if key == "reDistributeNode" {
				if val == "true" {
					reDitributeNodes = true
				}
			}
		}

		// 3. If all zones not active , then check if we need to add annotations to not evict master pods
		// Inorder for self heal to occur, set reDitributeNodes=true as annotation in respective cluster CRD.
		if isAllactive && reDitributeNodes {
			matchLabels := getLabels(instance)
			redisClusterPods, err := r.StatefulSetController.GetStatefulSetPodsByLabels(instance.Namespace, matchLabels)
			if err != nil {
				reqLogger.Error(err, "HaRedistribute - GetStatefulSetPodsByLabels")
				return reconcile.Result{RequeueAfter: time.Duration(reconcileTime) * time.Second}, nil
			}
			pods := clusterPods(redisClusterPods.Items)
			numofNodestoDel, delNodes, err1 := r.getListofNodestoDelete(instance, newClusterInfos, reqLogger)
			if err1 != nil {
				reqLogger.Error(err1, "HaRedistribute - getListofNodestoDelete")
				return reconcile.Result{RequeueAfter: time.Duration(reconcileTime) * time.Second}, nil

			}
			if numofNodestoDel > 0 {
				Podclient := k8sutil.NewPodController(r.Client)
				for i := 0; i < int(instance.Spec.MasterSize); i++ {
					stsname := statefulsets.ClusterStatefulSetName(instance.Name, i)
					if _, ok := delNodes[stsname]; ok {
						for _, delRedisNode := range delNodes[stsname] {
							reqLogger.Info("Calling Delete Pod", "PodName", delRedisNode.PodName, "stsname", stsname, "NodeName", delRedisNode.NodeName, "Zone", delRedisNode.Zonename, "Role", delRedisNode.Role, "IP", delRedisNode.IP, "StatefulSet", delRedisNode.StatefulSet)
							err2 := Podclient.DeletePodByName(instance.Namespace, delRedisNode.PodName)
							if err2 != nil {
								reqLogger.Error(err2, "HaRedistribute - DeletePodByName")
								return reconcile.Result{RequeueAfter: time.Duration(reconcileTime) * time.Second}, nil
							}
						}
					}
				}
				return reconcile.Result{RequeueAfter: time.Duration(reconcileTime) * time.Second}, nil
			}
			//check if any masters needs to be failedover
			haStatus, displacedMasters, _ := buildHAStatus(newClusterInfos, pods, instance, reqLogger, r.Client)
			if haStatus == rediskunv1alpha1.HaStatusFailed {
				reqLogger.Error(fmt.Errorf("unexpected Error - HaStatusFailed"), "HaStatusFailed When trying to Distribute Masters")
			} else if haStatus == rediskunv1alpha1.HaStatusRedistribute && len(displacedMasters) > 0 {
				reqLogger.Info("HaStatusRedistribute - Distribute Masters", "displacedMasters", displacedMasters)
				//Distribute Masters
				err := r.PlaceMastersbySTS(instance, displacedMasters, newClusterInfos, admin, context, reqLogger)
				if err != nil {
					reqLogger.Error(err, "HaStatusRedistribute - PlaceMastersbySTS")
				}
				reqLogger.Info("HaStatusHealthy - HaHealing Completed")
				//Update the annotation reDistributeNode to false in cluster CRD to stop selfheal.
				//Selfheal should occur only if human operator sets annotation reDistributeNode as true on cluster crd.
				//Once selfheal is completed, setting this annotation to false to stop auto selfheal as it is doesn't consider production traffic.
				if err := r.CrController.UpdateCrdAnnotations(instance, "reDistributeNode", "false"); err != nil {
					return reconcile.Result{}, err
				}
				return reconcile.Result{RequeueAfter: time.Duration(reconcileTime) * time.Second}, nil
			} else if haStatus == rediskunv1alpha1.HaStatusHealthy && len(displacedMasters) == 0 {
				newStatus := instance.Status.DeepCopy()
				SetHAStatus(newStatus, rediskunv1alpha1.HaStatusHealthy)
				r.updateClusterIfNeed(instance, newStatus, reqLogger)
				return reconcile.Result{RequeueAfter: time.Duration(reconcileTime) * time.Second}, nil
			}
		} else {
			reqLogger.Info("HaStatusRedistribute - Not all zones active or Healing annotation reDistributeNode is unset to true!")
			return reconcile.Result{RequeueAfter: time.Duration(reconcileTime) * time.Second}, nil

		}
	} else {
		reqLogger.Info("HAhealing not required", "haStatus", haStatus)
	}
	//r.MetricClient.SetClusterOK(instance.Namespace, instance.Name)
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

func (r *DistributedRedisClusterReconciler) getListofNodestoDelete(cluster *rediskunv1alpha1.DistributedRedisCluster, clusterinfos *redisutil.ClusterInfos, reqLogger logr.Logger) (int32, map[string][]*redisutil.Node, error) {
	var nodesToDelete map[string][]*redisutil.Node = make(map[string][]*redisutil.Node)
	var nodesPerZonePerSTS map[string]map[string][]*redisutil.Node = make(map[string]map[string][]*redisutil.Node)
	var masterNodesPerZone, slaveNodesPerZone int32
	var minNodesExpectedPerZone int32 = 0
	var maxNodesExpectedPerZone int32 = 0
	var numNodesToDelete int32 = 0

	zones := make([]string, 0, len(cluster.Spec.HaConfig.ZonesInfo))
	for zone := range cluster.Spec.HaConfig.ZonesInfo {
		zones = append(zones, zone)
	}
	sort.Strings(zones)

	minNodesExpectedPerZone = (1 + cluster.Spec.ClusterReplicas) / int32(len(zones))
	maxNodesExpectedPerZone = ((1 + cluster.Spec.ClusterReplicas) + (int32(len(zones) - 1))) / int32(len(zones))

	reqLogger.Info("getListofNodestoDelete", "minNodesExpectedPerZone", minNodesExpectedPerZone)
	reqLogger.Info("getListofNodestoDelete", "maxNodesExpectedPerZone", maxNodesExpectedPerZone)

	for i := 0; i < int(cluster.Spec.MasterSize); i++ {
		stsname := statefulsets.ClusterStatefulSetName(cluster.Name, i)
		stsNodes, err := clusterinfos.GetNodes().GetNodesByFunc(func(node *redisutil.Node) bool {
			return node.StatefulSet == stsname
		})
		if err != nil {
			return 0, nil, err
		}
		if stsNodes == nil || len(stsNodes) < int(cluster.Spec.ClusterReplicas+1) {
			return 0, nil, fmt.Errorf("invalid number of nodes in sts %s", stsname)
		}
		for _, zone := range zones {
			if _, ok := nodesPerZonePerSTS[zone]; !ok {
				nodesPerZonePerSTS[zone] = make(map[string][]*redisutil.Node)
			}
			if _, ok := nodesPerZonePerSTS[zone][stsname]; !ok {
				nodesPerZonePerSTS[zone][stsname] = make([]*redisutil.Node, 0)
			}

			masterNodesPerZone, slaveNodesPerZone = 0, 0
			for _, node := range stsNodes {
				if node.Zonename == zone {
					if node.Role == redisutil.RedisMasterRole {
						masterNodesPerZone++
						nodesPerZonePerSTS[zone][stsname] = append(nodesPerZonePerSTS[zone][stsname], node)
					} else if node.Role == redisutil.RedisSlaveRole {
						slaveNodesPerZone++
						nodesPerZonePerSTS[zone][stsname] = append(nodesPerZonePerSTS[zone][stsname], node)
					} else {
						return 0, nil, fmt.Errorf("invalid Role %s", node.Role)
					}
				}
			}
			reqLogger.Info("Nodes in zone", "zone", zone, "masterNodesPerZone", masterNodesPerZone, "slaveNodesPerZone", slaveNodesPerZone, "stsname", stsname)
			if _, nodesExists := nodesPerZonePerSTS[zone][stsname]; !nodesExists || len(nodesPerZonePerSTS[zone][stsname]) == 0 {
				reqLogger.Info("No nodes in zone", "zone", zone, "stsname", stsname)
				numNodesToDelete = numNodesToDelete + minNodesExpectedPerZone
			} else {
				if len(nodesPerZonePerSTS[zone][stsname]) < int(minNodesExpectedPerZone) {
					reqLogger.Info("Less nodes in zone", "zone", zone, "nodes", len(nodesPerZonePerSTS[zone][stsname]), "stsname", stsname)
					numNodesToDelete = numNodesToDelete + (minNodesExpectedPerZone - int32(len(nodesPerZonePerSTS[zone][stsname])))
				}
			}
		}
	}
	TempNumNodesToDelete := numNodesToDelete
	if TempNumNodesToDelete > 0 {
		reqLogger.Info("getListofNodestoDelete", "numNodesToDelete", numNodesToDelete)
		for i := 0; i < int(cluster.Spec.MasterSize); i++ {
			stsname := statefulsets.ClusterStatefulSetName(cluster.Name, i)
			for _, zone := range zones {

				if _, nodesExists := nodesPerZonePerSTS[zone][stsname]; nodesExists && len(nodesPerZonePerSTS[zone][stsname]) > int(maxNodesExpectedPerZone) {
					reqLogger.Info("More nodes in zone", "zone", zone, "nodes", len(nodesPerZonePerSTS[zone][stsname]), "stsname", stsname)
					for _, node := range nodesPerZonePerSTS[zone][stsname] {
						if node.Role == redisutil.RedisSlaveRole {
							nodesToDelete[node.StatefulSet] = append(nodesToDelete[zone], node)
							reqLogger.Info("Node to delete", "nodeID", node.ID, "nodeIP", node.IP, "nodeRole", node.Role, "nodeStatefulSet", node.StatefulSet, "nodeZone", node.Zonename, "nodePodName", node.PodName, "nodeNodeName", node.NodeName)
							TempNumNodesToDelete--
						}
					}
				}
			}
		}
		if TempNumNodesToDelete == 0 {
			reqLogger.Info("numNodesToDelete Identification Completed", "numNodesToDelete", numNodesToDelete)
			return numNodesToDelete, nodesToDelete, nil
		}
	} else if TempNumNodesToDelete == 0 {
		reqLogger.Info("No nodes to delete")
		return 0, nil, nil
	}
	reqLogger.Info("Delete node identification Unsucefull", "number of nodes to delete", TempNumNodesToDelete)
	return 0, nil, fmt.Errorf("unsucess/Invalid number of nodes to delete")

}

func (r *DistributedRedisClusterReconciler) PlaceMastersbySTS(cluster *rediskunv1alpha1.DistributedRedisCluster, displacedMasters redisutil.Nodes, clusterinfos *redisutil.ClusterInfos, admin redisutil.IAdmin, context context.Context, reqLogger logr.Logger) error {

	zones := make([]string, 0, len(cluster.Spec.HaConfig.ZonesInfo))
	for zone := range cluster.Spec.HaConfig.ZonesInfo {
		zones = append(zones, zone)
	}
	sort.Strings(zones)
	for _, node := range displacedMasters {
		stsname := node.StatefulSet
		stsNodes, err := clusterinfos.GetNodes().GetNodesByFunc(func(node *redisutil.Node) bool {
			return node.StatefulSet == stsname
		})
		if err != nil {
			return err
		}
		if stsNodes == nil || len(stsNodes) < int(cluster.Spec.ClusterReplicas+1) {
			return fmt.Errorf("invalid number of nodes in sts %s", stsname)
		}
		stsindex, err := getSTSindex(node.StatefulSet)
		if err != nil {
			reqLogger.Error(err, fmt.Sprintf("unable to retrieve STS index  %s", node.StatefulSet))
			return err
		}

		zoneoffset := (stsindex % len(cluster.Spec.HaConfig.ZonesInfo))
		zonename := zones[zoneoffset]
		for _, node := range stsNodes {
			if node.Zonename == zonename {
				if node.Role == redisutil.RedisMasterRole {
					reqLogger.Error(fmt.Errorf("possible Multimaster"), "Node already master in appropriate zone Possibility of Multimaster", "nodeID", node.ID, "nodeIP", node.IP, "nodeRole", node.Role, "nodeStatefulSet", node.StatefulSet, "nodeZone", node.Zonename, "nodePodName", node.PodName, "nodeNodeName", node.NodeName)
					// Possibility of Multimaster scenario Manual operator involment needed
				} else if node.Role == redisutil.RedisSlaveRole {
					reqLogger.Info("Node to be promoted to master", "nodeID", node.ID, "nodeIP", node.IP, "nodeRole", node.Role, "nodeStatefulSet", node.StatefulSet, "nodeZone", node.Zonename, "nodePodName", node.PodName, "nodeNodeName", node.NodeName)
					//promote node to master
					err := admin.FailoverSlaveToMaster(context, node, redisutil.RedisFailoverDefault)
					if err != nil {
						reqLogger.Error(err, "FailoverSlaveToMaster Failed")
						return err
					}
				} else {
					return fmt.Errorf("invalid Role %s", node.Role)
				}
			}
		}
	}

	return nil
}
