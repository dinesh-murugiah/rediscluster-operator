package utils

import (
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

var namespaceList = []string{"test-cluster-dinesh", "test-cluster-hatest"}

var isClusterScoped = false

func IsClusterScoped() bool {
	return isClusterScoped
}

func GetNamespaceList() []string {
	return namespaceList
}

func SetClusterScoped(namespace string) {
	if namespace != "" {
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
