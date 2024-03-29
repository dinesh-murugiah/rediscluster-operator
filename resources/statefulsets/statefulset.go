package statefulsets

import (
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	redisv1alpha1 "github.com/dinesh-murugiah/rediscluster-operator/api/v1alpha1"
	config "github.com/dinesh-murugiah/rediscluster-operator/redisconfig"

	"github.com/dinesh-murugiah/rediscluster-operator/resources/configmaps"
	utils "github.com/dinesh-murugiah/rediscluster-operator/utils/commonutils"
)

var log = logf.Log.WithName("resource_statefulset")

const (
	redisStorageVolumeName      = "redis-data"
	redisRestoreLocalVolumeName = "redis-local"
	redisServerName             = "redis"
	zonenameTopologyKey         = "topology.kubernetes.io/zone"
	hostnameTopologyKey         = "kubernetes.io/hostname"
	ExporterContainerName       = "exporter"

	graceTime = 30

	configMapVolumeName       = "conf"
	aclConfigMapVolName       = "acl"
	utilConfigMapVolName      = "utilconf"
	readinessConfigmapVolName = "readyconf"
	livenessConfigmapVolName  = "liveconf"
	startupConfigmapVolName   = "startupconf"
	shutdownConfigmapVolName  = "shutdownconf"
)

// NewStatefulSetForCR creates a new StatefulSet for the given Cluster.
func NewStatefulSetForCR(cluster *redisv1alpha1.DistributedRedisCluster, ssName, svcName string,
	labels map[string]string) (*appsv1.StatefulSet, error) {
	var password *corev1.EnvVar
	volumes := redisVolumes(cluster)
	namespace := cluster.Namespace
	spec := cluster.Spec
	size := spec.ClusterReplicas + 1
	if cluster.Spec.AdminSecret == nil {
		return nil, fmt.Errorf("missing admin secret")
	} else {
		password = redisPassword(cluster)
	}
	terminationGracePeriodSeconds := cluster.Spec.TerminationGracePeriod
	if *cluster.Spec.TerminationGracePeriod > int64(0) {
		terminationGracePeriodSeconds = cluster.Spec.TerminationGracePeriod
	} else {
		*terminationGracePeriodSeconds = int64(30) //take default if not provided
	}

	//Enable affinity and topology spread constraints only if haenabled is true
	var affinity *corev1.Affinity
	var topologySpreadConstraints []corev1.TopologySpreadConstraint
	if cluster.Spec.HaConfig.HaEnabled {
		affinity = getAffinity(cluster, labels)
		topologySpreadConstraints = getTopolgyspreadConstraints(cluster, ssName)
	}

	ss := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            ssName,
			Namespace:       namespace,
			Labels:          labels,
			OwnerReferences: redisv1alpha1.DefaultOwnerReferences(cluster),
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: svcName,
			Replicas:    &size,
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.OnDeleteStatefulSetStrategyType,
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: cluster.Spec.Annotations,
				},
				Spec: corev1.PodSpec{
					ImagePullSecrets:          cluster.Spec.ImagePullSecrets,
					Affinity:                  affinity,
					Tolerations:               spec.ToleRations,
					SecurityContext:           spec.SecurityContext,
					NodeSelector:              cluster.Spec.NodeSelector,
					TopologySpreadConstraints: topologySpreadConstraints,
					Containers: []corev1.Container{
						redisServerContainer(cluster, password),
					},
					Volumes:                       volumes,
					TerminationGracePeriodSeconds: terminationGracePeriodSeconds,
				},
			},
		},
	}

	if spec.Storage != nil && spec.Storage.Type == redisv1alpha1.PersistentClaim {
		ss.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{
			persistentClaim(cluster, labels),
		}
		if spec.Storage.DeleteClaim {
			// set an owner reference so the persistent volumes are deleted when the cluster be deleted.
			ss.Spec.VolumeClaimTemplates[0].OwnerReferences = redisv1alpha1.DefaultOwnerReferences(cluster)
		}
	}
	if spec.Monitor != nil {
		ss.Spec.Template.Spec.Containers = append(ss.Spec.Template.Spec.Containers, redisMonitorContainers(cluster, password)...)
	}

	if spec.InitContainers != nil {
		initContainers := getInitContainersWithRedisEnv(cluster, password)
		ss.Spec.Template.Spec.InitContainers = append(ss.Spec.Template.Spec.InitContainers, initContainers...)
	}

	//if cluster.IsRestoreFromBackup() && cluster.IsRestoreRunning() && cluster.Status.Restore.Backup != nil {
	if cluster.IsRestoreFromBackup() && cluster.IsRestoreRunning() {
		/*
			restoreInitContainer, err := redisRestoreInitContainer(cluster, password)
			if err != nil {
				return nil, err
			}
			ss.Spec.Template.Spec.InitContainers = append(ss.Spec.Template.Spec.InitContainers, restoreInitContainer)
		*/
		//Dinesh Todo This flow Needs FIX ####, commented out below to fix status fileds
		err := fmt.Errorf("Restore Not supported yet")
		return nil, err
	}
	return ss, nil
}

func getTopolgyspreadConstraints(cluster *redisv1alpha1.DistributedRedisCluster, ssname string) []corev1.TopologySpreadConstraint {
	return []corev1.TopologySpreadConstraint{
		{
			MaxSkew:           1,
			TopologyKey:       zonenameTopologyKey,
			WhenUnsatisfiable: corev1.DoNotSchedule,
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"statefulSet": ssname},
			},
		},
		{
			MaxSkew:           1,
			TopologyKey:       hostnameTopologyKey,
			WhenUnsatisfiable: corev1.DoNotSchedule,
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"statefulSet": ssname},
			},
		},
	}
}

func getAffinity(cluster *redisv1alpha1.DistributedRedisCluster, labels map[string]string) *corev1.Affinity {
	affinity := cluster.Spec.Affinity
	if affinity != nil {
		return affinity
	}
	// pod  anti affinity for every pod in a redis cluster on node level (preferred weighted anti-affinity for every pod, mandatady on STS level)
	if cluster.Spec.RequiredAntiAffinity {
		return &corev1.Affinity{
			PodAntiAffinity: &corev1.PodAntiAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
					{
						Weight: 100,
						PodAffinityTerm: corev1.PodAffinityTerm{
							TopologyKey: hostnameTopologyKey,
							LabelSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{redisv1alpha1.LabelClusterName: cluster.Name},
							},
						},
					},
				},
				RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
					{
						TopologyKey: hostnameTopologyKey,
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: labels,
						},
					},
				},
			},
		}
	}
	// return pod  anti-affinity on node level per STS by default
	return &corev1.Affinity{
		PodAntiAffinity: &corev1.PodAntiAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
				{
					TopologyKey: hostnameTopologyKey,
					LabelSelector: &metav1.LabelSelector{
						MatchLabels: labels,
					},
				},
			},
		},
	}
}

func persistentClaim(cluster *redisv1alpha1.DistributedRedisCluster, labels map[string]string) corev1.PersistentVolumeClaim {
	mode := corev1.PersistentVolumeFilesystem
	return corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:   redisStorageVolumeName,
			Labels: labels,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: cluster.Spec.Storage.Size,
				},
			},
			StorageClassName: &cluster.Spec.Storage.Class,
			VolumeMode:       &mode,
		},
	}
}

func ClusterStatefulSetName(clusterName string, i int) string {
	return fmt.Sprintf("drc-%s-%d", clusterName, i)
}

func ClusterHeadlessSvcName(name string, i int) string {
	return fmt.Sprintf("%s-%d", name, i)
}

func getRedisCommand(cluster *redisv1alpha1.DistributedRedisCluster, password *corev1.EnvVar) []string {
	cmd := []string{
		"/conf/fix-ip.sh",
		"redis-server",
		"/conf/redis.conf",
		"--cluster-enabled yes",
		"--cluster-config-file /data/nodes.conf",
	}
	if password != nil {
		cmd = append(cmd, "--requirepass no",
			fmt.Sprintf("--masterauth '$(%s)'", redisv1alpha1.PasswordENV),
			"-- masteruser admin")
	}

	renameCmdMap := utils.BuildCommandReplaceMapping(config.RedisConf().GetRenameCommandsFile(), log)
	mergedCmd := mergeRenameCmds(cluster.Spec.Command, renameCmdMap)

	if len(mergedCmd) > 0 {
		cmd = append(cmd, mergedCmd...)
	}

	return cmd
}

func mergeRenameCmds(userCmds []string, systemRenameCmdMap map[string]string) []string {
	cmds := make([]string, 0)
	for _, cmd := range userCmds {
		splitedCmd := strings.Fields(cmd)
		if len(splitedCmd) == 3 && strings.ToLower(splitedCmd[0]) == "--rename-command" {
			if _, ok := systemRenameCmdMap[splitedCmd[1]]; !ok {
				cmds = append(cmds, cmd)
			}
		} else {
			cmds = append(cmds, cmd)
		}
	}

	renameCmdSlice := make([]string, len(systemRenameCmdMap))
	i := 0
	for key, value := range systemRenameCmdMap {
		c := fmt.Sprintf("--rename-command %s %s", key, value)
		renameCmdSlice[i] = c
		i++
	}
	sort.Strings(renameCmdSlice)
	for _, renameCmd := range renameCmdSlice {
		cmds = append(cmds, renameCmd)
	}

	return cmds
}

func redisServerContainer(cluster *redisv1alpha1.DistributedRedisCluster, password *corev1.EnvVar) corev1.Container {
	container := corev1.Container{
		Name:            redisServerName,
		Image:           cluster.Spec.Image,
		ImagePullPolicy: cluster.Spec.ImagePullPolicy,
		SecurityContext: cluster.Spec.ContainerSecurityContext,
		Ports: []corev1.ContainerPort{
			{
				Name:          "client",
				ContainerPort: 6379,
				Protocol:      corev1.ProtocolTCP,
			},
			{
				Name:          "gossip",
				ContainerPort: 16379,
				Protocol:      corev1.ProtocolTCP,
			},
		},
		VolumeMounts:   volumeMounts(),
		Command:        getRedisCommand(cluster, password),
		LivenessProbe:  &cluster.Spec.CustomLivenessProbe,
		ReadinessProbe: &cluster.Spec.CustomReadinessProbe,
		StartupProbe:   &cluster.Spec.CustomStartupProbe,
		Env: []corev1.EnvVar{
			{
				Name: "POD_IP",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "status.podIP",
					},
				},
			},
		},
		Resources: *cluster.Spec.Resources,
		// TODO store redis data when pod stop
		Lifecycle: &corev1.Lifecycle{
			PostStart: &corev1.LifecycleHandler{
				Exec: &corev1.ExecAction{
					Command: []string{"/bin/sh", "-c", "echo ${REDIS_PASSWORD} > /data/redis_password"},
				},
			},
			PreStop: &corev1.LifecycleHandler{
				Exec: &corev1.ExecAction{
					Command: []string{"/bin/sh", "/shutdown/shutdown.sh"},
				},
			},
		},
	}

	if password != nil {
		container.Env = append(container.Env, *password)
	}

	container.Env = customContainerEnv(container.Env, cluster.Spec.Env)

	return container
}

func redisMonitorContainers(cluster *redisv1alpha1.DistributedRedisCluster, password *corev1.EnvVar) []corev1.Container {
	var containers []corev1.Container
	for _, c := range *cluster.Spec.Monitor {
		container := corev1.Container{
			Name:            c.Name,
			Args:            c.Args,
			Image:           c.Image,
			Command:         c.Command,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Ports: []corev1.ContainerPort{
				{
					Name:          c.Prometheus.Name,
					Protocol:      corev1.ProtocolTCP,
					ContainerPort: c.Prometheus.Port,
				},
			},
			Env:             c.Env,
			Resources:       c.Resources,
			SecurityContext: c.SecurityContext,
		}
		if password != nil {
			container.Env = append(container.Env, *password)
		}

		container.Env = customContainerEnv(container.Env, cluster.Spec.Env)
		containers = append(containers, container)
	}

	return containers
}

func redisRestoreInitContainer(cluster *redisv1alpha1.DistributedRedisCluster, password *corev1.EnvVar) (corev1.Container, error) {
	//Dinesh Todo This flow Needs FIX ####, commented out below to fix status fileds
	err := fmt.Errorf("restore Not supported yet")
	return corev1.Container{}, err
	/*
		backup := cluster.Status.Restore.Backup
		backupSpec := backup.Spec.Backend
		location, err := backupSpec.Location()
		if err != nil {
			return corev1.Container{}, err
		}
		folderName, err := backup.RemotePath()
		if err != nil {
			return corev1.Container{}, err
		}
		log.V(3).Info("restore", "namespaces", cluster.Namespace, "name", cluster.Name, "folderName", folderName)
		container := corev1.Container{
			Name:            redisv1alpha1.JobTypeRestore,
			Image:           backup.Spec.Image,
			ImagePullPolicy: corev1.PullAlways,
			Args: []string{
				redisv1alpha1.JobTypeRestore,
				fmt.Sprintf(`--data-dir=%s`, redisv1alpha1.BackupDumpDir),
				fmt.Sprintf(`--location=%s`, location),
				fmt.Sprintf(`--folder=%s`, folderName),
				fmt.Sprintf(`--snapshot=%s`, backup.Name),
				"--",
			},
			Env: []corev1.EnvVar{
				{
					Name: "POD_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.name",
						},
					},
				},
				{
					Name: "REDIS_RESTORE_SUCCEEDED",
					ValueFrom: &corev1.EnvVarSource{
						ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: configmaps.RestoreConfigMapName(cluster.Name),
							},
							Key: configmaps.RestoreSucceeded,
						},
					},
				},
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      redisStorageVolumeName,
					MountPath: redisv1alpha1.BackupDumpDir,
				},
				{
					Name:      "rcloneconfig",
					ReadOnly:  true,
					MountPath: osm.SecretMountPath,
				},
			},
		}

		if backup.IsRefLocalPVC() {
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
				Name:      redisRestoreLocalVolumeName,
				MountPath: backup.Spec.Backend.Local.MountPath,
				SubPath:   backup.Spec.Backend.Local.SubPath,
				ReadOnly:  true,
			})
		}

		if password != nil {
			container.Env = append(container.Env, *password)
		}

		if backup.Spec.PodSpec != nil {
			container.Resources = backup.Spec.PodSpec.Resources
			container.LivenessProbe = backup.Spec.PodSpec.LivenessProbe
			container.ReadinessProbe = backup.Spec.PodSpec.ReadinessProbe
			container.Lifecycle = backup.Spec.PodSpec.Lifecycle
		}

		container.Env = customContainerEnv(container.Env, cluster.Spec.Env)

		return container, nil
	*/
}

func getInitContainersWithRedisEnv(cluster *redisv1alpha1.DistributedRedisCluster, password *corev1.EnvVar) []corev1.Container {
	initContainers := getContainersWithRedisEnv(cluster.Spec.InitContainers, cluster.Spec.Env, password)

	return initContainers
}

func getContainersWithRedisEnv(cs []corev1.Container, e []corev1.EnvVar, password *corev1.EnvVar) []corev1.Container {
	var containers []corev1.Container
	for _, c := range cs {
		if e != nil {
			c.Env = append(c.Env, e...)
		}
		if password != nil {
			c.Env = append(c.Env, *password)
		}
		containers = append(containers, c)
	}

	return containers
}

func customContainerEnv(env []corev1.EnvVar, customEnv []corev1.EnvVar) []corev1.EnvVar {
	env = append(env, customEnv...)
	return env
}

func volumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{
			Name:      redisStorageVolumeName,
			MountPath: "/data",
		},
		{
			Name:      configMapVolumeName,
			MountPath: "/conf",
		},
		{
			Name:      aclConfigMapVolName,
			MountPath: "/acl",
		},
		{
			Name:      utilConfigMapVolName,
			MountPath: "/config",
		},
		{
			Name:      livenessConfigmapVolName,
			MountPath: "/liveprobe",
		},
		{
			Name:      readinessConfigmapVolName,
			MountPath: "/readyprobe",
		},
		{
			Name:      startupConfigmapVolName,
			MountPath: "/startupprobe",
		},
		{
			Name:      shutdownConfigmapVolName,
			MountPath: "/shutdown",
		},
	}
}

// Returns the REDIS_PASSWORD environment variable.
func redisPassword(cluster *redisv1alpha1.DistributedRedisCluster) *corev1.EnvVar {

	secretName := cluster.Spec.AdminSecret.Name

	return &corev1.EnvVar{
		Name: redisv1alpha1.PasswordENV,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: secretName,
				},
				Key: "password",
			},
		},
	}
}

func redisVolumes(cluster *redisv1alpha1.DistributedRedisCluster) []corev1.Volume {
	executeMode := int32(0755)
	volumes := []corev1.Volume{
		{
			Name: configMapVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: configmaps.RedisConfigMapName(cluster.Name),
					},
					DefaultMode: &executeMode,
				},
			},
		},
		{
			Name: aclConfigMapVolName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: configmaps.AclConfigMapName(cluster.Name),
					},
					DefaultMode: &executeMode,
				},
			},
		},
		{
			Name: utilConfigMapVolName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: cluster.Spec.UtilConfig,
					},
					DefaultMode: &executeMode,
				},
			},
		},
		{
			Name: readinessConfigmapVolName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "readiness-data",
					},
					DefaultMode: &executeMode,
				},
			},
		},
		{
			Name: livenessConfigmapVolName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "liveness-data",
					},
					DefaultMode: &executeMode,
				},
			},
		},
		{
			Name: startupConfigmapVolName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "startup-data",
					},
					DefaultMode: &executeMode,
				},
			},
		},
		{
			Name: shutdownConfigmapVolName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "shutdown-data",
					},
					DefaultMode: &executeMode,
				},
			},
		},
	}

	dataVolume := redisDataVolume(cluster)
	if dataVolume != nil {
		volumes = append(volumes, *dataVolume)
	}

	if !cluster.IsRestoreFromBackup() ||
		//Dinesh Todo This flow Needs FIX ####, commented out below to fix status fileds
		//cluster.Status.Restore.Backup == nil ||
		!cluster.IsRestoreRunning() {
		return volumes
	}
	//Dinesh Todo This flow Needs FIX ####, commented out below to fix status fileds
	/*
		volumes = append(volumes, corev1.Volume{
			Name: "rcloneconfig",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: cluster.Status.Restore.Backup.RCloneSecretName(),
				},
			},
		})

			if cluster.Status.Restore.Backup.IsRefLocalPVC() {
				volumes = append(volumes, corev1.Volume{
					Name:         redisRestoreLocalVolumeName,
					VolumeSource: cluster.Status.Restore.Backup.Spec.Local.VolumeSource,
				})
			}
	*/
	return volumes
}

func emptyVolume() *corev1.Volume {
	return &corev1.Volume{
		Name: redisStorageVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
}
func redisDataVolume(cluster *redisv1alpha1.DistributedRedisCluster) *corev1.Volume {
	// This will find the volumed desired by the user. If no volume defined
	// an EmptyDir will be used by default
	if cluster.Spec.Storage == nil {
		return emptyVolume()
	}

	switch cluster.Spec.Storage.Type {
	case redisv1alpha1.Ephemeral:
		return emptyVolume()
	case redisv1alpha1.PersistentClaim:
		return nil
	default:
		return emptyVolume()
	}
}
