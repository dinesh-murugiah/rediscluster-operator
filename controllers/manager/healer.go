package manager

import (
	"context"
	"time"

	redisv1alpha1 "github.com/dinesh-murugiah/rediscluster-operator/api/v1alpha1"
	"github.com/dinesh-murugiah/rediscluster-operator/controllers/heal"
	"github.com/dinesh-murugiah/rediscluster-operator/utils/redisutil"
)

type IHeal interface {
	Heal(cluster *redisv1alpha1.DistributedRedisCluster, infos *redisutil.ClusterInfos, admin redisutil.IAdmin, context context.Context) (bool, error)
	FixTerminatingPods(cluster *redisv1alpha1.DistributedRedisCluster, maxDuration time.Duration) (bool, error)
}

type realHeal struct {
	*heal.CheckAndHeal
}

func NewHealer(heal *heal.CheckAndHeal) IHeal {
	return &realHeal{heal}
}

func (h *realHeal) Heal(cluster *redisv1alpha1.DistributedRedisCluster, infos *redisutil.ClusterInfos, admin redisutil.IAdmin, context context.Context) (bool, error) {
	if actionDone, err := h.FixFailedNodes(cluster, infos, admin, context); err != nil {
		return actionDone, err
	} else if actionDone {
		return actionDone, nil
	}

	if actionDone, err := h.FixUntrustedNodes(cluster, infos, admin, context); err != nil {
		return actionDone, err
	} else if actionDone {
		return actionDone, nil
	}
	return false, nil
}
