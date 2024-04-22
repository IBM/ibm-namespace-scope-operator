//
// Copyright 2022 IBM Corporation
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package main

import (
	"flag"
	"os"

	olmv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/klog"
	ctrl "sigs.k8s.io/controller-runtime"

	cache "github.com/IBM/controller-filtered-cache/filteredcache"

	operatorv1 "github.com/IBM/ibm-namespace-scope-operator/v4/api/v1"
	"github.com/IBM/ibm-namespace-scope-operator/v4/controllers"
	util "github.com/IBM/ibm-namespace-scope-operator/v4/controllers/common"
	// +kubebuilder:scaffold:imports
)

var (
	scheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(operatorv1.AddToScheme(scheme))
	utilruntime.Must(olmv1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func main() {
	klog.InitFlags(nil)
	defer klog.Flush()
	var metricsAddr string
	var enableLeaderElection bool
	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.Parse()

	gvkLabelMap := map[schema.GroupVersionKind]cache.Selector{
		corev1.SchemeGroupVersion.WithKind("ConfigMap"):   {LabelSelector: "managedby-namespace-scope"},
		rbacv1.SchemeGroupVersion.WithKind("Role"):        {LabelSelector: "namespace-scope-configmap"},
		rbacv1.SchemeGroupVersion.WithKind("RoleBinding"): {LabelSelector: "namespace-scope-configmap"},
	}

	operatorNs, err := util.GetOperatorNamespace()
	if err != nil {
		klog.Error("Failed to get operator namespace: ", err)
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:             scheme,
		MetricsBindAddress: metricsAddr,
		Namespace:          operatorNs,
		Port:               9443,
		LeaderElection:     enableLeaderElection,
		LeaderElectionID:   "6a4a72f9.ibm.com",
		NewCache:           cache.NewFilteredCacheBuilder(gvkLabelMap),
	})
	if err != nil {
		klog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&controllers.NamespaceScopeReconciler{
		Reader:   mgr.GetAPIReader(),
		Client:   mgr.GetClient(),
		Recorder: mgr.GetEventRecorderFor("NamespaceScope"),
		Scheme:   mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		klog.Error(err, "unable to create controller", "controller", "NamespaceScope")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	klog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		klog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
