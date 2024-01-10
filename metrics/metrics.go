package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

const (
	promControllerSubsystemRedis = "rediscontroller"
	promControllerSubsystemUtil  = "utilcontroller"
	promController               = "maincontroller"
)

type Recorder interface {
	// ClusterOK metrics
	SetClusterOK(namespace string, name string)
	SetControllerError(component string, name string)
	SetWebhookError(component string, name string)
	SetControllerSuccess(component string, name string)
}

type recorder struct {
	clusterOK         *prometheus.GaugeVec // clusterOk is the status of a cluster
	contollerError    *prometheus.GaugeVec // Controller instance creation failure
	webhookError      *prometheus.GaugeVec // Webhook creation failure for rediscontroller
	controllerSuccess *prometheus.GaugeVec // test
}

func NewRecorder(namespace string, reg prometheus.Registerer) Recorder {
	clusterOK := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: promControllerSubsystemRedis,
		Name:      "cluster_ok",
		Help:      "Number of clusters managed by the operator.",
	}, []string{"namespace", "name"})

	contollerError := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Subsystem: promController,
		Name:      "contoller_error",
		Help:      "Controller instance creation failure.",
	}, []string{"component", "name"})

	webhookError := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Subsystem: promController,
		Name:      "webhook_error",
		Help:      "webhook instance creation failure.",
	}, []string{"component", "name"})

	controllerSuccess := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Subsystem: promController,
		Name:      "contoller_success",
		Help:      "controller creation success.",
	}, []string{"component", "name"})

	r := recorder{
		clusterOK:         clusterOK,
		contollerError:    contollerError,
		webhookError:      webhookError,
		controllerSuccess: controllerSuccess,
	}

	// Register metrics.
	reg.MustRegister(
		r.clusterOK,
		r.contollerError,
		r.webhookError,
		r.controllerSuccess,
	)
	return r
}

func (r recorder) SetClusterOK(namespace string, name string) {
	r.clusterOK.WithLabelValues(namespace, name).Set(1)
}

func (r recorder) SetControllerError(component string, name string) {
	r.contollerError.WithLabelValues(component, name).Set(1)
}

func (r recorder) SetWebhookError(component string, name string) {
	r.webhookError.WithLabelValues(component, name).Set(1)
}

func (r recorder) SetControllerSuccess(component string, name string) {
	r.controllerSuccess.WithLabelValues(component, name).Set(1)
}
