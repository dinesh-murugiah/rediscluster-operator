package distributedrediscluster

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	redisv1alpha1 "github.com/dinesh-murugiah/rediscluster-operator/api/v1alpha1"
	"github.com/dinesh-murugiah/rediscluster-operator/controllers/clustering"
	"github.com/dinesh-murugiah/rediscluster-operator/controllers/manager"
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

func (r *DistributedRedisClusterReconciler) ensureCluster(ctx *syncContext) (bool, error) {
	cluster := ctx.cluster
	if err := r.validateAndSetDefault(cluster, ctx.reqLogger); err != nil {
		if k8sutil.IsRequestRetryable(err) {
			return false, Kubernetes.Wrap(err, "Validate")
		}
		return false, StopRetry.Wrap(err, "stop retry")
	}

	// Redis only load db from append only file when AOF ON, because of
	// we only backed up the RDB file when doing data backup, so we set
	// "appendonly no" force here when do restore.
	dbLoadedFromDiskWhenRestore(cluster, ctx.reqLogger)
	labels := getLabels(cluster)
	if err, crdeferr := r.Ensurer.EnsureRedisConfigMap(cluster, labels); err != nil {
		if crdeferr {
			return false, StopRetry.Wrap(err, "AdminSecretMissing")
		} else {
			return false, Kubernetes.Wrap(err, "EnsureRedisConfigMap")
		}
	}

	isSTScreated, err := r.Ensurer.EnsureRedisStatefulsets(cluster, labels)
	if err != nil {
		ctx.reqLogger.Error(err, "EnsureRedisStatefulSets")
		return false, Kubernetes.Wrap(err, "EnsureRedisStatefulSets")
	}
	if err1 := r.Ensurer.EnsureClusterShardsHeadLessSvcs(cluster, labels); err1 != nil {
		return isSTScreated, Kubernetes.Wrap(err, "EnsureRedisHeadLessSvcs")
	}
	if err2 := r.Ensurer.EnsureClusterHeadLessSvc(cluster, labels); err2 != nil {
		return isSTScreated, Kubernetes.Wrap(err, "EnsureRedisSvc")
	}
	if err3 := r.Ensurer.EnsureRedisRCloneSecret(cluster, labels); err3 != nil {
		if k8sutil.IsRequestRetryable(err) {
			return isSTScreated, Kubernetes.Wrap(err, "EnsureRedisRCloneSecret")
		}
		return isSTScreated, StopRetry.Wrap(err, "stop retry")
	}
	return isSTScreated, nil
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

	// TODO: DR-1173 Move this to validation webhook
	if cluster.Spec.HaConfig == nil {
		err = fmt.Errorf("ha config expected missing")
		return err

	} else {
		if cluster.Spec.HaConfig.HaEnabled {
			if cluster.Spec.HaConfig.ZonesInfo == nil || (len(cluster.Spec.HaConfig.ZonesInfo) < 1 || len(cluster.Spec.HaConfig.ZonesInfo) < 3) {
				err = fmt.Errorf("haspec missing zone info")
				return err
			}
		}
	}

	// TODO: DR-1173 Move this to validation webhook
	for _, container := range *cluster.Spec.Monitor {
		if container.Image == "" || container.Prometheus == nil || container.Prometheus.Port == 0 {
			err = fmt.Errorf("required fields missing for monitor")
			return err
		}
	}

	if cluster.IsRestoreFromBackup() && cluster.ShouldInitRestorePhase() {
		//Dinesh Todo This flow Needs FIX ####, commented out below to fix status fileds
		err = fmt.Errorf("restore Not supported yet")
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

func (r *DistributedRedisClusterReconciler) waitForClusterJoin(ctx *syncContext, context context.Context) error {
	if infos, err := ctx.admin.GetClusterInfos(context); err == nil {
		ctx.reqLogger.V(6).Info("debug waitForClusterJoin", "cluster infos", infos)
		return nil
	}
	var firstNode *redisutil.Node
	for _, nodeInfo := range ctx.clusterInfos.Infos {
		firstNode = nodeInfo.Node
		break
	}
	ctx.reqLogger.Info(">>> Sending CLUSTER MEET messages to join the cluster")
	err := ctx.admin.AttachNodeToCluster(context, firstNode.IPPort())
	if err != nil {
		return Redis.Wrap(err, "AttachNodeToCluster")
	}
	// Give one second for the join to start, in order to avoid that
	// waiting for cluster join will find all the nodes agree about
	// the config as they are still empty with unassigned slots.
	time.Sleep(1 * time.Second)

	_, err = ctx.admin.GetClusterInfos(context)
	if err != nil {
		return Requeue.Wrap(err, "wait for cluster join")
	}
	return nil
}

func (r *DistributedRedisClusterReconciler) syncCluster(ctx *syncContext, context context.Context) error {
	cluster := ctx.cluster
	admin := ctx.admin
	clusterInfos := ctx.clusterInfos
	expectMasterNum := cluster.Spec.MasterSize
	rCluster, nodes, err := newRedisCluster(clusterInfos, cluster, ctx.reqLogger)
	if err != nil {
		return Cluster.Wrap(err, "newRedisCluster")
	}
	clusterCtx := clustering.NewCtx(cluster.Spec.HaConfig, rCluster, nodes, cluster.Spec.MasterSize, cluster.Name, ctx.reqLogger)
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
		if err := clusterCtx.AttachingSlavesToMaster(admin, context); err != nil {
			return Cluster.Wrap(err, "AttachingSlavesToMaster")
		}

		if err := clusterCtx.AllocSlots(admin, newMasters, context); err != nil {
			return Cluster.Wrap(err, "AllocSlots")
		}
	} else if len(newMasters) > len(curMasters) {
		ctx.reqLogger.Info("Scaling up")
		if err := clusterCtx.PlaceSlaves(); err != nil {
			return Cluster.Wrap(err, "PlaceSlaves")

		}
		if err := clusterCtx.AttachingSlavesToMaster(admin, context); err != nil {
			return Cluster.Wrap(err, "AttachingSlavesToMaster")
		}

		if err := clusterCtx.RebalancedCluster(admin, newMasters, context); err != nil {
			return Cluster.Wrap(err, "RebalancedCluster")
		}
	} else if cluster.Status.MinReplicationFactor < cluster.Spec.ClusterReplicas {
		ctx.reqLogger.Info("Scaling slave")
		if err := clusterCtx.PlaceSlaves(); err != nil {
			return Cluster.Wrap(err, "PlaceSlaves")

		}
		if err := clusterCtx.AttachingSlavesToMaster(admin, context); err != nil {
			return Cluster.Wrap(err, "AttachingSlavesToMaster")
		}
	} else if len(curMasters) > int(expectMasterNum) {
		ctx.reqLogger.Info("Scaling down")
		var allMaster redisutil.Nodes
		allMaster = append(allMaster, newMasters...)
		allMaster = append(allMaster, curMasters...)
		if err := clusterCtx.DispatchSlotToNewMasters(admin, newMasters, curMasters, allMaster, context); err != nil {
			return err
		}
		if err := r.scalingDown(ctx, len(curMasters), clusterCtx.GetStatefulsetNodes(), context); err != nil {
			return err
		}
	}
	return nil
}

func (r *DistributedRedisClusterReconciler) scalingDown(ctx *syncContext, currentMasterNum int, statefulSetNodes map[string]redisutil.Nodes, context context.Context) error {
	cluster := ctx.cluster
	SetClusterRebalancing(&cluster.Status,
		fmt.Sprintf("scale down, currentMasterSize: %d, expectMasterSize %d", currentMasterNum, cluster.Spec.MasterSize))
	r.CrController.UpdateCRStatus(cluster)
	admin := ctx.admin
	expectMasterNum := int(cluster.Spec.MasterSize)
	for i := currentMasterNum - 1; i >= expectMasterNum; i-- {
		stsName := statefulsets.ClusterStatefulSetName(cluster.Name, i)
		for _, node := range statefulSetNodes[stsName] {
			admin.Connections().Remove(context, node.IPPort())
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
			if err := admin.ForgetNode(context, node.ID); err != nil {
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

		if err := r.PdbController.DeletePodDisruptionBudgetByName(cluster.Namespace, stsName); err != nil {
			ctx.reqLogger.Error(err, "DeletePodDisruptionBudgetByName", "pdb", stsName)
		}

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

func (r *DistributedRedisClusterReconciler) checkandUpdatePassword(admin redisutil.IAdmin, ctx *syncContext, context context.Context) (map[string]string, error) {

	passChanged, newaclconf, newsecretver, err := r.Checker.CheckPasswordChanged(ctx.cluster)
	if err != nil {
		ctx.reqLogger.Error(err, "Error while checking difference in ACL")
		return nil, err
	}
	if !passChanged {
		ctx.reqLogger.Info("No changes in acl secrets, Proceeding..")
		return nil, nil
	}
	ctx.reqLogger.Info("Difference in ACL found, trying to update password")
	SetClusterResetPassword(&ctx.cluster.Status, "Updating ACL's")
	r.CrController.UpdateCRStatus(ctx.cluster)

	if err := r.Checker.CheckRedisNodeNum(ctx.cluster); err == nil {
		aclcommands, err := statefulsets.GenerateAclcommands(ctx.reqLogger, r.Client, ctx.cluster, newaclconf, newsecretver)

		if err != nil {
			return newsecretver, err

		} else {
			for _, acl := range aclcommands {
				for addr, c := range admin.Connections().GetAll() {
					ctx.reqLogger.Info("Setting ACL for", "address", addr)
					cmd := strings.Fields(acl)
					args := make([]interface{}, len(cmd))
					for i, cm := range cmd {
						args[i] = cm
					}
					resp := c.Do(context, args...)
					if err := admin.Connections().ValidateResp(context, resp, addr, "unable to set ACL"); err != nil {
						return nil, err
					}
				}
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
	return newsecretver, nil
}

func (r *DistributedRedisClusterReconciler) checkandUpdatePods(admin redisutil.IAdmin, ctx *syncContext, context context.Context) (bool, error) {
	var ok, updated bool = false, false
	master_ips, slave_ips, err := r.getRedisIPsByRole(admin, ctx.reqLogger)
	if err != nil {
		return false, err
	}
	ssUR, err := r.getStatefulsetUpdatedRevision(ctx)
	for _, slave_ip := range slave_ips {
		ok, err = r.IsSlaveReady(slave_ip, admin, context)
		if err != nil {
			return false, err
		}
		if ok {
			updated, err = r.updatePodIfNeeded(slave_ip, ctx.pods, ssUR, ctx.reqLogger, ctx.cluster.Namespace, string(redisv1alpha1.RedisClusterNodeRoleSlave))
			if err != nil {
				return false, err
			}
		}
	}
	if updated {
		waiter := &waitStatefulSetUpdating{
			name:                  "waitStatefulSetUpdating",
			timeout:               30 * time.Second * time.Duration(ctx.cluster.Spec.ClusterReplicas),
			tick:                  5 * time.Second,
			statefulSetController: r.StatefulSetController,
			cluster:               ctx.cluster,
		}
		if err = waiting(waiter, ctx.reqLogger); err != nil {
			return false, err
		}
		ctx.reqLogger.Info("Slaves are updated")
		return true, err
	}
	if !updated && !ok {
		ctx.reqLogger.Info("Slaves are not ready")
		return true, err
	}
	if ok {
		for _, master_ip := range master_ips {
			updated, err = r.updatePodIfNeeded(master_ip, ctx.pods, ssUR, ctx.reqLogger, ctx.cluster.Namespace, string(redisv1alpha1.RedisClusterNodeRoleMaster))
			if updated {
				waiter := &waitStatefulSetUpdating{
					name:                  "waitStatefulSetUpdating",
					timeout:               30 * time.Second * time.Duration(ctx.cluster.Spec.ClusterReplicas),
					tick:                  5 * time.Second,
					statefulSetController: r.StatefulSetController,
					cluster:               ctx.cluster,
				}
				if err := waiting(waiter, ctx.reqLogger); err != nil {
					ctx.reqLogger.Info("Im inside wait loop error")
					return false, err
				}
				ctx.reqLogger.Info("Master is updated")
				return true, err
			}
		}
	}
	return false, err
}

func (r *DistributedRedisClusterReconciler) IsSlaveReady(ip string, admin redisutil.IAdmin, ctx context.Context) (bool, error) {
	c, err := admin.Connections().Get(ctx, ip)
	if err != nil {
		return false, err
	}
	info, err := c.Info(context.TODO(), "replication").Result()
	if err != nil {
		return false, err
	}

	ok := !strings.Contains(info, string(redisv1alpha1.RedisSyncing)) &&
		!strings.Contains(info, string(redisv1alpha1.RedisMasterSillPending)) &&
		strings.Contains(info, string(redisv1alpha1.RedisLinkUp))
	return ok, nil
}

func (r *DistributedRedisClusterReconciler) updatePodIfNeeded(ip string, pods []*corev1.Pod, revision_hashes []string, log logr.Logger, namespace string, role string) (bool, error) {
	if index := strings.Index(ip, ":"); index > 0 {
		ip = ip[:index]
	}
	for _, pod := range pods {
		if ip == pod.Status.PodIP {
			pod_name := strings.Split(pod.Name, "-")
			stsIdentifier, err := strconv.Atoi(pod_name[len(pod_name)-2])
			if err != nil {
				return false, fmt.Errorf("unable to convert pod hash identifier to int")
			}
			pod_hash := strings.Split((pod.ObjectMeta.Labels[v1.ControllerRevisionHashLabelKey]), "-")
			if len(pod_hash) < 3 {
				return false, fmt.Errorf("pod hash is invalid")
			}
			client := k8sutil.NewPodController(r.Client)
			if revision_hashes[stsIdentifier] != pod_hash[len(pod_hash)-1] {
				err := client.DeletePodByName(namespace, pod.Name)
				if err != nil {
					return false, err
				}
				return true, err
			} else {
				err := r.setRoleLabelIfNeeded(ip, pod, role, namespace, client)
				return false, err
			}
		}
	}
	return false, fmt.Errorf("pod not found")
}

func (r *DistributedRedisClusterReconciler) setRoleLabelIfNeeded(ip string, pod *corev1.Pod, role string, namespace string, client k8sutil.IPodControl) error {
	for labelKey, labelValue := range pod.ObjectMeta.Labels {
		if labelKey == redisv1alpha1.RedisRoleLabelKey && labelValue == role {
			return nil
		}
	}
	return client.UpdatePodLabels(pod, redisv1alpha1.RedisRoleLabelKey, role)
}

func (r *DistributedRedisClusterReconciler) getRedisIPsByRole(admin redisutil.IAdmin, log logr.Logger) ([]string, []string, error) {
	var masterNodes, slaveNodes []string
	for addr, c := range admin.Connections().GetAll() {
		info, err := c.Info(context.TODO(), "Replication").Result()
		if err != nil {
			return nil, nil, err
		}
		if strings.Contains(info, "role:master") {
			masterNodes = append(masterNodes, addr)
		} else if strings.Contains(info, "role:slave") {
			slaveNodes = append(slaveNodes, addr)
		}
	}
	return masterNodes, slaveNodes, nil
}

func (r *DistributedRedisClusterReconciler) getStatefulsetUpdatedRevision(ctx *syncContext) ([]string, error) {
	cluster := ctx.cluster
	masterNum := int(cluster.Spec.MasterSize)
	var hashes []string
	for i := 0; i < masterNum; i++ {
		stsName := statefulsets.ClusterStatefulSetName(cluster.Name, i)
		sts, err := r.StatefulSetController.GetStatefulSet(cluster.Namespace, stsName)
		if err != nil {
			return hashes, Kubernetes.Wrap(err, "GetStatefulSet")
		}
		cssUR := strings.Split(sts.Status.UpdateRevision, "-")
		if len(cssUR) > 0 {
			hashes = append(hashes, cssUR[len(cssUR)-1])
		}
	}
	return hashes, nil
}
