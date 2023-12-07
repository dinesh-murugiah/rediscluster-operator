package k8sutil

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ISecretControl defines the interface that uses to create, update, and delete Secrets.
type ISecretControl interface {
	// CreateSecret creates a Secret in a DistributedRedisCluster.
	CreateSecret(*corev1.Secret) error
	// UpdateSecret updates a Secret in a DistributedRedisCluster.
	UpdateSecret(*corev1.Secret) error
	// DeleteSecret deletes a Secret in a DistributedRedisCluster.
	DeleteSecret(*corev1.Secret) error
	// GetSecret get Secret in a DistributedRedisCluster.
	GetSecret(namespace, name string) (*corev1.Secret, error)
}

type SecretController struct {
	client client.Client
}

// NewRealSecretControl creates a concrete implementation of the
// ISecretControl.
func NewSecretController(client client.Client) ISecretControl {
	return &SecretController{client: client}
}

// CreateSecret implement the ISecretControl.Interface.
func (s *SecretController) CreateSecret(cm *corev1.Secret) error {
	return s.client.Create(context.TODO(), cm)
}

// UpdateSecret implement the ISecretControl.Interface.
func (s *SecretController) UpdateSecret(cm *corev1.Secret) error {
	return s.client.Update(context.TODO(), cm)
}

// DeleteSecret implement the ISecretControl.Interface.
func (s *SecretController) DeleteSecret(cm *corev1.Secret) error {
	return s.client.Delete(context.TODO(), cm)
}

// GetSecret implement the ISecretControl.Interface.
func (s *SecretController) GetSecret(namespace, name string) (*corev1.Secret, error) {
	cm := &corev1.Secret{}
	err := s.client.Get(context.TODO(), types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, cm)
	return cm, err
}
