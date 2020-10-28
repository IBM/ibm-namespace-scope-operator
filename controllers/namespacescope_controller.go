//
// Copyright 2020 IBM Corporation
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

package controllers

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorv1 "github.com/IBM/ibm-namespace-scope-operator/api/v1"
)

var ctx context.Context

// NamespaceScopeReconciler reconciles a NamespaceScope object
type NamespaceScopeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=operator.ibm.com,resources=namespacescopes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=operator.ibm.com,resources=namespacescopes/status,verbs=get;update;patch

func (r *NamespaceScopeReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx = context.Background()

	// Fetch the NamespaceScope instance
	instance := &operatorv1.NamespaceScope{}

	if err := r.Client.Get(ctx, req.NamespacedName, instance); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	klog.Infof("Reconciling NamespaceScope: %s", req.NamespacedName)
	if err := r.CreateUpdateConfigMap(instance); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *NamespaceScopeReconciler) CreateUpdateConfigMap(instance *operatorv1.NamespaceScope) error {
	cm := &corev1.ConfigMap{}
	cmName := "namespace-scope"
	cmNamespace, err := GetOperatorNamespace()
	if err != nil {
		return err
	}
	err = r.Get(ctx, types.NamespacedName{Name: cmName, Namespace: cmNamespace}, cm)
	if err != nil {
		// If ConfigMap does not exist, create it
		if errors.IsNotFound(err) {
			cm.Name = cmName
			cm.Namespace = cmNamespace
			cm.Data = make(map[string]string)

			if validatedNs, err := r.GetValidatedNamespaces(instance); err != nil {
				return err
			} else {
				cm.Data["namespaces"] = validatedNs
			}

			if err := r.Create(ctx, cm); err != nil {
				return err
			}
			return nil
		}
		return err
	}

	if validatedNs, err := r.GetValidatedNamespaces(instance); err != nil {
		return err
	} else {
		// If NamespaceMembers changed, update ConfigMap
		if validatedNs != cm.Data["namespaces"] {
			cm.Data["namespaces"] = validatedNs
			if err := r.Update(ctx, cm); err != nil {
				return err
			}
		}
	}

	return nil
}

func (r *NamespaceScopeReconciler) GetValidatedNamespaces(instance *operatorv1.NamespaceScope) (string, error) {
	var validatedNs []string
	for _, nsMem := range instance.Spec.NamespaceMembers {
		ns := &corev1.Namespace{}
		key := types.NamespacedName{Name: nsMem}
		if err := r.Get(ctx, key, ns); err != nil {
			if errors.IsNotFound(err) {
				klog.Infof("Namespace %s does not exist and will be ignored", nsMem)
				continue
			} else {
				return "", err
			}
		}
		validatedNs = append(validatedNs, nsMem)
	}
	return strings.Join(validatedNs, ","), nil
}

// GetOperatorNamespace returns the namespace the operator should be running in.
func GetOperatorNamespace() (string, error) {
	ns, found := os.LookupEnv("OPERATOR_NAMESPACE")
	if !found {
		nsBytes, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
		if err != nil {
			if os.IsNotExist(err) {
				return "", fmt.Errorf("namespace not found for current environment")
			}
			return "", err
		}
		ns = strings.TrimSpace(string(nsBytes))
	}
	if len(ns) == 0 {
		return "", fmt.Errorf("operator namespace is empty")
	}
	klog.V(1).Info("Found namespace", "Namespace", ns)
	return ns, nil
}

func (r *NamespaceScopeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&operatorv1.NamespaceScope{}).
		Complete(r)
}
