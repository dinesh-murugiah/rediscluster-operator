package poddisruptionbudgets

import (
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	redisv1alpha1 "github.com/dinesh-murugiah/rediscluster-operator/api/v1alpha1"
)

func NewPodDisruptionBudgetForCR(cluster *redisv1alpha1.DistributedRedisCluster, name string, labels map[string]string) *policyv1.PodDisruptionBudget {
	//maxUnavailable := intstr.FromInt(1)
	minUnavailable := intstr.FromInt(int(cluster.Spec.ClusterReplicas)) //Always master is 1 for a sts so, formula is (clusterreplicas + master - 1) which is clusterreplicas

	return &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{
			Labels:          labels,
			Name:            name,
			Namespace:       cluster.Namespace,
			OwnerReferences: redisv1alpha1.DefaultOwnerReferences(cluster),
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: &minUnavailable,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
		},
	}
}
