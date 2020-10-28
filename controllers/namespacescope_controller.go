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
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	if err := r.ProjectRoles(instance); err != nil {
		return ctrl.Result{}, err
	}

	klog.Infof("Finished reconciling NamespaceScope: %s", req.NamespacedName)
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

func (r *NamespaceScopeReconciler) ProjectRoles(instance *operatorv1.NamespaceScope) error {
	fromNs, err := GetOperatorNamespace()
	if err != nil {
		return err
	}
	saNames, err := r.GetServiceAccountFromNamespace(fromNs)
	if err != nil {
		return err
	}
	if validatedNs, err := r.GetValidatedNamespaces(instance); err != nil {
		return err
	} else {
		for _, toNs := range strings.Split(validatedNs, ",") {
			if err := r.CreateRole(fromNs, toNs); err != nil {
				return err
			}
			for _, saName := range saNames {
				if err := r.CreateRoleBinding(saName, fromNs, toNs); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (r *NamespaceScopeReconciler) GetServiceAccountFromNamespace(namespace string) ([]string, error) {
	sas := &corev1.ServiceAccountList{}
	opts := []client.ListOption{
		client.InNamespace(namespace),
	}
	if err := r.List(ctx, sas, opts...); err != nil {
		return nil, err
	}
	var saNames []string
	for _, sa := range sas.Items {
		if sa.Name == "default" || sa.Name == "deployer" || sa.Name == "builder" {
			continue
		}
		saNames = append(saNames, sa.Name)
	}
	return saNames, nil
}

func (r *NamespaceScopeReconciler) CreateRole(fromNs, toNs string) error {
	name := "role-from-" + fromNs
	namespace := toNs
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"projectedfrom": fromNs,
			},
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:     []string{"*"},
				APIGroups: []string{"*"},
				Resources: []string{"*"},
			},
		},
	}
	if err := r.Create(ctx, role); err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (r *NamespaceScopeReconciler) CreateRoleBinding(saName, fromNs, toNs string) error {
	name := saName + "-in-" + fromNs
	namespace := toNs
	roleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"projectedfrom": fromNs,
			},
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      saName,
				Namespace: fromNs,
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "Role",
			Name:     "role-from-" + fromNs,
			APIGroup: "rbac.authorization.k8s.io",
		},
	}

	if err := r.Create(ctx, roleBinding); err != nil && !errors.IsAlreadyExists(err) {
		return err
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
