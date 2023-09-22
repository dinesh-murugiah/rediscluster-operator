/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/appscode/go/log"
	utils "github.com/dinesh-murugiah/rediscluster-operator/utils/commonutils"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// log is for logging in this package.
var distributedredisclusterlog = logf.Log.WithName("distributedrediscluster-resource")

func (r *DistributedRedisCluster) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

//+kubebuilder:webhook:path=/mutate-redis-kun-redis-kun-v1alpha1-distributedrediscluster,mutating=true,failurePolicy=fail,sideEffects=None,groups=redis.kun.redis.kun,resources=distributedredisclusters,verbs=create;update,versions=v1alpha1,name=mdistributedrediscluster.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &DistributedRedisCluster{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *DistributedRedisCluster) Default() {
	distributedredisclusterlog.Info("default", "name", r.Name)

	// TODO(user): fill in your defaulting logic.
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-redis-kun-redis-kun-v1alpha1-distributedrediscluster,mutating=false,failurePolicy=fail,sideEffects=None,groups=redis.kun.redis.kun,resources=distributedredisclusters,verbs=create;update,versions=v1alpha1,name=vdistributedrediscluster.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &DistributedRedisCluster{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *DistributedRedisCluster) ValidateCreate() (admission.Warnings, error) {
	distributedredisclusterlog.Info("validate create", "name", r.Name)

	if errs := utilvalidation.IsDNS1035Label(r.Spec.ServiceName); len(r.Spec.ServiceName) > 0 && len(errs) > 0 {
		return nil, fmt.Errorf("the custom service is invalid: invalid value: %s, %s", r.Spec.ServiceName, strings.Join(errs, ","))
	}
	/*
		if r.Spec.Resources != nil {
			if errs := validation.ValidateResourceRequirements(r.Spec.Resources, field.NewPath("resources")); len(errs) > 0 {
				return errs.ToAggregate()
			}
		}
	*/
	return nil, nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *DistributedRedisCluster) ValidateUpdate(old runtime.Object) (admission.Warnings, error) {
	distributedredisclusterlog.Info("validate update", "name", r.Name)

	oldObj, ok := old.(*DistributedRedisCluster)
	if !ok {
		err := fmt.Errorf("invalid obj type")
		log.Error(err, "can not reflect type")
		return nil, err
	}

	if errs := utilvalidation.IsDNS1035Label(r.Spec.ServiceName); len(r.Spec.ServiceName) > 0 && len(errs) > 0 {
		return nil, fmt.Errorf("the custom service is invalid: invalid value: %s, %s", r.Spec.ServiceName, strings.Join(errs, ","))
	}
	/*
		if r.Spec.Resources != nil {
			if errs := validation.ValidateResourceRequirements(r.Spec.Resources, field.NewPath("resources")); len(errs) > 0 {
				return errs.ToAggregate()
			}
		}
	*/
	if oldObj.Status.Status == "" {
		return nil, nil
	}
	if compareObj(r, oldObj, distributedredisclusterlog) && oldObj.Status.Status != ClusterStatusOK {
		return nil, fmt.Errorf("redis cluster status: [%s], wait for the status to become %s before operating", oldObj.Status.Status, ClusterStatusOK)
	}
	return nil, nil
}
func compareObj(new, old *DistributedRedisCluster, log logr.Logger) bool {
	if utils.CompareInt32("MasterSize", new.Spec.MasterSize, old.Spec.MasterSize, log) {
		return true
	}

	if utils.CompareStringValue("Image", new.Spec.Image, old.Spec.Image, log) {
		return true
	}

	if !reflect.DeepEqual(new.Spec.Resources, old.Spec.Resources) {
		log.Info("compare resource", "new", new.Spec.Resources, "old", old.Spec.Resources)
		return true
	}

	if !reflect.DeepEqual(new.Spec.AdminSecret, old.Spec.AdminSecret) {
		log.Info("compare password", "new", new.Spec.AdminSecret, "old", old.Spec.AdminSecret)
		return true
	}

	return false
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *DistributedRedisCluster) ValidateDelete() (admission.Warnings, error) {
	distributedredisclusterlog.Info("validate delete", "name", r.Name)

	// TODO(user): fill in your validation logic upon object deletion.
	return nil, nil
}
