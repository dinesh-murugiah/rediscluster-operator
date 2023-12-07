package heal

import (
	"context"

	"k8s.io/apimachinery/pkg/util/errors"

	redisv1alpha1 "github.com/dinesh-murugiah/rediscluster-operator/api/v1alpha1"
	"github.com/dinesh-murugiah/rediscluster-operator/utils/redisutil"
)

// FixFailedNodes fix failed nodes: in some cases (cluster without enough master after crash or scale down), some nodes may still know about fail nodes
func (c *CheckAndHeal) FixFailedNodes(cluster *redisv1alpha1.DistributedRedisCluster, infos *redisutil.ClusterInfos, admin redisutil.IAdmin, context context.Context) (bool, error) {

	forgetSet := listGhostNodes(cluster, infos)
	var errs []error
	doneAnAction := false
	for id := range forgetSet {
		doneAnAction = true
		c.Logger.Info("[FixFailedNodes] Forgetting failed node, this command might fail, this is not an error", "node", id)
		if !c.DryRun {
			c.Logger.Info("[FixFailedNodes] try to forget node", "nodeId", id)
			if err := admin.ForgetNode(context, id); err != nil {
				errs = append(errs, err)
			}
		}
	}

	return doneAnAction, errors.NewAggregate(errs)
}

// listGhostNodes : A Ghost node is a node still known by some redis node but which doesn't exists anymore
// meaning it is failed, and pod not in kubernetes, or without targetable IP
func listGhostNodes(cluster *redisv1alpha1.DistributedRedisCluster, infos *redisutil.ClusterInfos) map[string]bool {
	ghostNodesSet := map[string]bool{}
	ghostNodesDetect := map[string]int32{}
	if infos == nil || infos.Infos == nil {
		return ghostNodesSet
	}
	for _, nodeinfos := range infos.Infos {
		for _, node := range nodeinfos.Friends {
			// only forget it when no more part of kubernetes, or if noaddress
			if node.HasStatus(redisutil.NodeStatusNoAddr) {
				ghostNodesDetect[node.ID] += 1
			}
			//Pfail is a transient state and is a non majority declaration , this should not be used identifying a node as ghost
			//if node.HasStatus(redisutil.NodeStatusFail) || node.HasStatus(redisutil.NodeStatusPFail) {
			if node.HasStatus(redisutil.NodeStatusFail) {
				ghostNodesDetect[node.ID] += 1
			}
		}
	}

	for ghnodeid, count := range ghostNodesDetect {
		if count >= ((cluster.Spec.ClusterReplicas+1)*cluster.Spec.MasterSize)/2 {
			ghostNodesSet[ghnodeid] = true
		}
	}

	return ghostNodesSet
}
