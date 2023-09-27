package statefulsets

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	redisv1alpha1 "github.com/dinesh-murugiah/rediscluster-operator/api/v1alpha1"
	"github.com/go-logr/logr"
)

const passwordKey = "password"

func getAdminPassword(key string, envSet []corev1.EnvVar) string {
	for _, value := range envSet {
		if key == value.Name {
			/*
				if value.ValueFrom != nil && value.ValueFrom.SecretKeyRef != nil && len(value.Value) != 0 {
					return value.Value
				}
			*/
			if len(value.Value) != 0 {
				return value.Value
			}
		}
	}
	return ""
}

// GetOldRedisClusterPassword return old redis cluster's password.
func GetRedisClusterAdminPassword(client client.Client, cluster *redisv1alpha1.DistributedRedisCluster, log logr.Logger) (string, error) {
	if cluster.Spec.AdminSecret == nil {
		return "", nil
	}
	secret := &corev1.Secret{}
	err := client.Get(context.TODO(), types.NamespacedName{
		Name:      cluster.Spec.AdminSecret.Name,
		Namespace: cluster.Namespace,
	}, secret)
	if err != nil {
		return "", err
	}
	return string(secret.Data[passwordKey]), nil

}

func GenerateAclcommands(log logr.Logger, client client.Client, cluster *redisv1alpha1.DistributedRedisCluster, newaclconf string, newsecretver map[string]string) (map[string]string, error) {

	aclcommands := make(map[string]string)

	if cluster.Status.SecretsVer == nil || len(cluster.Status.SecretsVer) == 0 {
		return aclcommands, fmt.Errorf("secrets version map invalid in status")
	}

	if len(newaclconf) == 0 {
		return aclcommands, fmt.Errorf("invalid new secrets")
	}
	existingsecretver := cluster.Status.SecretsVer
	//existingsecretver := newsecretver
	newacldata := parseAclInput(newaclconf)

	//First add the set of usersnewly added if any and list of users for which acl rules changed
	for key := range newsecretver {
		if _, ok := existingsecretver[key]; !ok {
			log.Info("diffAclConfigs", "New User found in latest secret", key)
			aclcommands[key] = fmt.Sprintf("acl setuser %s", newacldata[key])
		} else if map1Value, ok := existingsecretver[key]; ok {
			map2Value := newsecretver[key]
			if !reflect.DeepEqual(map1Value, map2Value) {
				log.Info("diffAclConfigs", "update in acl data found for user/secret", key, "command:", newacldata[key])
				aclcommands[key] = fmt.Sprintf("acl setuser %s", newacldata[key])
			}
		}
	}

	// Give deluser for the users which are to be removed
	for key := range existingsecretver {
		if _, ok := newsecretver[key]; !ok {
			log.Info("diffAclConfigs", "User not found in latest secret, possibly deleted", key)
			aclcommands[key] = fmt.Sprintf("acl setuser %s", key)
		}
	}

	if aclcommands == nil || len(aclcommands) == 0 {
		err := fmt.Errorf("unexpected error when trying to create acl comands")
		log.Error(err, "Error in GenerateAclcommands")
		return nil, err
	}

	return aclcommands, nil

}

func parseAclInput(input string) map[string]string {
	lines := strings.Split(input, "\n")
	data := make(map[string]string)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		fields := strings.Fields(line)

		if len(fields) >= 3 && fields[0] == "user" {
			if fields[1] != "admin" && fields[1] != "pinger" {
				key := fields[1]
				value := strings.Join(fields[1:], " ")
				data[key] = value
			}
		}
	}

	return data
}
func IsdiffAclConfigs(curr_map, check_map map[string]string, logger logr.Logger) bool {

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

/*
// GetClusterPassword return current redis cluster's password.
func GetClusterPassword(client client.Client, cluster *redisv1alpha1.DistributedRedisCluster) (string, error) {
	if cluster.Spec.AdminSecret == nil {
		return "", nil
	}
	secret := &corev1.Secret{}
	err := client.Get(context.TODO(), types.NamespacedName{
		Name:      cluster.Spec.AdminSecret.Name,
		Namespace: cluster.Namespace,
	}, secret)
	if err != nil {
		return "", err
	}
	return string(secret.Data[passwordKey]), nil
}
*/
