package k8sutil

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// IDeploymentControl define the interface that used to create, update, and delete Deployments.
type IDeploymentControl interface {
	CreateDeployment(*appsv1.Deployment) error
	UpdateDeployment(*appsv1.Deployment) error
	DeleteDeployment(*appsv1.Deployment) error
	GetDeployment(namespace, name string) (*appsv1.Deployment, error)
	ListDeploymentByLabels(namespace string, labs client.MatchingLabels) (*appsv1.DeploymentList, error)
}

type DeploymentController struct {
	client client.Client
}

// NewRealDeploymentControl creates a concrete implementation of the
// IDeploymentControl.
func NewDeploymentController(client client.Client) IDeploymentControl {
	return &DeploymentController{client: client}
}

// CreateDeployment implement the IDeploymentControl.Interface.
func (d *DeploymentController) CreateDeployment(deployment *appsv1.Deployment) error {
	return d.client.Create(context.TODO(), deployment)
}

// UpdateDeployment implement the IDeploymentControl.Interface.
func (d *DeploymentController) UpdateDeployment(deployment *appsv1.Deployment) error {
	return d.client.Update(context.TODO(), deployment)
}

// DeleteDeployment implement the IDeploymentControl.Interface.
func (d *DeploymentController) DeleteDeployment(deployment *appsv1.Deployment) error {
	return d.client.Delete(context.TODO(), deployment)
}

// GetDeployment implement the IDeploymentControl.Interface.
func (d *DeploymentController) GetDeployment(namespace, name string) (*appsv1.Deployment, error) {
	deployment := &appsv1.Deployment{}
	err := d.client.Get(context.TODO(), types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, deployment)
	return deployment, err
}

func (d *DeploymentController) ListDeploymentByLabels(namespace string, labs client.MatchingLabels) (*appsv1.DeploymentList, error) {
	deploymentList := &appsv1.DeploymentList{}
	opts := []client.ListOption{
		client.InNamespace(namespace),
		labs,
	}
	err := d.client.List(context.TODO(), deploymentList, opts...)
	if err != nil {
		return nil, err
	}

	return deploymentList, nil
}
