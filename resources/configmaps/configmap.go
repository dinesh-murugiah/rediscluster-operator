package configmaps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	redisv1alpha1 "github.com/dinesh-murugiah/rediscluster-operator/api/v1alpha1"
	"github.com/go-logr/logr"
)

const (
	RestoreSucceeded = "succeeded"

	RedisConfKey = "redis.conf"

	Aclfile = "users.acl"
)

var ErrBadInput = errors.New("admin-secret-missing")

// NewConfigMapForCR creates a new ConfigMap for the given Cluster
func NewConfigMapForCR(cluster *redisv1alpha1.DistributedRedisCluster, labels map[string]string) *corev1.ConfigMap {
	// Do CLUSTER FAILOVER when master down
	shutdownContent := `#!/bin/sh
CLUSTER_CONFIG="/data/nodes.conf"
failover() {
	echo "Do CLUSTER FAILOVER"
	masterID=$(cat ${CLUSTER_CONFIG} | grep "myself" | awk '{print $1}')
	echo "Master: ${masterID}"
	slave=$(cat ${CLUSTER_CONFIG} | grep ${masterID} | grep "slave" | awk 'NR==1{print $2}' | sed 's/:6379@16379//')
	echo "Slave: ${slave}"
	password=$(cat /data/redis_password)
	if [[ -z "${password}" ]]; then
		redis-cli -h ${slave} CLUSTER FAILOVER
	else
		redis-cli -h ${slave} -a "${password}" CLUSTER FAILOVER
	fi
	echo "Wait for MASTER <-> SLAVE syncFinished"
	sleep 20
}
if [ -f ${CLUSTER_CONFIG} ]; then
	cat ${CLUSTER_CONFIG} | grep "myself" | grep "master" && \
	failover
fi`

	// Fixed Nodes.conf does not update IP address of a node when IP changes after restart,
	// see more https://github.com/antirez/redis/issues/4645.
	fixIPContent := `#!/bin/sh
CLUSTER_CONFIG="/data/nodes.conf"
if [ -f ${CLUSTER_CONFIG} ]; then
    if [ -z "${POD_IP}" ]; then
    echo "Unable to determine Pod IP address!"
    exit 1
    fi
    echo "Updating my IP to ${POD_IP} in ${CLUSTER_CONFIG}"
    sed -i.bak -e "/myself/ s/ .*:6379@16379/ ${POD_IP}:6379@16379/" ${CLUSTER_CONFIG}
fi
exec "$@"`

	redisConfContent := generateRedisConfContent(cluster.Spec.Config)
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            RedisConfigMapName(cluster.Name),
			Namespace:       cluster.Namespace,
			Labels:          labels,
			OwnerReferences: redisv1alpha1.DefaultOwnerReferences(cluster),
		},
		Data: map[string]string{
			"shutdown.sh": shutdownContent,
			"fix-ip.sh":   fixIPContent,
			RedisConfKey:  redisConfContent,
		},
	}
}

func NewACLConfigMap(conflog logr.Logger, client client.Client, cluster *redisv1alpha1.DistributedRedisCluster, labels map[string]string) (*corev1.ConfigMap, map[string]string, error, bool) {
	//	secretversion := make(map[string]string)

	aclfileContent, secretversion, err := GenerateAclconf(conflog, client, cluster)
	if err != nil {
		if errors.Is(err, ErrBadInput) {
			return nil, secretversion, err, true
		} else {
			return nil, secretversion, err, false
		}
	}

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            AclConfigMapName(cluster.Name),
			Namespace:       cluster.Namespace,
			Labels:          labels,
			OwnerReferences: redisv1alpha1.DefaultOwnerReferences(cluster),
		},
		Data: map[string]string{
			Aclfile: aclfileContent,
		},
	}, secretversion, nil, false

}

func Getsecretversions(conflog logr.Logger, client client.Client, cluster *redisv1alpha1.DistributedRedisCluster) (map[string]string, error) {

	secretversion := make(map[string]string)

	if cluster.Spec.AdminSecret == nil {

		err := fmt.Errorf("invalid custom resource - admin secret missing")
		conflog.Error(err, "admin secret missing")
		return secretversion, err
	} else {
		_, versionID, err := GetAclStringFromSecrets(conflog, client, cluster, cluster.Spec.AdminSecret.Name)
		if err != nil {
			conflog.Error(err, "admin secret unable to get")
			return secretversion, err

		} else {
			secretversion[cluster.Spec.AdminSecret.Name] = versionID
		}
	}

	if cluster.Spec.DefaultSecret == nil {
		conflog.Info("Getsecretversions", "defaultsecret", "default secret not given generating one. Ideally should not come here")
		secretversion["default-secret"] = "default"
	} else {
		_, versionID, err := GetAclStringFromSecrets(conflog, client, cluster, cluster.Spec.DefaultSecret.Name)
		if err != nil {
			conflog.Error(err, "default secret unable to get")
			return secretversion, err

		} else {

			secretversion[cluster.Spec.DefaultSecret.Name] = versionID
		}
	}

	if cluster.Spec.AdditionalSecret != nil {
		for _, ref := range cluster.Spec.AdditionalSecret {
			sceretrefname := ref.Name
			//conflog.Info("Getsecretversions", "generating acl for", sceretrefname)
			_, versionID, err := GetAclStringFromSecrets(conflog, client, cluster, sceretrefname)
			if err != nil {
				conflog.Error(err, "additional secret unable to get")
				return secretversion, err
			} else {

				secretversion[sceretrefname] = versionID
			}
		}
	}
	return secretversion, nil
}

/*
	func Getsecretversions(conflog logr.Logger, client client.Client, cluster *redisv1alpha1.DistributedRedisCluster) (map[string]string, error) {
		secretversion := make(map[string]string)

		if cluster.Spec.AdminSecret == nil {
			err := fmt.Errorf("admin secret refrence not defined in CR")
			conflog.Error(err, "in Getsecretversions:")
			secretversion["admin-secret"] = "default"
		} else {
			conflog.Info("Getsecretversions", "fetting secret revision for", cluster.Spec.AdminSecret.Name)
			_, versionID, err := GetAclStringFromSecrets(conflog, client, cluster, cluster.Spec.AdminSecret.Name)
			if err != nil {
				conflog.Error(err, "admin secret unable to get")
				return secretversion, err

			} else {
				secretversion[cluster.Spec.AdminSecret.Name] = versionID
			}
		}

		if cluster.Spec.DefaultSecret == nil {
			err := fmt.Errorf("default secret refrence not defined in CR")
			conflog.Error(err, "in Getsecretversions:")
			return secretversion, err
		} else {
			conflog.Info("Getsecretversions", "fetting secret revision for", cluster.Spec.DefaultSecret.Name)
			_, versionID, err := GetAclStringFromSecrets(conflog, client, cluster, cluster.Spec.DefaultSecret.Name)
			if err != nil {
				conflog.Error(err, "default secret unable to get")
				return secretversion, err

			} else {
				secretversion[cluster.Spec.DefaultSecret.Name] = versionID
			}
		}

		if cluster.Spec.AdditionalSecret != nil {
			for _, ref := range cluster.Spec.AdditionalSecret {
				sceretrefname := ref.Name
				conflog.Info("Getsecretversions", "fetting secret revision for", sceretrefname)
				_, versionID, err := GetAclStringFromSecrets(conflog, client, cluster, sceretrefname)
				if err != nil {
					conflog.Error(err, "additional secret unable to get")
				} else {
					secretversion[sceretrefname] = versionID
				}
			}
		}
		return secretversion, nil
	}
*/
func GenerateAclconf(conflog logr.Logger, client client.Client, cluster *redisv1alpha1.DistributedRedisCluster) (string, map[string]string, error) {
	var buffer bytes.Buffer

	secretversion := make(map[string]string)

	if cluster.Spec.AdminSecret == nil {

		err := fmt.Errorf("invalid custom resource: %w", ErrBadInput)
		conflog.Error(err, "admin secret missing")
		return "", secretversion, err
	} else {
		acldata, versionID, err := GetAclStringFromSecrets(conflog, client, cluster, cluster.Spec.AdminSecret.Name)
		if err != nil {
			conflog.Error(err, "admin secret unable to create")
			return "", secretversion, err

		} else {
			buffer.WriteString(acldata)
			buffer.WriteString("\n")
			secretversion[cluster.Spec.AdminSecret.Name] = versionID
		}
	}

	if cluster.Spec.DefaultSecret == nil {
		conflog.Info("generateAclconf", "defaultsecret", "default secret not given generating one. Ideally should not come here")
		buffer.WriteString("user default on ~* +@all -@dangerous >defaultpwd")
		buffer.WriteString("\n")
		secretversion["default-secret"] = "default"
	} else {
		acldata, versionID, err := GetAclStringFromSecrets(conflog, client, cluster, cluster.Spec.DefaultSecret.Name)
		if err != nil {
			conflog.Error(err, "default secret unable to create")
			return "", secretversion, err

		} else {
			buffer.WriteString(acldata)
			buffer.WriteString("\n")
			secretversion[cluster.Spec.DefaultSecret.Name] = versionID
		}
	}

	if cluster.Spec.AdditionalSecret != nil {
		for _, ref := range cluster.Spec.AdditionalSecret {
			sceretrefname := ref.Name
			//conflog.Info("generateAclconf", "generating acl for", sceretrefname)
			acldata, versionID, err := GetAclStringFromSecrets(conflog, client, cluster, sceretrefname)
			if err != nil {
				conflog.Error(err, "additional secret unable to create")
				return "", secretversion, err
			} else {
				buffer.WriteString(acldata)
				buffer.WriteString("\n")
				secretversion[sceretrefname] = versionID
			}
		}
	}
	return buffer.String(), secretversion, nil
}
func generateRedisConfContent(configMap map[string]string) string {

	var buffer bytes.Buffer

	// Write the acl file
	aclfilepath := "/acl/" + Aclfile
	buffer.WriteString(fmt.Sprintf("%s %s", "aclfile", aclfilepath))
	buffer.WriteString("\n")

	if configMap == nil {
		return buffer.String()
	}

	keys := make([]string, 0, len(configMap))
	for k := range configMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		v := configMap[k]
		if len(v) == 0 {
			continue
		}
		buffer.WriteString(fmt.Sprintf("%s %s", k, v))
		buffer.WriteString("\n")
	}
	return buffer.String()
}

func RedisConfigMapName(clusterName string) string {
	return fmt.Sprintf("%s-%s", "redis-cluster", clusterName)
}

func NewConfigMapForRestore(cluster *redisv1alpha1.DistributedRedisCluster, labels map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            RestoreConfigMapName(cluster.Name),
			Namespace:       cluster.Namespace,
			Labels:          labels,
			OwnerReferences: redisv1alpha1.DefaultOwnerReferences(cluster),
		},
		Data: map[string]string{
			RestoreSucceeded: strconv.Itoa(0),
		},
	}
}

func RestoreConfigMapName(clusterName string) string {
	return fmt.Sprintf("%s-%s", "rediscluster-restore", clusterName)
}

func AclConfigMapName(clusterName string) string {
	return fmt.Sprintf("%s-%s", "rediscluster-acl", clusterName)
}
func GetAclStringFromSecrets(conflog logr.Logger, client client.Client, cluster *redisv1alpha1.DistributedRedisCluster, secretname string) (string, string, error) {

	aclsecret, versionId, err := GetAcldataFromSecrets(client, cluster, secretname)
	if err != nil {
		conflog.Error(err, "Unable to get ACL data from secrets")
		return "", "", err
	}
	aclString := fmt.Sprintf("user %s on %s #%s", aclsecret["username"], aclsecret["acl"], GetHashedPassword(aclsecret["password"]))
	//conflog.Info("GetAclStringFromSecrets", "aclstring", aclString)

	return aclString, versionId, nil
}

// Extract the auth,acldata from secrets.
func GetAcldataFromSecrets(client client.Client, cluster *redisv1alpha1.DistributedRedisCluster, secretname string) (map[string]string, string, error) {
	values := make(map[string]string)
	secret := &corev1.Secret{}
	err := client.Get(context.TODO(), types.NamespacedName{
		Name:      secretname,
		Namespace: cluster.Namespace,
	}, secret)
	if err != nil {
		return values, "", err
	}
	if len(string(secret.Data["username"])) == 0 ||
		len(string(secret.Data["password"])) == 0 ||
		len(string(secret.Data["acl"])) == 0 {
		return values, secret.ResourceVersion, fmt.Errorf("secret has missing data")
	}
	values["username"] = strings.TrimSpace(string(secret.Data["username"]))
	values["password"] = strings.TrimSpace(string(secret.Data["password"]))
	values["acl"] = strings.TrimSpace(string(secret.Data["acl"]))

	return values, secret.ResourceVersion, nil
}

func GetHashedPassword(password string) string {
	hash := sha256.New()
	hash.Write([]byte(password))

	return fmt.Sprintf("%x", hash.Sum(nil))
}
