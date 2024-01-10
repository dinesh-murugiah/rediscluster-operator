package utils

import (
	"fmt"
	"os"
	"strings"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// AnnotationScope annotation name for defining instance scope. Used for specifying cluster wide clusters.
	// A namespace-scoped operator watches and manages resources in a single namespace, whereas a cluster-scoped operator watches and manages resources cluster-wide.
	AnnotationScope = "redis.kun/scope"
	//AnnotationClusterScoped annotation value for cluster wide clusters.
	AnnotationClusterScoped   = "cluster-scoped"
	AnnotationNamespaceScoped = "namespace-scoped"
)

var isClusterScoped = true
var namespaceList []string
var watchNamespaceEnvVar = "WATCH_NAMESPACE"

func IsClusterScoped() bool {
	return isClusterScoped
}

func GetNamespaceWatchList() ([]string, error) {
	namespaces, found := os.LookupEnv(watchNamespaceEnvVar)
	if !found {
		return nil, fmt.Errorf("%s must be set", watchNamespaceEnvVar)
	} else {
		if strings.Contains(namespaces, ",") {
			return strings.Split(namespaces, ","), nil
		} else {
			return []string{namespaces}, nil
		}
	}

}

func GenerateNamespaceList(log logr.Logger) error {
	var err error
	namespaceList, err = GetNamespaceWatchList()
	if err != nil {
		return err
	}
	log.Info(fmt.Sprintf("Watching namespaces: %v", namespaceList))
	return nil
}

func GetNamespaceList() []string {
	return namespaceList
}

func SetOperatorScope() {
	ns, found := os.LookupEnv(watchNamespaceEnvVar)
	if !found || ns == "" {
		isClusterScoped = true
	} else {
		isClusterScoped = false
	}
}

func ShoudManage(meta metav1.Object) bool {
	if v, ok := meta.GetAnnotations()[AnnotationScope]; ok {
		if IsClusterScoped() {
			return v == AnnotationClusterScoped
		} else {
			if v == AnnotationNamespaceScoped {
				for _, namespace := range namespaceList {
					if meta.GetNamespace() == namespace {
						return true
					}
				}
			}
		}
	} else {
		if !IsClusterScoped() {
			for _, namespace := range namespaceList {
				if meta.GetNamespace() == namespace {
					return true
				}
			}
		}
	}
	return false
}
