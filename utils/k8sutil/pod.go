package k8sutil

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// IPodControl defines the interface that uses to create, update, and delete Pods.
type IPodControl interface {
	// CreatePod creates a Pod in a DistributedRedisCluster.
	CreatePod(*corev1.Pod) error
	// UpdatePod updates a Pod in a DistributedRedisCluster.
	UpdatePod(*corev1.Pod) error
	// DeletePod deletes a Pod in a DistributedRedisCluster.
	DeletePod(*corev1.Pod) error
	DeletePodByName(namespace, name string) error
	// GetPod get Pod in a DistributedRedisCluster.
	GetPod(namespace, name string) (*corev1.Pod, error)
	// UpdatePodLabels updates label of a pod in a DistributedRedisCluster.
	UpdatePodLabels(pod *corev1.Pod, labelKey string, labelValue string) error

	ListPodsWithLabel(namespace string, labelkey string, labelvalue string) (*corev1.PodList, error)

	UpdatePodAnnotations(pod *corev1.Pod, annotationKey string, annotationvalue string) error
}

type PodController struct {
	client client.Client
}

// NewPodController creates a concrete implementation of the
// IPodControl.
func NewPodController(client client.Client) IPodControl {
	return &PodController{client: client}
}

// CreatePod implement the IPodControl.Interface.
func (p *PodController) CreatePod(pod *corev1.Pod) error {
	return p.client.Create(context.TODO(), pod)
}

// UpdatePod implement the IPodControl.Interface.
func (p *PodController) UpdatePod(pod *corev1.Pod) error {
	return p.client.Update(context.TODO(), pod)
}

// DeletePod implement the IPodControl.Interface.
func (p *PodController) DeletePod(pod *corev1.Pod) error {
	return p.client.Delete(context.TODO(), pod)
}

// DeletePod implement the IPodControl.Interface.
func (p *PodController) DeletePodByName(namespace, name string) error {
	pod, err := p.GetPod(namespace, name)
	if err != nil {
		return err
	}
	return p.client.Delete(context.TODO(), pod)
}

func (p *PodController) UpdatePodAnnotations(pod *corev1.Pod, annotationKey string, annotationvalue string) error {

	if _, ok := pod.ObjectMeta.Annotations[annotationKey]; !ok {
		err := fmt.Errorf("Annotation %s Notfound", annotationKey)
		return err
	}

	if pod.ObjectMeta.Annotations[annotationKey] == annotationvalue {
		return nil
	} else {
		pod.ObjectMeta.Annotations[annotationKey] = annotationvalue
		return p.UpdatePod(pod)
	}
}

func (p *PodController) UpdatePodLabels(pod *corev1.Pod, labelKey string, labelValue string) error {

	if pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}
	pod.Labels[labelKey] = labelValue
	return p.UpdatePod(pod)
}

// GetPod implement the IPodControl.Interface.
func (p *PodController) GetPod(namespace, name string) (*corev1.Pod, error) {
	pod := &corev1.Pod{}
	err := p.client.Get(context.TODO(), types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, pod)
	return pod, err
}

func (p *PodController) ListPodsWithLabel(namespace string, labelkey string, labelvalue string) (*corev1.PodList, error) {
	podList := &corev1.PodList{}
	err := p.client.List(context.TODO(), podList, &client.ListOptions{
		Namespace: namespace,
		LabelSelector: labels.SelectorFromSet(labels.Set(map[string]string{
			labelkey: labelvalue,
		})),
	})
	return podList, err
}
