package deployments

import (
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	redisv1alpha1 "github.com/dinesh-murugiah/rediscluster-operator/api/v1alpha1"
)

const (
	configMapMountPath = "/config"
	utilContainerName  = "rediscluster-util-container"
)

// UtilNewDeploymentForCR creates a new Deployment for the given Cluster for Utility purpose.
func UtilNewDeploymentForCR(instance *redisv1alpha1.RedisClusterBackup, cluster *redisv1alpha1.DistributedRedisCluster, dpName, secretName string) *appsv1.Deployment {
	deployment := utilGenerateDeploymentSpec(instance, cluster, dpName, secretName)
	return deployment
}

func utilGenerateDeploymentSpec(instance *redisv1alpha1.RedisClusterBackup, cluster *redisv1alpha1.DistributedRedisCluster, dpName, secretName string) *appsv1.Deployment {
	replicas := instance.Spec.UtilSpec.Replicas
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: instance.Namespace,
			Name:      dpName,
			Labels: map[string]string{
				"app":       instance.Spec.RedisClusterName,
				"container": utilContainerName,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: utilGeneratePodTemplate(instance, cluster, secretName),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app":       instance.Spec.RedisClusterName,
					"container": utilContainerName,
				},
			},
		},
	}
	return deployment
}

func utilGeneratePodTemplate(instance *redisv1alpha1.RedisClusterBackup, cluster *redisv1alpha1.DistributedRedisCluster, secretName string) corev1.PodTemplateSpec {
	var TerminationGracePeriodSeconds int64
	var imagePullPolicy corev1.PullPolicy
	if instance.Spec.UtilSpec.ImagePullPolicy == "" {
		imagePullPolicy = corev1.PullAlways
	} else {
		imagePullPolicy = corev1.PullPolicy(instance.Spec.UtilSpec.ImagePullPolicy)
	}
	if *instance.Spec.UtilSpec.TerminationGracePeriod == int64(0) {
		TerminationGracePeriodSeconds = 30
	} else {
		TerminationGracePeriodSeconds = *instance.Spec.UtilSpec.TerminationGracePeriod
	}
	podTemplate := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"app":       instance.Spec.RedisClusterName,
				"container": utilContainerName,
			},
			Annotations: map[string]string{
				"0.prometheus.io/path":   "/metrics",
				"0.prometheus.io/port":   "9999",
				"0.prometheus.io/scrape": "true",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:            utilContainerName,
					Image:           instance.Spec.UtilSpec.Image,
					ImagePullPolicy: imagePullPolicy,
					Command:         []string{instance.Spec.UtilSpec.StartUpCommand},
					Resources:       *instance.Spec.UtilSpec.Resources,
					Ports: []corev1.ContainerPort{
						{
							Name:          "metrics-0",
							ContainerPort: 9999,
						},
					},
					Env: []corev1.EnvVar{
						{
							Name: "REDIS_USER", // Change this to the desired environment variable name
							ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: secretName,
									},
									Key: "username",
								},
							},
						},
						{
							Name: "REDIS_PASSWORD", // Change this to the desired environment variable name
							ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: secretName,
									},
									Key: "password",
								},
							},
						},
						{
							Name:  "REDIS_PORT",
							Value: "6379", //TODO: Change this to the port from the RedisCluster Spec when custom port support is added.
						},
						{
							Name:  "MASTER_SIZE",
							Value: strconv.Itoa(int(cluster.Spec.MasterSize)),
						},
						{
							Name:  "CLUSTER_REPLICAS",
							Value: strconv.Itoa(int(cluster.Spec.ClusterReplicas)),
						},
						{
							Name:  "CLUSTER_NAME",
							Value: instance.Spec.RedisClusterName,
						},
						{
							Name:  "S3_BUCKET",
							Value: instance.Spec.UtilSpec.S3Bucket,
						},
						{
							Name:  "CLUSTER_NAMESPACE",
							Value: instance.ObjectMeta.Namespace,
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "configmap-volume",
							MountPath: configMapMountPath,
						},
						{
							Name:      "redis-data",
							MountPath: "/data",
						},
					},
				},
			},
			Volumes:                       utilGenerateConfigmapVolumeSpec(cluster),
			TerminationGracePeriodSeconds: &TerminationGracePeriodSeconds,
		},
	}
	return podTemplate
}

func utilGenerateConfigmapVolumeSpec(cluster *redisv1alpha1.DistributedRedisCluster) []corev1.Volume {
	executeMode := int32(0755)
	configMapVolume := corev1.Volume{
		Name: "configmap-volume",
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: cluster.Spec.UtilConfig,
				},
				DefaultMode: &executeMode,
			},
		},
	}

	// Define the new emptyDir volume for downloading redis RDB's
	redisDataVolume := corev1.Volume{
		Name: "redis-data",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}

	return []corev1.Volume{
		configMapVolume,
		redisDataVolume,
	}
}
