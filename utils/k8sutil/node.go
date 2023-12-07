package k8sutil

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// IPodControl defines the interface that uses to create, update, and delete Pods.
type INodeControl interface {
	// GetNode with the node name obtained from podspec.
	GetNode(nodename string) (*corev1.Node, error)
	// Get the Zonename of the node. if not found return unknown.
	GetZoneLabel(node *corev1.Node) string
}

type NodeController struct {
	client client.Client
}

// NewNodeController creates a concrete implementation of the
// INodeControl.
func NewNodeController(client client.Client) INodeControl {
	return &NodeController{client: client}
}

// GetNode with the node name obtained from podspec.
func (p *NodeController) GetNode(nodename string) (*corev1.Node, error) {
	node := &corev1.Node{}
	err := p.client.Get(context.TODO(), types.NamespacedName{
		Name: nodename,
	}, node)
	return node, err
}

func (p *NodeController) GetZoneLabel(node *corev1.Node) string {
	if node == nil {
		return "unknown"
	}
	for key, value := range node.Labels {
		if key == "topology.kubernetes.io/zone" {
			return value
		}
	}
	return "unknown"
}
