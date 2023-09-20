package testclient

import (
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

func NewClient(config *rest.Config) (client.Client, error) {
	scheme := runtime.NewScheme()

	// To check its correctness by testing
	_ = clientgoscheme.AddToScheme(scheme)

	mapper, err := apiutil.NewDiscoveryRESTMapper(config)
	if err != nil {
		return nil, err
	}
	options := client.Options{
		Scheme: scheme,
		Mapper: mapper,
	}
	cli, err := client.New(config, options)
	if err != nil {
		return nil, err
	}
	return cli, nil
}
