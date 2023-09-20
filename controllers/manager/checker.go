package manager

import (
	"fmt"
	"reflect"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	redisv1alpha1 "github.com/dinesh-murugiah/rediscluster-operator/api/v1alpha1"
	"github.com/dinesh-murugiah/rediscluster-operator/resources/configmaps"
	"github.com/dinesh-murugiah/rediscluster-operator/resources/statefulsets"
	"github.com/dinesh-murugiah/rediscluster-operator/utils/k8sutil"
	"github.com/go-logr/logr"
)

type ICheck interface {
	CheckRedisNodeNum(*redisv1alpha1.DistributedRedisCluster) error
	//CheckRedisMasterNum(*redisv1alpha1.DistributedRedisCluster) error
	CheckPasswordChanged(*redisv1alpha1.DistributedRedisCluster) (bool, string, map[string]string, error)
}

type realCheck struct {
	statefulSetClient k8sutil.IStatefulSetControl
	configMapClient   k8sutil.IConfigMapControl
	logger            logr.Logger
	client            client.Client
}

func NewCheck(client client.Client, logger logr.Logger) ICheck {
	return &realCheck{
		statefulSetClient: k8sutil.NewStatefulSetController(client),
		configMapClient:   k8sutil.NewConfigMapController(client),
		logger:            logger,
		client:            client,
	}
}

func (c *realCheck) CheckPasswordChanged(cluster *redisv1alpha1.DistributedRedisCluster) (bool, string, map[string]string, error) {
	secretversion := make(map[string]string)
	aclcmname := configmaps.AclConfigMapName(cluster.Name)
	_, err := c.configMapClient.GetConfigMap(cluster.Namespace, aclcmname)
	if err != nil {
		if errors.IsNotFound(err) {
			c.logger.WithValues("ConfigMap.Namespace", cluster.Namespace, "ConfigMap.Name", aclcmname).
				Info("config map not found when checking password change")
			return false, "", secretversion, err
		}
	}

	if cluster.Status.SecretsVer == nil || len(cluster.Status.SecretsVer) == 0 {
		c.logger.WithValues("Namespace", cluster.Namespace).
			Info("Secret version info not found in CR!!!")
		return false, "", secretversion, err
	}
	//acldata_curr := aclcm.Data[configmaps.Aclfile]
	//aclmap_curr := parseAclInput(acldata_curr)
	//acldata_new, secretversion, err2 := configmaps.GenerateAclconf(c.logger, c.client, cluster)
	acldata_new, secretversion, err2 := configmaps.GenerateAclconf(c.logger, c.client, cluster)
	if err2 != nil {
		c.logger.Error(err2, "unable to generate acl data from secrets")
		return false, "", secretversion, err
	}
	//aclmap_new := parseAclInput(acldata_new)
	return IsdiffAclVerions(cluster.Status.SecretsVer, secretversion, c.logger), acldata_new, secretversion, nil
	//return IsdiffAclVerions(secretversion, secretversion, c.logger), acldata_new, secretversion, nil

}
func (c *realCheck) CheckRedisNodeNum(cluster *redisv1alpha1.DistributedRedisCluster) error {
	for i := 0; i < int(cluster.Spec.MasterSize); i++ {
		name := statefulsets.ClusterStatefulSetName(cluster.Name, i)
		expectNodeNum := cluster.Spec.ClusterReplicas + 1
		ss, err := c.statefulSetClient.GetStatefulSet(cluster.Namespace, name)
		if err != nil {
			return err
		}
		if err := c.checkRedisNodeNum(expectNodeNum, ss); err != nil {
			return err
		}
	}

	return nil
}

func (c *realCheck) checkRedisNodeNum(expectNodeNum int32, ss *appsv1.StatefulSet) error {
	if expectNodeNum != *ss.Spec.Replicas {
		return fmt.Errorf("number of redis pods is different from specification")
	}
	if expectNodeNum != ss.Status.ReadyReplicas {
		return fmt.Errorf("redis pods are not all ready")
	}
	if expectNodeNum != ss.Status.CurrentReplicas {
		return fmt.Errorf("redis pods need to be updated")
	}

	return nil
}

func (c *realCheck) CheckRedisMasterNum(cluster *redisv1alpha1.DistributedRedisCluster) error {
	if cluster.Spec.MasterSize != cluster.Status.NumberOfMaster {
		return fmt.Errorf("number of redis master different from specification")
	}
	return nil
}

func IsdiffAclVerions(curr_map, check_map map[string]string, logger logr.Logger) bool {

	// Check keys present in curr_map but not in check_map
	for key := range curr_map {
		if _, ok := check_map[key]; !ok {
			logger.Info("diffAclConfigs", "User not found in latest secret, possibly deleted", key)
			return true
		}
	}

	// Check keys present in check_map but not in curr_map
	for key := range check_map {
		if _, ok := curr_map[key]; !ok {
			logger.Info("diffAclConfigs", "New User found in latest secret", key)
			return true
		}
	}

	// Check acl of existing users changed
	for key := range curr_map {
		if map2Value, ok := check_map[key]; ok {
			map1Value := curr_map[key]
			if !reflect.DeepEqual(map1Value, map2Value) {
				logger.Info("diffAclConfigs", "update in acl data found for user/secret", key)
				return true
			}
		}
	}

	return false
}

//
//func (c *realCheck) CheckRedisClusterIsEmpty(cluster *redisv1alpha1.DistributedRedisCluster, admin redisutil.IAdmin) (bool, error) {
//
//}
