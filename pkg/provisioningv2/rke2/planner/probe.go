package planner

import (
	"fmt"
	"strings"

	rkev1 "github.com/rancher/rancher/pkg/apis/rke.cattle.io/v1"
	"github.com/rancher/rancher/pkg/apis/rke.cattle.io/v1/plan"
	rancherruntime "github.com/rancher/rancher/pkg/provisioningv2/rke2/runtime"
	"github.com/rancher/wrangler/pkg/data/convert"
	capi "sigs.k8s.io/cluster-api/api/v1alpha4"
)

var allProbes = map[string]plan.Probe{
	"calico": {
		InitialDelaySeconds: 1,
		TimeoutSeconds:      5,
		SuccessThreshold:    1,
		FailureThreshold:    2,
		HTTPGetAction: plan.HTTPGetAction{
			URL: "http://127.0.0.1:9099/liveness",
		},
	},
	"etcd": {
		InitialDelaySeconds: 1,
		TimeoutSeconds:      5,
		SuccessThreshold:    1,
		FailureThreshold:    2,
		HTTPGetAction: plan.HTTPGetAction{
			URL: "http://127.0.0.1:2381/health",
		},
	},
	"kube-apiserver": {
		InitialDelaySeconds: 1,
		TimeoutSeconds:      5,
		SuccessThreshold:    1,
		FailureThreshold:    2,
		HTTPGetAction: plan.HTTPGetAction{
			URL:        "https://127.0.0.1:6443/readyz",
			CACert:     "/var/lib/rancher/%s/server/tls/server-ca.crt",
			ClientCert: "/var/lib/rancher/%s/server/tls/client-kube-apiserver.crt",
			ClientKey:  "/var/lib/rancher/%s/server/tls/client-kube-apiserver.key",
		},
	},
	"kube-scheduler": {
		InitialDelaySeconds: 1,
		TimeoutSeconds:      5,
		SuccessThreshold:    1,
		FailureThreshold:    2,
		HTTPGetAction: plan.HTTPGetAction{
			URL: "https://127.0.0.1:%s/healthz",
		},
	},
	"kube-controller-manager": {
		InitialDelaySeconds: 1,
		TimeoutSeconds:      5,
		SuccessThreshold:    1,
		FailureThreshold:    2,
		HTTPGetAction: plan.HTTPGetAction{
			URL: "https://127.0.0.1:%s/healthz",
		},
	},
	"kubelet": {
		InitialDelaySeconds: 1,
		TimeoutSeconds:      5,
		SuccessThreshold:    1,
		FailureThreshold:    2,
		HTTPGetAction: plan.HTTPGetAction{
			URL: "http://127.0.0.1:10248/healthz",
		},
	},
}

func isCalico(controlPlane *rkev1.RKEControlPlane, runtime string) bool {
	if runtime != rancherruntime.RuntimeRKE2 {
		return false
	}
	cni := convert.ToString(controlPlane.Spec.MachineGlobalConfig.Data["cni"])
	return cni == "" ||
		cni == "calico" ||
		cni == "calico+multus"
}

// renderSecureProbe takes the existing argument value and renders a secure probe using the argument values and an error
// if one occurred.
func renderSecureProbe(arg interface{}, rawProbe plan.Probe, runtime string, defaultSecurePort string, defaultCertDir string, defaultCert string) (plan.Probe, error) {
	securePort := getArgValue(arg, SecurePortArgument, "=")
	if securePort == "" {
		// If the user set a custom --secure-port, set --secure-port to an empty string so we don't override
		// their custom value
		securePort = defaultSecurePort
	}
	TLSCert := getArgValue(arg, TLSCertFileArgument, "=")
	if TLSCert == "" {
		// If the --tls-cert-file Argument was not set in the config for this component, we can look to see if
		// the --cert-dir was set. --tls-cert-file (if set) will take precedence over --cert-dir
		certDir := getArgValue(arg, CertDirArgument, "=")
		if certDir == "" {
			// If --cert-dir was not set, we use defaultCertDir value that was passed in, but must render it to replace
			// the %s for runtime
			certDir = fmt.Sprintf(defaultCertDir, runtime)
		}
		// Our goal here is to generate the tlsCert. If we get to this point, we know we will be using the defaultCert
		TLSCert = certDir + "/" + defaultCert
	}
	return replaceCACertAndPortForProbes(rawProbe, TLSCert, securePort)
}

// addProbes adds probes for the machine (based on type of machine) to the nodePlan and returns the nodePlan and an error
// if one occurred.
func (p *Planner) addProbes(nodePlan plan.NodePlan, controlPlane *rkev1.RKEControlPlane, machine *capi.Machine, config map[string]interface{}) (plan.NodePlan, error) {
	var (
		runtime    = rancherruntime.GetRuntime(controlPlane.Spec.KubernetesVersion)
		probeNames []string
	)

	nodePlan.Probes = map[string]plan.Probe{}

	if runtime != rancherruntime.RuntimeK3S && isEtcd(machine) {
		probeNames = append(probeNames, "etcd")
	}
	if isControlPlane(machine) {
		probeNames = append(probeNames, "kube-apiserver")
		probeNames = append(probeNames, "kube-controller-manager")
		probeNames = append(probeNames, "kube-scheduler")
	}
	if !(IsOnlyEtcd(machine) && runtime == rancherruntime.RuntimeK3S) {
		// k3s doesn't run the kubelet on etcd only nodes
		probeNames = append(probeNames, "kubelet")
	}
	if !IsOnlyEtcd(machine) && isCalico(controlPlane, runtime) {
		probeNames = append(probeNames, "calico")
	}

	for _, probeName := range probeNames {
		nodePlan.Probes[probeName] = allProbes[probeName]
	}

	nodePlan.Probes = replaceRuntimeForProbes(nodePlan.Probes, runtime)

	if isControlPlane(machine) {
		kcmProbe, err := renderSecureProbe(config[KubeControllerManagerArg], nodePlan.Probes["kube-controller-manager"], rancherruntime.GetRuntime(controlPlane.Spec.KubernetesVersion), DefaultKubeControllerManagerDefaultSecurePort, DefaultKubeControllerManagerCertDir, DefaultKubeControllerManagerCert)
		if err != nil {
			return nodePlan, err
		}
		nodePlan.Probes["kube-controller-manager"] = kcmProbe

		ksProbe, err := renderSecureProbe(config[KubeSchedulerArg], nodePlan.Probes["kube-scheduler"], rancherruntime.GetRuntime(controlPlane.Spec.KubernetesVersion), DefaultKubeSchedulerDefaultSecurePort, DefaultKubeSchedulerCertDir, DefaultKubeSchedulerCert)
		if err != nil {
			return nodePlan, err
		}
		nodePlan.Probes["kube-scheduler"] = ksProbe
	}
	return nodePlan, nil
}

// replaceCACertAndPortForProbes adds/replaces the CACert and URL with rendered values based on the values provided.
func replaceCACertAndPortForProbes(probe plan.Probe, cacert, port string) (plan.Probe, error) {
	if cacert == "" || port == "" {
		return plan.Probe{}, fmt.Errorf("CA cert (%s) or port (%s) not defined properly", cacert, port)
	}
	probe.HTTPGetAction.CACert = cacert
	probe.HTTPGetAction.URL = fmt.Sprintf(probe.HTTPGetAction.URL, port)
	return probe, nil
}

func replaceRuntimeForProbes(probes map[string]plan.Probe, runtime string) map[string]plan.Probe {
	result := map[string]plan.Probe{}
	for k, v := range probes {
		v.HTTPGetAction.CACert = replaceRuntime(v.HTTPGetAction.CACert, runtime)
		v.HTTPGetAction.ClientCert = replaceRuntime(v.HTTPGetAction.ClientCert, runtime)
		v.HTTPGetAction.ClientKey = replaceRuntime(v.HTTPGetAction.ClientKey, runtime)
		result[k] = v
	}
	return result
}

func replaceRuntime(str string, runtime string) string {
	if !strings.Contains(str, "%s") {
		return str
	}
	return fmt.Sprintf(str, runtime)
}
