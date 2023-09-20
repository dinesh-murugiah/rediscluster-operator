package manager

import (
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	redisv1alpha1 "github.com/dinesh-murugiah/rediscluster-operator/api/v1alpha1"
	"github.com/dinesh-murugiah/rediscluster-operator/resources/configmaps"
	"github.com/dinesh-murugiah/rediscluster-operator/resources/poddisruptionbudgets"
	"github.com/dinesh-murugiah/rediscluster-operator/resources/services"
	"github.com/dinesh-murugiah/rediscluster-operator/resources/statefulsets"
	"github.com/dinesh-murugiah/rediscluster-operator/utils/k8sutil"
)

type IEnsureResource interface {
	EnsureRedisStatefulsets(cluster *redisv1alpha1.DistributedRedisCluster, labels map[string]string) (bool, error)
	EnsureRedisHeadLessSvcs(cluster *redisv1alpha1.DistributedRedisCluster, labels map[string]string) error
	EnsureRedisSvc(cluster *redisv1alpha1.DistributedRedisCluster, labels map[string]string) error
	EnsureRedisConfigMap(cluster *redisv1alpha1.DistributedRedisCluster, labels map[string]string) (error, bool)
	EnsureRedisRCloneSecret(cluster *redisv1alpha1.DistributedRedisCluster, labels map[string]string) error
	UpdateRedisStatefulsets(cluster *redisv1alpha1.DistributedRedisCluster, labels map[string]string) error
}

type realEnsureResource struct {
	statefulSetClient k8sutil.IStatefulSetControl
	svcClient         k8sutil.IServiceControl
	configMapClient   k8sutil.IConfigMapControl
	pdbClient         k8sutil.IPodDisruptionBudgetControl
	crClient          k8sutil.ICustomResource
	client            client.Client
	logger            logr.Logger
}

func NewEnsureResource(client client.Client, logger logr.Logger) IEnsureResource {
	return &realEnsureResource{
		statefulSetClient: k8sutil.NewStatefulSetController(client),
		svcClient:         k8sutil.NewServiceController(client),
		configMapClient:   k8sutil.NewConfigMapController(client),
		pdbClient:         k8sutil.NewPodDisruptionBudgetController(client),
		crClient:          k8sutil.NewCRControl(client),
		client:            client,
		logger:            logger,
	}
}

func (r *realEnsureResource) EnsureRedisStatefulsets(cluster *redisv1alpha1.DistributedRedisCluster, labels map[string]string) (bool, error) {
	updated := false
	for i := 0; i < int(cluster.Spec.MasterSize); i++ {
		name := statefulsets.ClusterStatefulSetName(cluster.Name, i)
		svcName := statefulsets.ClusterHeadlessSvcName(cluster.Spec.ServiceName, i)
		// assign label
		labels[redisv1alpha1.StatefulSetLabel] = name
		if stsUpdated, err := r.ensureRedisStatefulset(cluster, name, svcName, labels); err != nil {
			return false, err
		} else if stsUpdated {
			updated = stsUpdated
		}
	}
	return updated, nil
}

func (r *realEnsureResource) ensureRedisStatefulset(cluster *redisv1alpha1.DistributedRedisCluster, ssName, svcName string,
	labels map[string]string) (bool, error) {
	/* Commenting out to support launch in 1.24 k8s
	if err := r.ensureRedisPDB(cluster, ssName, labels); err != nil {
		return false, err
	}
	*/
	ss, err := r.statefulSetClient.GetStatefulSet(cluster.Namespace, ssName)
	if err == nil {
		if shouldUpdateRedis(cluster, ss) {
			r.logger.WithValues("StatefulSet.Namespace", cluster.Namespace, "StatefulSet.Name", ssName).
				Info("updating statefulSet")
			newSS, err := statefulsets.NewStatefulSetForCR(cluster, ssName, svcName, labels)
			if err != nil {
				return false, err
			}
			return true, r.statefulSetClient.UpdateStatefulSet(newSS)
		}
	} else if err != nil && errors.IsNotFound(err) {
		r.logger.WithValues("StatefulSet.Namespace", cluster.Namespace, "StatefulSet.Name", ssName).
			Info("creating a new statefulSet")
		newSS, err := statefulsets.NewStatefulSetForCR(cluster, ssName, svcName, labels)
		if err != nil {
			return false, err
		}
		return false, r.statefulSetClient.CreateStatefulSet(newSS)
	}
	return false, err
}

func shouldUpdateRedis(cluster *redisv1alpha1.DistributedRedisCluster, sts *appsv1.StatefulSet) bool {
	if (cluster.Spec.ClusterReplicas + 1) != *sts.Spec.Replicas {
		return true
	}
	if cluster.Spec.Image != sts.Spec.Template.Spec.Containers[0].Image {
		return true
	}
	/* // TODO FIX it by adding a new user for probes
	if statefulsets.IsPasswordChanged(cluster, sts) {
		return true
	}
	*/
	expectResource := cluster.Spec.Resources
	currentResource := sts.Spec.Template.Spec.Containers[0].Resources
	if result := expectResource.Requests.Memory().Cmp(*currentResource.Requests.Memory()); result != 0 {
		return true
	}
	if result := expectResource.Requests.Cpu().Cmp(*currentResource.Requests.Cpu()); result != 0 {
		return true
	}
	if result := expectResource.Limits.Memory().Cmp(*currentResource.Limits.Memory()); result != 0 {
		return true
	}
	if result := expectResource.Limits.Cpu().Cmp(*currentResource.Limits.Cpu()); result != 0 {
		return true
	}
	return monitorChanged(cluster, sts)
}

func monitorChanged(cluster *redisv1alpha1.DistributedRedisCluster, sts *appsv1.StatefulSet) bool {
	if cluster.Spec.Monitor != nil {
		for _, container := range sts.Spec.Template.Spec.Containers {
			if container.Name == statefulsets.ExporterContainerName {
				return false
			}
		}
		return true
	} else {
		for _, container := range sts.Spec.Template.Spec.Containers {
			if container.Name == statefulsets.ExporterContainerName {
				return true
			}
		}
		return false
	}
}

func (r *realEnsureResource) ensureRedisPDB(cluster *redisv1alpha1.DistributedRedisCluster, name string, labels map[string]string) error {
	_, err := r.pdbClient.GetPodDisruptionBudget(cluster.Namespace, name)
	if err != nil && errors.IsNotFound(err) {
		r.logger.WithValues("PDB.Namespace", cluster.Namespace, "PDB.Name", name).
			Info("creating a new PodDisruptionBudget")
		pdb := poddisruptionbudgets.NewPodDisruptionBudgetForCR(cluster, name, labels)
		return r.pdbClient.CreatePodDisruptionBudget(pdb)
	}
	return err
}

func (r *realEnsureResource) EnsureRedisHeadLessSvcs(cluster *redisv1alpha1.DistributedRedisCluster, labels map[string]string) error {
	for i := 0; i < int(cluster.Spec.MasterSize); i++ {
		svcName := statefulsets.ClusterHeadlessSvcName(cluster.Spec.ServiceName, i)
		name := statefulsets.ClusterStatefulSetName(cluster.Name, i)
		// assign label
		labels[redisv1alpha1.StatefulSetLabel] = name
		if err := r.ensureRedisHeadLessSvc(cluster, svcName, labels); err != nil {
			return err
		}
	}
	return nil
}

func (r *realEnsureResource) ensureRedisHeadLessSvc(cluster *redisv1alpha1.DistributedRedisCluster, name string, labels map[string]string) error {
	_, err := r.svcClient.GetService(cluster.Namespace, name)
	if err != nil && errors.IsNotFound(err) {
		r.logger.WithValues("Service.Namespace", cluster.Namespace, "Service.Name", cluster.Spec.ServiceName).
			Info("creating a new headless service")
		svc := services.NewHeadLessSvcForCR(cluster, name, labels)
		return r.svcClient.CreateService(svc)
	}
	return err
}

func (r *realEnsureResource) EnsureRedisSvc(cluster *redisv1alpha1.DistributedRedisCluster, labels map[string]string) error {
	name := cluster.Spec.ServiceName
	delete(labels, redisv1alpha1.StatefulSetLabel)
	_, err := r.svcClient.GetService(cluster.Namespace, name)
	if err != nil && errors.IsNotFound(err) {
		r.logger.WithValues("Service.Namespace", cluster.Namespace, "Service.Name", cluster.Spec.ServiceName).
			Info("creating a new service")
		svc := services.NewSvcForCR(cluster, name, labels)
		return r.svcClient.CreateService(svc)
	}
	return err
}

func (r *realEnsureResource) EnsureRedisConfigMap(cluster *redisv1alpha1.DistributedRedisCluster, labels map[string]string) (error, bool) {

	cmName := configmaps.RedisConfigMapName(cluster.Name)
	drcCm, err := r.configMapClient.GetConfigMap(cluster.Namespace, cmName)
	if err != nil {
		if errors.IsNotFound(err) {
			r.logger.WithValues("ConfigMap.Namespace", cluster.Namespace, "ConfigMap.Name", cmName).
				Info("creating a new configMap")
			cm := configmaps.NewConfigMapForCR(cluster, labels)

			if err2 := r.configMapClient.CreateConfigMap(cm); err2 != nil {
				return err2, false
			}

		} else {
			return err, false
		}
	} else {
		if isRedisConfChanged(drcCm.Data[configmaps.RedisConfKey], cluster.Spec.Config, r.logger) {
			cm := configmaps.NewConfigMapForCR(cluster, labels)
			if err2 := r.configMapClient.UpdateConfigMap(cm); err2 != nil {
				return err2, false
			}
		}
	}
	aclcmName := configmaps.AclConfigMapName(cluster.Name)
	_, err2 := r.configMapClient.GetConfigMap(cluster.Namespace, aclcmName)
	if err2 != nil {
		if errors.IsNotFound(err) {
			r.logger.WithValues("ConfigMap.Namespace", cluster.Namespace, "ConfigMap.Name", cmName).
				Info("creating a new ACL configMap")
			aclcm, _, conferr, abortcr := configmaps.NewACLConfigMap(r.logger, r.client, cluster, labels)
			if conferr != nil {
				if abortcr {
					return conferr, true
				} else {
					return conferr, false
				}

			}
			if err3 := r.configMapClient.CreateConfigMap(aclcm); err3 != nil {
				return err3, false
			}

			/*
				cluster.Status.SecretsVer = make(map[string]string)
				for key, value := range secretVersions {
					cluster.Status.SecretsVer[key] = value
				}
			*/

		} else {
			return err2, false
		}
	}

	if cluster.IsRestoreFromBackup() {
		restoreCmName := configmaps.RestoreConfigMapName(cluster.Name)
		restoreCm, err := r.configMapClient.GetConfigMap(cluster.Namespace, restoreCmName)
		if err != nil {
			if errors.IsNotFound(err) {
				r.logger.WithValues("ConfigMap.Namespace", cluster.Namespace, "ConfigMap.Name", restoreCmName).
					Info("creating a new restore configMap")
				cm := configmaps.NewConfigMapForRestore(cluster, labels)
				if err1 := r.configMapClient.CreateConfigMap(cm); err1 != nil {
					return err1, false
				}
			} else {
				return err, false
			}
		}
		if cluster.Status.Restore.Phase == redisv1alpha1.RestorePhaseRestart && restoreCm.Data[configmaps.RestoreSucceeded] == "0" {
			cm := configmaps.NewConfigMapForRestore(cluster, labels)
			err := r.configMapClient.UpdateConfigMap(cm)
			if err != nil {
				return err, false
			}
		}
	}
	return nil, false
}

func (r *realEnsureResource) EnsureRedisRCloneSecret(cluster *redisv1alpha1.DistributedRedisCluster, labels map[string]string) error {
	if !cluster.IsRestoreFromBackup() || cluster.IsRestored() {
		return nil
	}
	//Dinesh Todo This flow Needs FIX ####, commented out below to fix status fileds
	err := fmt.Errorf("Restore Not supported yet")
	return err
	/*
		backup := cluster.Status.Restore.Backup
		secret, err := osm.NewRcloneSecret(r.client, backup.RCloneSecretName(), cluster.Namespace, backup.Spec.Backend, redisv1alpha1.DefaultOwnerReferences(cluster))
		if err != nil {
			return err
		}
		if err := k8sutil.CreateSecret(r.client, secret, r.logger); err != nil {
			return err
		}
		return nil
	*/
}

func isRedisConfChanged(confInCm string, currentConf map[string]string, log logr.Logger) bool {
	lines := strings.Split(strings.TrimSuffix(confInCm, "\n"), "\n")
	if len(lines) != len(currentConf) {
		return true
	}
	for _, line := range lines {
		line = strings.TrimSuffix(line, " ")
		confLine := strings.SplitN(line, " ", 2)
		if len(confLine) == 2 {
			if valueInCurrentConf, ok := currentConf[confLine[0]]; !ok {
				return true
			} else {
				if valueInCurrentConf != confLine[1] {
					return true
				}
			}
		} else {
			log.Info("custom config is invalid", "raw", line, "split", confLine)
		}
	}
	return false
}

func (r *realEnsureResource) UpdateRedisStatefulsets(cluster *redisv1alpha1.DistributedRedisCluster, labels map[string]string) error {
	for i := 0; i < int(cluster.Spec.MasterSize); i++ {
		name := statefulsets.ClusterStatefulSetName(cluster.Name, i)
		svcName := statefulsets.ClusterHeadlessSvcName(cluster.Spec.ServiceName, i)
		// assign label
		labels[redisv1alpha1.StatefulSetLabel] = name
		if err := r.updateRedisStatefulset(cluster, name, svcName, labels); err != nil {
			return err
		}
	}
	return nil
}

func (r *realEnsureResource) updateRedisStatefulset(cluster *redisv1alpha1.DistributedRedisCluster, ssName, svcName string,
	labels map[string]string) error {
	r.logger.WithValues("StatefulSet.Namespace", cluster.Namespace, "StatefulSet.Name", ssName).
		Info("updating statefulSet immediately")
	newSS, err := statefulsets.NewStatefulSetForCR(cluster, ssName, svcName, labels)
	if err != nil {
		return err
	}
	return r.statefulSetClient.UpdateStatefulSet(newSS)
}
