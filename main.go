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

package main

import (
	"flag"
	"fmt"
	"os"
	goruntime "runtime"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	rediskunv1alpha1 "github.com/dinesh-murugiah/rediscluster-operator/api/v1alpha1"
	"github.com/dinesh-murugiah/rediscluster-operator/controllers/distributedrediscluster"
	clustermanger "github.com/dinesh-murugiah/rediscluster-operator/controllers/manager"
	"github.com/dinesh-murugiah/rediscluster-operator/controllers/redisclusterbackup"
	config2 "github.com/dinesh-murugiah/rediscluster-operator/redisconfig"
	utils "github.com/dinesh-murugiah/rediscluster-operator/utils/commonutils"
	"github.com/dinesh-murugiah/rediscluster-operator/utils/exec"
	"github.com/dinesh-murugiah/rediscluster-operator/utils/k8sutil"
	"github.com/dinesh-murugiah/rediscluster-operator/version"
	"github.com/spf13/pflag"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	//+kubebuilder:scaffold:imports
)

var (
	scheme     = runtime.NewScheme()
	setupLog   = ctrl.Log.WithName("setup")
	clusterLog = ctrl.Log.WithName("controller_distributedrediscluster")
)
var log = logf.Log.WithName("main")

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(rediskunv1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}
func printVersion() {
	log.Info(fmt.Sprintf("Go Version: %s", goruntime.Version()))
	log.Info(fmt.Sprintf("Go OS/Arch: %s/%s", goruntime.GOOS, goruntime.GOARCH))
	//log.Info(fmt.Sprintf("Version of operator-sdk: %v", sdkVersion.Version))
	log.Info(fmt.Sprintf("Version of operator: %s+%s", version.Version, version.GitSHA))
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	pflag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	pflag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	pflag.BoolVar(&enableLeaderElection, "leader-elect", true,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")

	pflag.CommandLine.AddFlagSet(distributedrediscluster.FlagSet())
	pflag.CommandLine.AddFlagSet(redisclusterbackup.FlagSet())

	// Add flags registered by imported packages (e.g. glog and
	// controller-runtime)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)

	config2.RedisConf().AddFlags(pflag.CommandLine)

	pflag.Parse()

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	zlog := zap.New(zap.UseFlagOptions(&opts))
	logf.SetLogger(zlog)

	printVersion()

	utils.SetClusterScoped("")

	ctrl.SetLogger(zlog)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		Port:                   9443,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "9973c3fe.redis.kun",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	distributedredisclusterclient := mgr.GetClient()
	gvk := runtimeschema.GroupVersionKind{
		Group:   "",
		Version: "v1",
		Kind:    "Pod",
	}
	restClient, err := apiutil.RESTClientForGVK(gvk, false, mgr.GetConfig(), serializer.NewCodecFactory(clientgoscheme.Scheme))
	if err != nil {
		os.Exit(1)
	}

	if err = (&distributedrediscluster.DistributedRedisClusterReconciler{
		Client:                distributedredisclusterclient,
		Scheme:                mgr.GetScheme(),
		Ensurer:               clustermanger.NewEnsureResource(distributedredisclusterclient, clusterLog),
		StatefulSetController: k8sutil.NewStatefulSetController(distributedredisclusterclient),
		ServiceController:     k8sutil.NewServiceController(distributedredisclusterclient),
		PdbController:         k8sutil.NewPodDisruptionBudgetController(distributedredisclusterclient),
		PvcController:         k8sutil.NewPvcController(distributedredisclusterclient),
		CrController:          k8sutil.NewCRControl(distributedredisclusterclient),
		Checker:               clustermanger.NewCheck(distributedredisclusterclient, clusterLog),
		Execer:                exec.NewRemoteExec(restClient, mgr.GetConfig(), clusterLog),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "DistributedRedisCluster")
		os.Exit(1)
	}
	if err = (&redisclusterbackup.RedisClusterBackupReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "RedisClusterBackup")
		os.Exit(1)
	}
	if err = (&rediskunv1alpha1.DistributedRedisCluster{}).SetupWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create webhook", "webhook", "DistributedRedisCluster")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	if os.Getenv("ENABLE_WEBHOOKS") == "true" {
		log.Info("Starting the WebHook.")
		ws := mgr.GetWebhookServer()
		ws.CertDir = "/etc/webhook/certs"
		ws.Port = 7443
		if err = (&rediskunv1alpha1.DistributedRedisCluster{}).SetupWebhookWithManager(mgr); err != nil {
			log.Error(err, "unable to create webHook", "webHook", "DistributedRedisCluster")
			os.Exit(1)
		}
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
