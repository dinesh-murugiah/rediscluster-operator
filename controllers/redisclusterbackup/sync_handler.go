package redisclusterbackup

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	redisv1alpha1 "github.com/dinesh-murugiah/rediscluster-operator/api/v1alpha1"
	"github.com/dinesh-murugiah/rediscluster-operator/osm"
	utils "github.com/dinesh-murugiah/rediscluster-operator/utils/commonutils"
	"github.com/dinesh-murugiah/rediscluster-operator/utils/event"
	"github.com/dinesh-murugiah/rediscluster-operator/utils/k8sutil"
)

const (
	backupNoRetry = false
	backupRetry   = true
)

func (r *RedisClusterBackupReconciler) create(reqLogger logr.Logger, backup *redisv1alpha1.RedisClusterBackup) (error, bool) {

	if err := r.ValidateBackup(backup); err != nil {
		if k8sutil.IsRequestRetryable(err) {
			return err, backupRetry
		}
		r.markAsFailedBackup(backup, err.Error())
		r.Recorder.Event(
			backup,
			corev1.EventTypeWarning,
			event.BackupFailed,
			err.Error(),
		)
		return nil, backupNoRetry // stop retry
	}

	if backup.Status.StartTime == nil ||
		(backup.Status.Phase == redisv1alpha1.BackupPhaseSucceeded ||
			backup.Status.Phase == redisv1alpha1.BackupPhaseFailed) {
		t := metav1.Now()
		backup.Status.StartTime = &t
		if err := r.CrController.UpdateCRStatus(backup); err != nil {
			r.Recorder.Event(
				backup,
				corev1.EventTypeWarning,
				event.BackupError,
				err.Error(),
			)
			return err, backupRetry
		}
	}

	// Do not process "completed", aka "failed" or "succeeded" or "ignored", backups.
	if backup.Status.Phase == redisv1alpha1.BackupPhaseIgnored {
		// backup.Status.Phase == redisv1alpha1.BackupPhaseFailed ||
		//backup.Status.Phase == redisv1alpha1.BackupPhaseSucceeded {
		delete(backup.GetLabels(), redisv1alpha1.LabelBackupStatus)
		if err := r.CrController.UpdateCR(backup); err != nil {
			r.Recorder.Event(
				backup,
				corev1.EventTypeWarning,
				event.BackupError,
				err.Error(),
			)
			return err, backupRetry
		}
		return nil, backupNoRetry
	}

	running, err := r.isBackupRunning(backup)
	if err != nil {
		r.Recorder.Event(
			backup,
			corev1.EventTypeWarning,
			event.BackupError,
			err.Error(),
		)
		return err, backupRetry
	}
	if running {
		return r.handleBackupJob(reqLogger, backup)
	}

	cluster, err := r.CrController.GetDistributedRedisCluster(backup.Namespace, backup.Spec.RedisClusterName)
	if err != nil {
		r.Recorder.Event(
			backup,
			corev1.EventTypeWarning,
			event.BackupError,
			err.Error(),
		)
		return err, backupRetry
	}

	secret, err := osm.NewRcloneSecret(r.Client, backup.RCloneSecretName(), backup.Namespace, backup.Spec.Backend, []metav1.OwnerReference{
		{
			APIVersion: redisv1alpha1.GroupVersion.String(),
			Kind:       redisv1alpha1.RedisClusterBackupKind,
			Name:       backup.Name,
			UID:        backup.UID,
		},
	})
	if err != nil {
		msg := fmt.Sprintf("Failed to generate rclone secret. Reason: %v", err)
		r.markAsFailedBackup(backup, msg)
		r.Recorder.Event(
			backup,
			corev1.EventTypeWarning,
			event.BackupFailed,
			msg,
		)
		return nil, backupRetry // don't retry
	}

	if err := k8sutil.CreateSecret(r.Client, secret, reqLogger); err != nil {
		r.Recorder.Event(
			backup,
			corev1.EventTypeWarning,
			event.BackupError,
			err.Error(),
		)
		return err, backupRetry
	}

	if backup.Spec.Local == nil {
		if err := osm.CheckBucketAccess(r.Client, backup.Spec.Backend, backup.Namespace); err != nil {
			r.markAsFailedBackup(backup, err.Error())
			r.Recorder.Event(
				backup,
				corev1.EventTypeWarning,
				event.BackupFailed,
				err.Error(),
			)
			return nil, backupRetry
		}
	}

	job, err := r.getBackupJob(reqLogger, backup, cluster)
	if err != nil {
		message := fmt.Sprintf("Failed to create Backup Job. Reason: %v", err)
		r.Recorder.Event(
			backup,
			corev1.EventTypeWarning,
			event.BackupError,
			message,
		)
		if k8sutil.IsRequestRetryable(err) {
			return err, backupRetry
		}
		return r.markAsFailedBackup(backup, message), backupNoRetry
	}

	backup.Status.Phase = redisv1alpha1.BackupPhaseRunning
	backup.Status.MasterSize = cluster.Spec.MasterSize
	backup.Status.ClusterReplicas = cluster.Spec.ClusterReplicas
	backup.Status.ClusterImage = cluster.Spec.Image
	if err := r.CrController.UpdateCRStatus(backup); err != nil {
		r.Recorder.Event(
			backup,
			corev1.EventTypeWarning,
			event.BackupFailed,
			err.Error(),
		)
		return err, backupRetry
	}

	backup.Labels[redisv1alpha1.LabelClusterName] = backup.Spec.RedisClusterName
	backup.Labels[redisv1alpha1.LabelBackupStatus] = string(redisv1alpha1.BackupPhaseRunning)
	if err := r.CrController.UpdateCR(backup); err != nil {
		r.Recorder.Event(
			backup,
			corev1.EventTypeWarning,
			event.BackupError,
			err.Error(),
		)
		return err, backupRetry
	}

	reqLogger.Info("Backup running")
	r.Recorder.Event(
		backup,
		corev1.EventTypeNormal,
		event.Starting,
		"Backup running",
	)

	if err := r.Client.Create(context.TODO(), job); err != nil {
		r.Recorder.Event(
			backup,
			corev1.EventTypeWarning,
			event.BackupError,
			err.Error(),
		)
		return err, backupRetry
	}

	return nil, backupNoRetry
}

func (r *RedisClusterBackupReconciler) ValidateBackup(backup *redisv1alpha1.RedisClusterBackup) error {
	if backup.Labels == nil {
		backup.Labels = make(map[string]string)
	}
	if err := backup.Validate(); err != nil {
		return err
	}

	if _, err := r.CrController.GetDistributedRedisCluster(backup.Namespace, backup.Spec.RedisClusterName); err != nil {
		return err
	}

	return nil
}

func (r *RedisClusterBackupReconciler) getBackupJob(reqLogger logr.Logger, backup *redisv1alpha1.RedisClusterBackup, cluster *redisv1alpha1.DistributedRedisCluster) (*batchv1.Job, error) {
	var ttltime int32 = 0
	var jobcompletions int32 = cluster.Spec.MasterSize
	jobName := backup.JobName()
	jobLabel := map[string]string{
		redisv1alpha1.LabelClusterName:  backup.Spec.RedisClusterName,
		redisv1alpha1.AnnotationJobType: redisv1alpha1.JobTypeBackup,
	}

	persistentVolume, err := r.GetVolumeForBackup(backup, jobName)
	if err != nil {
		return nil, err
	}

	containers, err := r.backupContainers(backup, cluster, reqLogger)
	if err != nil {
		return nil, err
	}

	isController := true
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: backup.Namespace,
			Labels:    jobLabel,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: redisv1alpha1.GroupVersion.String(),
					Kind:       redisv1alpha1.RedisClusterBackupKind,
					Name:       backup.Name,
					UID:        backup.UID,
					Controller: &isController,
				},
			},
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: &ttltime,
			Completions:             &jobcompletions,
			ActiveDeadlineSeconds:   backup.Spec.ActiveDeadlineSeconds,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: containers,
					Volumes: []corev1.Volume{
						{
							Name:         persistentVolume.Name,
							VolumeSource: persistentVolume.VolumeSource,
						},
						{
							Name: "rcloneconfig",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: backup.RCloneSecretName(),
								},
							},
						},
					},
					RestartPolicy: corev1.RestartPolicyNever,
				},
			},
		},
	}
	if backup.Spec.PodSpec != nil {
		job.Spec.Template.Spec.NodeSelector = backup.Spec.PodSpec.NodeSelector
		job.Spec.Template.Spec.Affinity = backup.Spec.PodSpec.Affinity
		job.Spec.Template.Spec.SchedulerName = backup.Spec.PodSpec.SchedulerName
		job.Spec.Template.Spec.Tolerations = backup.Spec.PodSpec.Tolerations
		job.Spec.Template.Spec.PriorityClassName = backup.Spec.PodSpec.PriorityClassName
		job.Spec.Template.Spec.Priority = backup.Spec.PodSpec.Priority
		job.Spec.Template.Spec.SecurityContext = backup.Spec.PodSpec.SecurityContext
		job.Spec.Template.Spec.ImagePullSecrets = backup.Spec.PodSpec.ImagePullSecrets
	}
	if backup.Spec.Backend.Local != nil {
		job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{
			Name:         "local",
			VolumeSource: backup.Spec.Backend.Local.VolumeSource,
		})
	}
	if utils.IsClusterScoped() {
		if job.Annotations == nil {
			job.Annotations = make(map[string]string)
		}
		job.Annotations[utils.AnnotationScope] = utils.AnnotationClusterScoped
	}

	return job, nil
}

func (r *RedisClusterBackupReconciler) backupContainers(backup *redisv1alpha1.RedisClusterBackup, cluster *redisv1alpha1.DistributedRedisCluster, reqLogger logr.Logger) ([]corev1.Container, error) {
	backupSpec := backup.Spec.Backend
	location, err := backupSpec.Location()
	if err != nil {
		return nil, err
	}
	masterNum := int(cluster.Spec.MasterSize)
	reqLogger.Info("backupContainers", "numberof-masters", masterNum)
	containers := make([]corev1.Container, masterNum)
	i := 0
	for _, node := range cluster.Status.Nodes {
		if node.Role == redisv1alpha1.RedisClusterNodeRoleMaster {
			if i == masterNum {
				break
			}
			folderName, err := backup.RemotePath()
			if err != nil {
				r.Recorder.Event(
					backup,
					corev1.EventTypeWarning,
					event.BackupError,
					err.Error(),
				)
				return nil, err
			}
			reqLogger.V(3).Info("backup", "folderName", folderName)
			container := corev1.Container{
				Name:            fmt.Sprintf("%s-%d", redisv1alpha1.JobTypeBackup, i),
				Image:           backup.Spec.Image,
				ImagePullPolicy: "IfNotPresent",
				Args: []string{
					redisv1alpha1.JobTypeBackup,
					fmt.Sprintf(`--data-dir=%s`, redisv1alpha1.BackupDumpDir),
					fmt.Sprintf(`--location=%s`, location),
					fmt.Sprintf(`--host=%s`, node.IP),
					fmt.Sprintf(`--folder=%s`, folderName),
					fmt.Sprintf(`--snapshot=%s-%d`, backup.Name, i),
					"--",
				},
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      redisv1alpha1.UtilVolumeName,
						MountPath: redisv1alpha1.BackupDumpDir,
					},
					{
						Name:      "rcloneconfig",
						ReadOnly:  true,
						MountPath: osm.SecretMountPath,
					},
				},
			}
			if cluster.Spec.AdminSecret != nil {
				container.Env = append(container.Env, redisPassword(cluster))
			} else {
				return nil, fmt.Errorf("missing admin secret for cluster")
			}
			if backup.Spec.Backend.Local != nil {
				container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
					Name:      "local",
					MountPath: backup.Spec.Backend.Local.MountPath,
					SubPath:   backup.Spec.Backend.Local.SubPath,
				})
			}
			if backup.Spec.PodSpec != nil {
				container.Resources = backup.Spec.PodSpec.Resources
				container.LivenessProbe = backup.Spec.PodSpec.LivenessProbe
				container.ReadinessProbe = backup.Spec.PodSpec.ReadinessProbe
				container.Lifecycle = backup.Spec.PodSpec.Lifecycle
			}
			containers[i] = container
			reqLogger.Info("backup_container", "name", containers[i].Name)
			reqLogger.Info("backup_container", "image", containers[i].Image)
			i++
		}

	}

	reqLogger.Info("backupContainers", "NoofContainers", i)

	return containers, nil
}

// GetVolumeForBackup returns pvc or empty directory depending on StorageType.
// In case of PVC, this function will create a PVC then returns the volume.
func (r *RedisClusterBackupReconciler) GetVolumeForBackup(backup *redisv1alpha1.RedisClusterBackup, jobName string) (*corev1.Volume, error) {
	storage := backup.Spec.Storage
	if storage == nil || storage.Type == redisv1alpha1.Ephemeral {
		ed := corev1.EmptyDirVolumeSource{}
		return &corev1.Volume{
			Name: redisv1alpha1.UtilVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &ed,
			},
		}, nil
	}

	volume := &corev1.Volume{
		Name: redisv1alpha1.UtilVolumeName,
	}

	if err := r.createPVCForBackup(backup, jobName); err != nil {
		return nil, err
	}

	volume.PersistentVolumeClaim = &corev1.PersistentVolumeClaimVolumeSource{
		ClaimName: jobName,
	}

	return volume, nil
}

func (r *RedisClusterBackupReconciler) createPVCForBackup(backup *redisv1alpha1.RedisClusterBackup, jobName string) error {
	getClaim := &corev1.PersistentVolumeClaim{}
	err := r.Client.Get(context.TODO(), types.NamespacedName{
		Namespace: backup.Namespace,
		Name:      jobName,
	}, getClaim)
	if err != nil {
		if errors.IsNotFound(err) {
			storage := backup.Spec.Storage
			mode := corev1.PersistentVolumeFilesystem
			pvcSpec := &corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: storage.Size,
					},
				},
				StorageClassName: &storage.Class,
				VolumeMode:       &mode,
			}

			claim := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      jobName,
					Namespace: backup.Namespace,
				},
				Spec: *pvcSpec,
			}
			if storage.DeleteClaim {
				claim.OwnerReferences = []metav1.OwnerReference{
					{
						APIVersion: redisv1alpha1.GroupVersion.String(),
						Kind:       redisv1alpha1.RedisClusterBackupKind,
						Name:       backup.Name,
						UID:        backup.UID,
					},
				}
			}

			return r.Client.Create(context.TODO(), claim)
		}
		return err
	}
	return nil
}

func (r *RedisClusterBackupReconciler) handleBackupJob(reqLogger logr.Logger, backup *redisv1alpha1.RedisClusterBackup) (error, bool) {
	reqLogger.Info("Handle Backup Job")
	jobObj, err := r.JobController.GetJob(backup.Namespace, backup.JobName())
	if err != nil {
		// TODO: Sometimes the job is created successfully, but it cannot be obtained immediately.
		if errors.IsNotFound(err) {
			msg := "One Backup is already Running, Job not found"
			reqLogger.Info(msg, "err", err)
			r.markAsIgnoredBackup(backup, msg)
			delete(backup.GetLabels(), redisv1alpha1.LabelBackupStatus)
			if err := r.CrController.UpdateCR(backup); err != nil {
				r.Recorder.Event(
					backup,
					corev1.EventTypeWarning,
					event.BackupError,
					err.Error(),
				)
				return err, backupRetry
			}
			r.Recorder.Event(
				backup,
				corev1.EventTypeWarning,
				event.BackupFailed,
				msg,
			)
			return nil, backupRetry
		}
		return err, backupRetry
	}
	if !isJobFinished(jobObj) {
		return fmt.Errorf("wait for job Succeeded or Failed"), backupNoRetry
	}

	cluster, err := r.CrController.GetDistributedRedisCluster(backup.Namespace, backup.Spec.RedisClusterName)
	if err != nil {
		r.Recorder.Event(
			backup,
			corev1.EventTypeWarning,
			event.BackupError,
			err.Error(),
		)
		return err, backupRetry
	}

	jobType := jobObj.Status.Conditions[0].Type

	for _, o := range jobObj.OwnerReferences {
		if o.Kind == redisv1alpha1.RedisClusterBackupKind {
			if o.Name == backup.Name {
				jobSucceeded := jobType == batchv1.JobComplete
				if jobSucceeded {
					backup.Status.Phase = redisv1alpha1.BackupPhaseSucceeded
					backup.Status.Reason = "NA"
				} else {
					backup.Status.Phase = redisv1alpha1.BackupPhaseFailed
					backup.Status.Reason = "run batch job failed"
				}
				t := metav1.Now()
				backup.Status.CompletionTime = &t
				if err := r.CrController.UpdateCRStatus(backup); err != nil {
					r.Recorder.Event(
						backup,
						corev1.EventTypeWarning,
						event.BackupError,
						err.Error(),
					)
					return err, backupRetry
				}

				delete(backup.GetLabels(), redisv1alpha1.LabelBackupStatus)
				if err := r.CrController.UpdateCR(backup); err != nil {
					r.Recorder.Event(
						backup,
						corev1.EventTypeWarning,
						event.BackupError,
						err.Error(),
					)
					return err, backupRetry
				}

				if jobSucceeded {
					msg := "Successfully completed backup"
					reqLogger.Info(msg)
					r.Recorder.Event(
						backup,
						corev1.EventTypeNormal,
						event.BackupSuccessful,
						msg,
					)
					r.Recorder.Event(
						cluster,
						corev1.EventTypeNormal,
						event.BackupSuccessful,
						msg,
					)
					return nil, backupRetry
				} else {
					msg := "Failed to complete backup"
					reqLogger.Info(msg)
					r.Recorder.Event(
						backup,
						corev1.EventTypeWarning,
						event.BackupFailed,
						msg,
					)
					r.Recorder.Event(
						cluster,
						corev1.EventTypeWarning,
						event.BackupFailed,
						msg,
					)
					return nil, backupNoRetry
				}
			} else {
				msg := "One Backup is already Running"
				reqLogger.Info(msg, o.Name, backup.Name)
				r.markAsIgnoredBackup(backup, msg)
				delete(backup.GetLabels(), redisv1alpha1.LabelBackupStatus)
				if err := r.CrController.UpdateCR(backup); err != nil {
					r.Recorder.Event(
						backup,
						corev1.EventTypeWarning,
						event.BackupError,
						err.Error(),
					)
					return err, backupNoRetry
				}
				r.Recorder.Event(
					backup,
					corev1.EventTypeWarning,
					event.BackupFailed,
					msg,
				)
			}
			break
		}
	}

	return nil, backupRetry
}
