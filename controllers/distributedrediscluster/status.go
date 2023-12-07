package distributedrediscluster

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	redisv1alpha1 "github.com/dinesh-murugiah/rediscluster-operator/api/v1alpha1"
	utils "github.com/dinesh-murugiah/rediscluster-operator/utils/commonutils"
	"github.com/dinesh-murugiah/rediscluster-operator/utils/k8sutil"
	"github.com/dinesh-murugiah/rediscluster-operator/utils/redisutil"
)

func SetClusterFailed(status *redisv1alpha1.DistributedRedisClusterStatus, reason string) {
	status.Status = redisv1alpha1.ClusterStatusKO
	status.Reason = reason
}

func SetClusterOK(status *redisv1alpha1.DistributedRedisClusterStatus, reason string) {
	status.Status = redisv1alpha1.ClusterStatusOK
	status.Reason = reason
}

func SetClusterRebalancing(status *redisv1alpha1.DistributedRedisClusterStatus, reason string) {
	status.Status = redisv1alpha1.ClusterStatusRebalancing
	status.Reason = reason
}

func SetClusterScaling(status *redisv1alpha1.DistributedRedisClusterStatus, reason string) {
	status.Status = redisv1alpha1.ClusterStatusScaling
	status.Reason = reason
}

func SetClusterUpdating(status *redisv1alpha1.DistributedRedisClusterStatus, reason string) {
	status.Status = redisv1alpha1.ClusterStatusOnDelete
	status.Reason = reason
}

func SetSecretStatus(status *redisv1alpha1.DistributedRedisClusterStatus, secretstate string) {
	status.SecretStatus = secretstate
}

func SetHAStatus(status *redisv1alpha1.DistributedRedisClusterStatus, HAstate redisv1alpha1.HaStatus) {
	status.HAStatus = HAstate
}

func SetClusterResetPassword(status *redisv1alpha1.DistributedRedisClusterStatus, reason string) {
	status.Status = redisv1alpha1.ClusterStatusResetPassword
	status.Reason = reason
}

func buildHAStatus(clusterInfos *redisutil.ClusterInfos, pods []*corev1.Pod, cluster *redisv1alpha1.DistributedRedisCluster, reqLogger logr.Logger, Client client.Client) (redisv1alpha1.HaStatus, error) {

	var numMastersHAPlaced int32 = 0
	var misplacemasters redisutil.Nodes = make([]*redisutil.Node, 0)
	//nodectrl := k8sutil.NewNodeController(Client)

	zones := make([]string, 0, len(cluster.Spec.HaConfig.ZonesInfo))
	for zone := range cluster.Spec.HaConfig.ZonesInfo {
		zones = append(zones, zone)
	}
	sort.Strings(zones)

	masterNodes, err := clusterInfos.GetNodes().GetNodesByFunc(func(node *redisutil.Node) bool {
		return node.Role == redisutil.RedisMasterRole
	})

	if err != nil || len(masterNodes) != int(cluster.Spec.MasterSize) {
		reqLogger.Error(err, fmt.Sprintf("unable to retrieve sufficient redis node with the role master"))
		return redisv1alpha1.HaStatusFailed, err
	}

	for _, node := range masterNodes {
		reqLogger.Info("buildHAStatus", "stsname", node.StatefulSet, "zone", node.Zonename)
		stsindex, err := getSTSindex(node.StatefulSet)
		if err != nil {
			reqLogger.Error(err, fmt.Sprintf("unable to retrieve sufficient redis node with the role master"))
			return redisv1alpha1.HaStatusFailed, err
		}
		zoneoffset := (stsindex % len(cluster.Spec.HaConfig.ZonesInfo))
		zonename := zones[zoneoffset]
		if node.Zonename == zonename {
			numMastersHAPlaced++
		} else {
			misplacemasters = append(misplacemasters, node)
		}

	}

	if numMastersHAPlaced != cluster.Spec.MasterSize {
		err := fmt.Errorf("master placement not suitable for cluster ha")
		for _, node := range misplacemasters {
			reqLogger.Info("buildHAStatus", "misplaced master", node.PodName, "misplaced zone", node.Zonename)
		}
		return redisv1alpha1.HaStatusRedistribute, err
	}
	return redisv1alpha1.HaStatusHealthy, nil

}

func getSTSindex(stsName string) (int, error) {
	// Find the last index of '-'
	lastIndex := strings.LastIndex(stsName, "-")
	if lastIndex == -1 {
		err := fmt.Errorf("Unable to find the last index of '-' in %s", stsName)
		return -1, err
	}

	// Extract the substring after the last '-'
	stsindexstr := stsName[lastIndex+1:]

	// Check if the substring is a number and return the sts index
	if stsindex, err := strconv.Atoi(stsindexstr); err == nil {
		return stsindex, nil
	} else {
		err := fmt.Errorf("invalid statefulset index in %s", stsName)
		return -1, err
	}
}

func buildClusterStatus(clusterInfos *redisutil.ClusterInfos, pods []*corev1.Pod,
	cluster *redisv1alpha1.DistributedRedisCluster, reqLogger logr.Logger, Client client.Client, updateClusterInfo bool) *redisv1alpha1.DistributedRedisClusterStatus {
	oldStatus := cluster.Status
	status := &redisv1alpha1.DistributedRedisClusterStatus{
		Status:       oldStatus.Status,
		Reason:       oldStatus.Reason,
		SecretStatus: oldStatus.SecretStatus,
		SecretsVer:   oldStatus.SecretsVer,
		Restore:      oldStatus.Restore,
		HAStatus:     oldStatus.HAStatus,
	}
	nodectrl := k8sutil.NewNodeController(Client)

	nbMaster := int32(0)
	nbSlaveByMaster := map[string]int{}

	for _, pod := range pods {
		redisNodes, err := clusterInfos.GetNodes().GetNodesByFunc(func(node *redisutil.Node) bool {
			return node.IP == pod.Status.PodIP
		})
		if err != nil {
			reqLogger.Error(err, fmt.Sprintf("unable to retrieve the associated redis node with the pod: %s, ip:%s", pod.Name, pod.Status.PodIP))
			continue
		}
		if len(redisNodes) == 1 {
			redisNode := redisNodes[0]

			newNode := redisv1alpha1.RedisClusterNode{
				PodName:  pod.Name,
				NodeName: pod.Spec.NodeName,
				IP:       pod.Status.PodIP,
				Slots:    []string{},
			}
			znode, err := nodectrl.GetNode(pod.Spec.NodeName)
			if err == nil {
				newNode.Zonename = nodectrl.GetZoneLabel(znode)
				//reqLogger.Info("buildClusterStatus", "Zone label found", newNode.Zonename)
			} else {
				reqLogger.Error(err, "GetNode Returned Error", "context", "buildClusterStatus", "action", "setting zonename to unknown")
				newNode.Zonename = "unknown"
			}
			if len(pod.OwnerReferences) > 0 {
				if pod.OwnerReferences[0].Kind == "StatefulSet" {
					newNode.StatefulSet = pod.OwnerReferences[0].Name
				}
			}
			if updateClusterInfo {
				redisNode.NodeName = newNode.NodeName
				redisNode.PodName = newNode.PodName
				redisNode.Zonename = newNode.Zonename
				redisNode.StatefulSet = newNode.StatefulSet
			}
			if redisutil.IsMasterWithSlot(redisNode) {
				if _, ok := nbSlaveByMaster[redisNode.ID]; !ok {
					nbSlaveByMaster[redisNode.ID] = 0
				}
				nbMaster++
			}

			newNode.ID = redisNode.ID
			newNode.Role = redisNode.GetRole()
			newNode.Port = redisNode.Port
			newNode.Slots = []string{}
			if redisutil.IsSlave(redisNode) && redisNode.MasterReferent != "" {
				nbSlaveByMaster[redisNode.MasterReferent] = nbSlaveByMaster[redisNode.MasterReferent] + 1
				newNode.MasterRef = redisNode.MasterReferent
			}
			if len(redisNode.Slots) > 0 {
				slots := redisutil.SlotRangesFromSlots(redisNode.Slots)
				for _, slot := range slots {
					newNode.Slots = append(newNode.Slots, slot.String())
				}
			}
			status.Nodes = append(status.Nodes, newNode)
		} else {
			err1 := fmt.Errorf("multiple nodes with same podip")
			reqLogger.Error(err1, fmt.Sprintf("multiple nodes in cluster info has same podip:%s", pod.Status.PodIP))
			continue
		}
	}
	status.NumberOfMaster = nbMaster

	minReplicationFactor := math.MaxInt32
	maxReplicationFactor := 0
	for _, counter := range nbSlaveByMaster {
		if counter > maxReplicationFactor {
			maxReplicationFactor = counter
		}
		if counter < minReplicationFactor {
			minReplicationFactor = counter
		}
	}
	if len(nbSlaveByMaster) == 0 {
		minReplicationFactor = 0
	}
	status.MaxReplicationFactor = int32(maxReplicationFactor)
	status.MinReplicationFactor = int32(minReplicationFactor)

	return status
}

func (r *DistributedRedisClusterReconciler) updateClusterIfNeed(cluster *redisv1alpha1.DistributedRedisCluster,
	newStatus *redisv1alpha1.DistributedRedisClusterStatus,
	reqLogger logr.Logger) {
	if compareStatus(&cluster.Status, newStatus, reqLogger) {
		reqLogger.WithValues("namespace", cluster.Namespace, "name", cluster.Name).
			V(3).Info("status changed")
		cluster.Status = *newStatus
		r.CrController.UpdateCRStatus(cluster)
	}
}

func compareStatus(old, new *redisv1alpha1.DistributedRedisClusterStatus, reqLogger logr.Logger) bool {
	if utils.CompareStringValue("ClusterStatus", string(old.Status), string(new.Status), reqLogger) {
		return true
	}

	if utils.CompareStringValue("SecretStatus", string(old.SecretStatus), string(new.SecretStatus), reqLogger) {
		return true
	}

	if old.SecretsVer == nil {
		return true
	} else if len(old.SecretsVer) == 0 {
		return true
	}

	if utils.CompareStringValue("ClusterStatusReason", old.Reason, new.Reason, reqLogger) {
		return true
	}

	if utils.CompareInt32("NumberOfMaster", old.NumberOfMaster, new.NumberOfMaster, reqLogger) {
		return true
	}

	if utils.CompareInt32("len(Nodes)", int32(len(old.Nodes)), int32(len(new.Nodes)), reqLogger) {
		return true
	}

	if utils.CompareStringValue("restoreSucceeded", string(old.Restore.Phase), string(new.Restore.Phase), reqLogger) {
		return true
	}

	for _, nodeA := range old.Nodes {
		found := false
		for _, nodeB := range new.Nodes {
			if nodeA.ID == nodeB.ID {
				found = true
				if compareNodes(&nodeA, &nodeB, reqLogger) {
					return true
				}
			}
		}
		if !found {
			return true
		}
	}

	return false
}

func compareNodes(nodeA, nodeB *redisv1alpha1.RedisClusterNode, reqLogger logr.Logger) bool {
	if utils.CompareStringValue("Node.IP", nodeA.IP, nodeB.IP, reqLogger) {
		return true
	}
	if utils.CompareStringValue("Node.MasterRef", nodeA.MasterRef, nodeB.MasterRef, reqLogger) {
		return true
	}
	if utils.CompareStringValue("Node.PodName", nodeA.PodName, nodeB.PodName, reqLogger) {
		return true
	}
	if utils.CompareStringValue("Node.Port", nodeA.Port, nodeB.Port, reqLogger) {
		return true
	}
	if utils.CompareStringValue("Node.Role", string(nodeA.Role), string(nodeB.Role), reqLogger) {
		return true
	}

	sizeSlotsA := 0
	sizeSlotsB := 0
	if nodeA.Slots != nil {
		sizeSlotsA = len(nodeA.Slots)
	}
	if nodeB.Slots != nil {
		sizeSlotsB = len(nodeB.Slots)
	}
	if sizeSlotsA != sizeSlotsB {
		reqLogger.V(4).Info(fmt.Sprintf("compare Node.Slote size: %d - %d", sizeSlotsA, sizeSlotsB))
		return true
	}

	if (sizeSlotsA != 0) && !reflect.DeepEqual(nodeA.Slots, nodeB.Slots) {
		reqLogger.V(4).Info(fmt.Sprintf("compare Node.Slote deepEqual: %v - %v", nodeA.Slots, nodeB.Slots))
		return true
	}

	return false
}
