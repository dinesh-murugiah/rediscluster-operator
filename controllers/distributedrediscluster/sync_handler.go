package distributedrediscluster

import (
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	redisv1alpha1 "github.com/dinesh-murugiah/rediscluster-operator/api/v1alpha1"
	"github.com/dinesh-murugiah/rediscluster-operator/controllers/clustering"
	"github.com/dinesh-murugiah/rediscluster-operator/controllers/manager"
	config "github.com/dinesh-murugiah/rediscluster-operator/redisconfig"
	"github.com/dinesh-murugiah/rediscluster-operator/resources/statefulsets"
	"github.com/dinesh-murugiah/rediscluster-operator/utils/k8sutil"
	"github.com/dinesh-murugiah/rediscluster-operator/utils/redisutil"
)

const (
	requeueAfter = 10 * time.Second
)

type syncContext struct {
	cluster      *redisv1alpha1.DistributedRedisCluster
	clusterInfos *redisutil.ClusterInfos
	admin        redisutil.IAdmin
	healer       manager.IHeal
	pods         []*corev1.Pod
	reqLogger    logr.Logger
}

func (r *DistributedRedisClusterReconciler) ensureCluster(ctx *syncContext) error {
	cluster := ctx.cluster
	if err := r.validateAndSetDefault(cluster, ctx.reqLogger); err != nil {
		if k8sutil.IsRequestRetryable(err) {
			return Kubernetes.Wrap(err, "Validate")
		}
		return StopRetry.Wrap(err, "stop retry")
	}

	// Redis only load db from append only file when AOF ON, because of
	// we only backed up the RDB file when doing data backup, so we set
	// "appendonly no" force here when do restore.
	dbLoadedFromDiskWhenRestore(cluster, ctx.reqLogger)
	labels := getLabels(cluster)
	if err, crdeferr := r.Ensurer.EnsureRedisConfigMap(cluster, labels); err != nil {
		if crdeferr {
			return StopRetry.Wrap(err, "AdminSecretMissing")
		} else {
			return Kubernetes.Wrap(err, "EnsureRedisConfigMap")
		}
	}

	if updated, err := r.Ensurer.EnsureRedisStatefulsets(cluster, labels); err != nil {
		ctx.reqLogger.Error(err, "EnsureRedisStatefulSets")
		return Kubernetes.Wrap(err, "EnsureRedisStatefulSets")
	} else if updated {
		// update cluster status = RollingUpdate immediately when cluster's image or resource or password changed
		SetClusterUpdating(&cluster.Status, "cluster spec updated")
		r.CrController.UpdateCRStatus(cluster)
		waiter := &waitStatefulSetUpdating{
			name:                  "waitStatefulSetUpdating",
			timeout:               30 * time.Second * time.Duration(cluster.Spec.ClusterReplicas+2),
			tick:                  5 * time.Second,
			statefulSetController: r.StatefulSetController,
			cluster:               cluster,
		}
		if err := waiting(waiter, ctx.reqLogger); err != nil {
			return err
		}
	}
	if err := r.Ensurer.EnsureRedisHeadLessSvcs(cluster, labels); err != nil {
		return Kubernetes.Wrap(err, "EnsureRedisHeadLessSvcs")
	}
	if err := r.Ensurer.EnsureRedisSvc(cluster, labels); err != nil {
		return Kubernetes.Wrap(err, "EnsureRedisSvc")
	}
	if err := r.Ensurer.EnsureRedisRCloneSecret(cluster, labels); err != nil {
		if k8sutil.IsRequestRetryable(err) {
			return Kubernetes.Wrap(err, "EnsureRedisRCloneSecret")
		}
		return StopRetry.Wrap(err, "stop retry")
	}
	return nil
}

func (r *DistributedRedisClusterReconciler) waitPodReady(ctx *syncContext) error {
	if _, err := ctx.healer.FixTerminatingPods(ctx.cluster, 5*time.Minute); err != nil {
		return Kubernetes.Wrap(err, "FixTerminatingPods")
	}
	if err := r.Checker.CheckRedisNodeNum(ctx.cluster); err != nil {
		return Requeue.Wrap(err, "CheckRedisNodeNum")
	}

	return nil
}

func (r *DistributedRedisClusterReconciler) validateAndSetDefault(cluster *redisv1alpha1.DistributedRedisCluster, reqLogger logr.Logger) error {
	var update bool
	var err error

	if cluster.IsRestoreFromBackup() && cluster.ShouldInitRestorePhase() {
		//Dinesh Todo This flow Needs FIX ####, commented out below to fix status fileds
		err = fmt.Errorf("Restore Not supported yet")
		return err
		/*
			reqLogger.Error("force appendonly = no when do restore")
			update, err = r.initRestore(cluster, reqLogger)
			if err != nil {
				return err
			}
		*/
	}

	if cluster.IsRestoreFromBackup() && (cluster.IsRestoreRunning() || cluster.IsRestoreRestarting()) {
		// Set ClusterReplicas = 0, only start master node in first reconcile loop when do restore
		cluster.Spec.ClusterReplicas = 0
	}

	updateDefault := cluster.DefaultSpec(reqLogger)
	if update || updateDefault {
		return r.CrController.UpdateCR(cluster)
	}

	return nil
}

func dbLoadedFromDiskWhenRestore(cluster *redisv1alpha1.DistributedRedisCluster, reqLogger logr.Logger) {
	if cluster.IsRestoreFromBackup() && !cluster.IsRestored() {
		if cluster.Spec.Config != nil {
			reqLogger.Info("force appendonly = no when do restore")
			cluster.Spec.Config["appendonly"] = "no"
		}
	}
}

func (r *DistributedRedisClusterReconciler) initRestore(cluster *redisv1alpha1.DistributedRedisCluster, reqLogger logr.Logger) (bool, error) {
	update := false
	//Dinesh Todo This flow Needs FIX ####, commented out below to fix status fileds
	/*
		if cluster.Status.Restore.Backup == nil {
			initSpec := cluster.Spec.Restore
			backup, err := r.crController.GetRedisClusterBackup(initSpec.BackupSource.Namespace, initSpec.BackupSource.Name)
			if err != nil {
				reqLogger.Error(err, "GetRedisClusterBackup")
				return update, err
			}
			if backup.Status.Phase != redisv1alpha1.BackupPhaseSucceeded {
				reqLogger.Error(nil, "backup is still running")
				return update, fmt.Errorf("backup is still running")
			}
			cluster.Status.Restore.Backup = backup
			cluster.Status.Restore.Phase = redisv1alpha1.RestorePhaseRunning
			if err := r.crController.UpdateCRStatus(cluster); err != nil {
				return update, err
			}
		}
		backup := cluster.Status.Restore.Backup
		if cluster.Spec.Image == "" {
			cluster.Spec.Image = backup.Status.ClusterImage
			update = true
		}
		if cluster.Spec.MasterSize != backup.Status.MasterSize {
			cluster.Spec.MasterSize = backup.Status.MasterSize
			update = true
		}
	*/
	return update, nil
}

func (r *DistributedRedisClusterReconciler) waitForClusterJoin(ctx *syncContext) error {
	if infos, err := ctx.admin.GetClusterInfos(); err == nil {
		ctx.reqLogger.V(6).Info("debug waitForClusterJoin", "cluster infos", infos)
		return nil
	}
	var firstNode *redisutil.Node
	for _, nodeInfo := range ctx.clusterInfos.Infos {
		firstNode = nodeInfo.Node
		break
	}
	ctx.reqLogger.Info(">>> Sending CLUSTER MEET messages to join the cluster")
	err := ctx.admin.AttachNodeToCluster(firstNode.IPPort())
	if err != nil {
		return Redis.Wrap(err, "AttachNodeToCluster")
	}
	// Give one second for the join to start, in order to avoid that
	// waiting for cluster join will find all the nodes agree about
	// the config as they are still empty with unassigned slots.
	time.Sleep(1 * time.Second)

	_, err = ctx.admin.GetClusterInfos()
	if err != nil {
		return Requeue.Wrap(err, "wait for cluster join")
	}
	return nil
}

func (r *DistributedRedisClusterReconciler) syncCluster(ctx *syncContext) error {
	cluster := ctx.cluster
	admin := ctx.admin
	clusterInfos := ctx.clusterInfos
	expectMasterNum := cluster.Spec.MasterSize
	rCluster, nodes, err := newRedisCluster(clusterInfos, cluster)
	if err != nil {
		return Cluster.Wrap(err, "newRedisCluster")
	}
	clusterCtx := clustering.NewCtx(rCluster, nodes, cluster.Spec.MasterSize, cluster.Name, ctx.reqLogger)
	if err := clusterCtx.DispatchMasters(); err != nil {
		return Cluster.Wrap(err, "DispatchMasters")
	}
	curMasters := clusterCtx.GetCurrentMasters()
	newMasters := clusterCtx.GetNewMasters()
	ctx.reqLogger.Info("masters", "newMasters", len(newMasters), "curMasters", len(curMasters))
	if len(curMasters) == 0 {
		ctx.reqLogger.Info("Creating cluster")
		if err := clusterCtx.PlaceSlaves(); err != nil {
			return Cluster.Wrap(err, "PlaceSlaves")

		}
		if err := clusterCtx.AttachingSlavesToMaster(admin); err != nil {
			return Cluster.Wrap(err, "AttachingSlavesToMaster")
		}

		if err := clusterCtx.AllocSlots(admin, newMasters); err != nil {
			return Cluster.Wrap(err, "AllocSlots")
		}
	} else if len(newMasters) > len(curMasters) {
		ctx.reqLogger.Info("Scaling up")
		if err := clusterCtx.PlaceSlaves(); err != nil {
			return Cluster.Wrap(err, "PlaceSlaves")

		}
		if err := clusterCtx.AttachingSlavesToMaster(admin); err != nil {
			return Cluster.Wrap(err, "AttachingSlavesToMaster")
		}

		if err := clusterCtx.RebalancedCluster(admin, newMasters); err != nil {
			return Cluster.Wrap(err, "RebalancedCluster")
		}
	} else if cluster.Status.MinReplicationFactor < cluster.Spec.ClusterReplicas {
		ctx.reqLogger.Info("Scaling slave")
		if err := clusterCtx.PlaceSlaves(); err != nil {
			return Cluster.Wrap(err, "PlaceSlaves")

		}
		if err := clusterCtx.AttachingSlavesToMaster(admin); err != nil {
			return Cluster.Wrap(err, "AttachingSlavesToMaster")
		}
	} else if len(curMasters) > int(expectMasterNum) {
		ctx.reqLogger.Info("Scaling down")
		var allMaster redisutil.Nodes
		allMaster = append(allMaster, newMasters...)
		allMaster = append(allMaster, curMasters...)
		if err := clusterCtx.DispatchSlotToNewMasters(admin, newMasters, curMasters, allMaster); err != nil {
			return err
		}
		if err := r.scalingDown(ctx, len(curMasters), clusterCtx.GetStatefulsetNodes()); err != nil {
			return err
		}
	}
	return nil
}

func (r *DistributedRedisClusterReconciler) scalingDown(ctx *syncContext, currentMasterNum int, statefulSetNodes map[string]redisutil.Nodes) error {
	cluster := ctx.cluster
	SetClusterRebalancing(&cluster.Status,
		fmt.Sprintf("scale down, currentMasterSize: %d, expectMasterSize %d", currentMasterNum, cluster.Spec.MasterSize))
	r.CrController.UpdateCRStatus(cluster)
	admin := ctx.admin
	expectMasterNum := int(cluster.Spec.MasterSize)
	for i := currentMasterNum - 1; i >= expectMasterNum; i-- {
		stsName := statefulsets.ClusterStatefulSetName(cluster.Name, i)
		for _, node := range statefulSetNodes[stsName] {
			admin.Connections().Remove(node.IPPort())
		}
	}
	for i := currentMasterNum - 1; i >= expectMasterNum; i-- {
		stsName := statefulsets.ClusterStatefulSetName(cluster.Name, i)
		ctx.reqLogger.Info("scaling down", "statefulSet", stsName)
		sts, err := r.StatefulSetController.GetStatefulSet(cluster.Namespace, stsName)
		if err != nil {
			return Kubernetes.Wrap(err, "GetStatefulSet")
		}
		for _, node := range statefulSetNodes[stsName] {
			ctx.reqLogger.Info("forgetNode", "id", node.ID, "ip", node.IP, "role", node.GetRole())
			if len(node.Slots) > 0 {
				return Redis.New(fmt.Sprintf("node %s is not empty! Reshard data away and try again", node.String()))
			}
			if err := admin.ForgetNode(node.ID); err != nil {
				return Redis.Wrap(err, "ForgetNode")
			}
		}
		// remove resource
		if err := r.StatefulSetController.DeleteStatefulSetByName(cluster.Namespace, stsName); err != nil {
			ctx.reqLogger.Error(err, "DeleteStatefulSetByName", "statefulSet", stsName)
		}
		svcName := statefulsets.ClusterHeadlessSvcName(cluster.Name, i)
		if err := r.ServiceController.DeleteServiceByName(cluster.Namespace, svcName); err != nil {
			ctx.reqLogger.Error(err, "DeleteServiceByName", "service", svcName)
		}
		/*
			if err := r.pdbController.DeletePodDisruptionBudgetByName(cluster.Namespace, stsName); err != nil {
				ctx.reqLogger.Error(err, "DeletePodDisruptionBudgetByName", "pdb", stsName)
			}
		*/
		if err := r.PvcController.DeletePvcByLabels(cluster.Namespace, sts.Labels); err != nil {
			ctx.reqLogger.Error(err, "DeletePvcByLabels", "labels", sts.Labels)
		}
		// wait pod Terminating
		waiter := &waitPodTerminating{
			name:                  "waitPodTerminating",
			statefulSet:           stsName,
			timeout:               30 * time.Second * time.Duration(cluster.Spec.ClusterReplicas+2),
			tick:                  5 * time.Second,
			statefulSetController: r.StatefulSetController,
			cluster:               cluster,
		}
		if err := waiting(waiter, ctx.reqLogger); err != nil {
			ctx.reqLogger.Error(err, "waitPodTerminating")
		}

	}
	return nil
}

func (r *DistributedRedisClusterReconciler) checkandUpadtePassword(client client.Client, ctx *syncContext) (error, map[string]string) {

	passChanged, newaclconf, newsecretver, err1 := r.Checker.CheckPasswordChanged(ctx.cluster)
	if err1 != nil {
		ctx.reqLogger.Error(err1, "error when checking acl difference")
		return err1, nil
	}
	if !passChanged {
		ctx.reqLogger.Info("no changes in acl secrets when checking acl differences")
		return nil, nil
	}
	ctx.reqLogger.Info("acl difference found trying to update password")
	SetClusterResetPassword(&ctx.cluster.Status, "updating acl's")
	r.CrController.UpdateCRStatus(ctx.cluster)

	if err := r.Checker.CheckRedisNodeNum(ctx.cluster); err == nil {
		namespace := ctx.cluster.Namespace

		matchLabels := getLabels(ctx.cluster)
		redisClusterPods, err := r.StatefulSetController.GetStatefulSetPodsByLabels(namespace, matchLabels)
		if err != nil {
			return err, newsecretver
		}

		adminPassword, err := statefulsets.GetRedisClusterAdminPassword(r.Client, ctx.cluster, ctx.reqLogger)
		if err != nil {
			return err, newsecretver
		}

		podSet := clusterPods(redisClusterPods.Items)
		admin, err := newRedisAdmin(podSet, adminPassword, config.RedisConf(), ctx.reqLogger)
		if err != nil {
			return err, newsecretver
		}
		defer admin.Close()
		aclcommands, err2 := statefulsets.GenerateAclcommands(ctx.reqLogger, r.Client, ctx.cluster, newaclconf, newsecretver)

		if err2 != nil {
			return err2, newsecretver

		} else {
			ctx.reqLogger.Info("Printing acl commands to be run:")
			for key, value := range aclcommands {
				ctx.reqLogger.Info("acl commands", "user: ", key, "aclcommand: ", value)
			}
		}
		/*
			// Update the password recorded in the file /data/redis_password, redis pod preStop hook
			// need /data/redis_password do CLUSTER FAILOVER
			cmd := fmt.Sprintf("echo %s > /data/redis_password", adminPassword)
			if err := r.execer.ExecCommandInPodSet(podSet, "/bin/sh", "-c", cmd); err != nil {
				return err
			}

			// Reset all redis pod's password.
			if err := admin.ResetPassword(newPassword); err != nil {
				return err
			}
		*/
	}
	return nil, newsecretver
}
