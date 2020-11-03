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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	operatorv1 "github.com/IBM/ibm-namespace-scope-operator/api/v1"
	util "github.com/IBM/ibm-namespace-scope-operator/controllers/common"
	"github.com/IBM/ibm-namespace-scope-operator/controllers/constant"
)

var ctx context.Context

// NamespaceScopeReconciler reconciles a NamespaceScope object
type NamespaceScopeReconciler struct {
	client.Client
	Recorder record.EventRecorder
	Scheme   *runtime.Scheme
}

func (r *NamespaceScopeReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx = context.Background()

	// Fetch the NamespaceScope instance
	instance := &operatorv1.NamespaceScope{}

	if err := r.Client.Get(ctx, req.NamespacedName, instance); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Check if the NamespaceScope instance is marked to be deleted, which is
	// indicated by the deletion timestamp being set.
	if !instance.GetDeletionTimestamp().IsZero() {
		if util.Contains(instance.GetFinalizers(), constant.NamespaceScopeFinalizer) {
			if err := r.DeleteAllRbac(instance); err != nil {
				return ctrl.Result{}, err
			}

			// Remove NamespaceScopeFinalizer. Once all finalizers have been
			// removed, the object will be deleted.
			controllerutil.RemoveFinalizer(instance, constant.NamespaceScopeFinalizer)
			if err := r.Update(ctx, instance); err != nil {
				return ctrl.Result{}, err
			}
		}
		klog.Infof("Finished reconciling NamespaceScope: %s", req.NamespacedName)
		return ctrl.Result{}, nil
	}

	// Add finalizer for this instance
	if !util.Contains(instance.GetFinalizers(), constant.NamespaceScopeFinalizer) {
		if err := r.addFinalizer(instance); err != nil {
			return ctrl.Result{}, err
		}
	}

	instance = setDefaults(instance)

	klog.Infof("Reconciling NamespaceScope: %s", req.NamespacedName)
	if err := r.InitConfigMap(instance); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.PushRbacToNamespace(instance); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.DeleteRbacFromUnmanagedNamespace(instance); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.UpdateConfigMap(instance); err != nil {
		return ctrl.Result{}, err
	}

	klog.Infof("Finished reconciling NamespaceScope: %s", req.NamespacedName)
	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

func (r *NamespaceScopeReconciler) addFinalizer(nss *operatorv1.NamespaceScope) error {
	controllerutil.AddFinalizer(nss, constant.NamespaceScopeFinalizer)
	if err := r.Update(ctx, nss); err != nil {
		klog.Errorf("Failed to update NamespaceScope with finalizer: %v", err)
		return err
	}
	klog.Infof("Added finalizer for the NamespaceScope instance %s/%s", nss.Namespace, nss.Name)
	return nil
}

func (r *NamespaceScopeReconciler) InitConfigMap(instance *operatorv1.NamespaceScope) error {
	cm := &corev1.ConfigMap{}
	cmName := instance.Spec.ConfigmapName
	cmNamespace := instance.Namespace

	if err := r.Get(ctx, types.NamespacedName{Name: cmName, Namespace: cmNamespace}, cm); err != nil {
		// If ConfigMap does not exist, create it
		if errors.IsNotFound(err) {
			cm.Name = cmName
			cm.Namespace = cmNamespace
			cm.Data = make(map[string]string)
			cm.Data["namespaces"] = strings.Join(instance.Spec.NamespaceMembers, ",")
			// Set NamespaceScope instance as the owner of the ConfigMap.
			if err := controllerutil.SetControllerReference(instance, cm, r.Scheme); err != nil {
				klog.Errorf("Failed to set owner reference for ConfigMap %s/%s: %v", cmNamespace, cmName, err)
				return err
			}
			if err := r.Create(ctx, cm); err != nil {
				klog.Errorf("Failed to create ConfigMap %s in namespace %s: %v", cmName, cmNamespace, err)
				return err
			}
			klog.Infof("Created ConfigMap %s in namespace %s", cmName, cmNamespace)
			return nil
		}
		return err
	}

	ownerRefUIDs := util.GetOwnerReferenceUIDs(cm.GetOwnerReferences())
	if len(ownerRefUIDs) != 0 {
		// ConfigMap OwnerReference UIDs don't contain current NamespaceScope instance UID, means this
		// ConfigMap belong to another NamespaceScope instance, stop reconcile.
		if !util.UIDContains(ownerRefUIDs, instance.UID) {
			r.Recorder.Eventf(instance, corev1.EventTypeWarning, "ConfigMap Name Conflict", "ConfigMap %s/%s has belong to another NamesapceScope instance, you need to change to a new configmapName", cmNamespace, cmName)
			klog.Errorf("configMap %s/%s has belong to another NamesapceScope instance, you need to change to a new configmapName", cmNamespace, cmName)
			return fmt.Errorf("configMap %s/%s has belong to another NamesapceScope instance, you need to change to a new configmapName", cmNamespace, cmName)
		}
	} else {
		r.Recorder.Eventf(instance, corev1.EventTypeWarning, "No OwnerReference", "ConfigMap %s/%s has no owner reference, you need to change to a new configmapName", cmNamespace, cmName)
		klog.Errorf("configMap %s/%s has no owner reference, you need to change to a new configmapName", cmNamespace, cmName)
		return fmt.Errorf("configMap %s/%s has no owner reference, you need to change to a new configmapName", cmNamespace, cmName)
	}

	return nil
}

func (r *NamespaceScopeReconciler) UpdateConfigMap(instance *operatorv1.NamespaceScope) error {
	cm := &corev1.ConfigMap{}
	cmKey := types.NamespacedName{Name: instance.Spec.ConfigmapName, Namespace: instance.Namespace}
	if err := r.Get(ctx, cmKey, cm); err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("Not found ConfigMap %s", cmKey.String())
			return nil
		}
		return err
	}

	// If NamespaceMembers changed, update ConfigMap
	if strings.Join(instance.Spec.NamespaceMembers, ",") != cm.Data["namespaces"] {
		cm.Data["namespaces"] = strings.Join(instance.Spec.NamespaceMembers, ",")
		if err := r.Update(ctx, cm); err != nil {
			klog.Errorf("Failed to update ConfigMap %s : %v", cmKey.String(), err)
			return err
		}

		// When the configmap updated, restart all the pods with the RestartLabels
		if err := r.RestartPods(instance.Spec.RestartLabels, instance.Namespace); err != nil {
			return err
		}
	}
	return nil
}

func (r *NamespaceScopeReconciler) PushRbacToNamespace(instance *operatorv1.NamespaceScope) error {
	fromNs := instance.Namespace
	saNames, err := r.GetServiceAccountFromNamespace(instance.Spec.RestartLabels, fromNs)
	if err != nil {
		return err
	}
	labels := map[string]string{
		"projectedfrom": instance.Namespace + "-" + instance.Name,
	}

	for _, toNs := range instance.Spec.NamespaceMembers {
		if err := r.CreateRole(labels, toNs); err != nil {
			if errors.IsForbidden(err) {
				r.Recorder.Eventf(instance, corev1.EventTypeWarning, "Forbidden", "cannot create resource roles in API group rbac.authorization.k8s.io in the namespace %s", toNs)
			}
			return err
		}
		if err := r.CreateUpdateRoleBinding(labels, saNames, fromNs, toNs); err != nil {
			if errors.IsForbidden(err) {
				r.Recorder.Eventf(instance, corev1.EventTypeWarning, "Forbidden", "cannot create resource rolebindings in API group rbac.authorization.k8s.io in the namespace %s", toNs)
			}
			return err
		}
	}
	return nil
}

func (r *NamespaceScopeReconciler) DeleteRbacFromUnmanagedNamespace(instance *operatorv1.NamespaceScope) error {
	cm := &corev1.ConfigMap{}
	cmKey := types.NamespacedName{Name: instance.Spec.ConfigmapName, Namespace: instance.Namespace}
	if err := r.Get(ctx, cmKey, cm); err != nil {
		klog.Errorf("Not found ConfigMap %s", cmKey.String())
		return err
	}

	var nsInCm []string
	if cm.Data["namespaces"] != "" {
		nsInCm = strings.Split(cm.Data["namespaces"], ",")
	}
	nsInCr := instance.Spec.NamespaceMembers
	unmanagedNss := util.GetListDifference(nsInCm, nsInCr)
	labels := map[string]string{
		"projectedfrom": instance.Namespace + "-" + instance.Name,
	}
	for _, toNs := range unmanagedNss {
		if err := r.DeleteRoleBinding(labels, toNs); err != nil {
			if errors.IsForbidden(err) {
				r.Recorder.Eventf(instance, corev1.EventTypeWarning, "Forbidden", "cannot delete resource rolebindings in API group rbac.authorization.k8s.io in the namespace %s", toNs)
				continue
			}
			return err
		}
		if err := r.DeleteRole(labels, toNs); err != nil {
			if errors.IsForbidden(err) {
				r.Recorder.Eventf(instance, corev1.EventTypeWarning, "Forbidden", "cannot delete resource roles in API group rbac.authorization.k8s.io in the namespace %s", toNs)
				continue
			}
			return err
		}
	}
	return nil
}

// When delete NamespaceScope instance, cleanup all RBAC resources
func (r *NamespaceScopeReconciler) DeleteAllRbac(instance *operatorv1.NamespaceScope) error {
	labels := map[string]string{
		"projectedfrom": instance.Namespace + "-" + instance.Name,
	}
	for _, toNs := range instance.Spec.NamespaceMembers {
		if err := r.DeleteRoleBinding(labels, toNs); err != nil {
			if errors.IsForbidden(err) {
				r.Recorder.Eventf(instance, corev1.EventTypeWarning, "Forbidden", "cannot delete resource rolebindings in API group rbac.authorization.k8s.io in the namespace %s", toNs)
				continue
			}
			return err
		}
		if err := r.DeleteRole(labels, toNs); err != nil {
			if errors.IsForbidden(err) {
				r.Recorder.Eventf(instance, corev1.EventTypeWarning, "Forbidden", "cannot delete resource roles in API group rbac.authorization.k8s.io in the namespace %s", toNs)
				continue
			}
			return err
		}
	}
	return nil
}

func (r *NamespaceScopeReconciler) GetServiceAccountFromNamespace(labels map[string]string, namespace string) ([]string, error) {
	pods := &corev1.PodList{}
	opts := []client.ListOption{
		client.MatchingLabels(labels),
		client.InNamespace(namespace),
	}

	if err := r.List(ctx, pods, opts...); err != nil {
		klog.Errorf("Cannot list pods with labels %v in namespace %s: %v", labels, namespace, err)
		return nil, err
	}

	var saNames []string

	for _, pod := range pods.Items {
		if len(pod.Spec.ServiceAccountName) != 0 {
			saNames = append(saNames, pod.Spec.ServiceAccountName)
		}
	}

	saNames = util.ToStringSlice(util.MakeSet(saNames))

	return saNames, nil
}

func (r *NamespaceScopeReconciler) CreateRole(labels map[string]string, toNs string) error {
	name := constant.NamespaceScopeManagedRoleName + labels["projectedfrom"]
	namespace := toNs
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:     []string{"*"},
				APIGroups: []string{"*"},
				Resources: []string{"*"},
			},
		},
	}
	if err := r.Create(ctx, role); err != nil {
		if errors.IsAlreadyExists(err) {
			return nil
		}
		klog.Errorf("Failed to create role %s/%s: %v", namespace, name, err)
		return err
	}
	klog.Infof("Created role %s/%s", namespace, name)
	return nil
}

func (r *NamespaceScopeReconciler) DeleteRole(labels map[string]string, toNs string) error {
	opts := []client.DeleteAllOfOption{
		client.MatchingLabels(labels),
		client.InNamespace(toNs),
	}
	if err := r.DeleteAllOf(ctx, &rbacv1.Role{}, opts...); err != nil {
		klog.Errorf("Failed to delete role with labels %v in namespace %s: %v", labels, toNs, err)
		return err
	}
	klog.Infof("Deleted role with labels %v in namespace %s", labels, toNs)
	return nil
}

func (r *NamespaceScopeReconciler) CreateUpdateRoleBinding(labels map[string]string, saNames []string, fromNs, toNs string) error {
	name := constant.NamespaceScopeManagedRoleBindingName + labels["projectedfrom"]
	namespace := toNs
	subjects := []rbacv1.Subject{}
	for _, saName := range saNames {
		subject := rbacv1.Subject{
			Kind:      "ServiceAccount",
			Name:      saName,
			Namespace: fromNs,
		}
		subjects = append(subjects, subject)
	}
	roleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Subjects: subjects,
		RoleRef: rbacv1.RoleRef{
			Kind:     "Role",
			Name:     constant.NamespaceScopeManagedRoleName + fromNs,
			APIGroup: "rbac.authorization.k8s.io",
		},
	}

	if err := r.Create(ctx, roleBinding); err != nil {
		if errors.IsAlreadyExists(err) {
			if err := r.UpdateRoleBinding(roleBinding); err != nil {
				return err
			}
			return nil
		}
		klog.Errorf("Failed to create rolebinding %s/%s: %v", namespace, name, err)
		return err
	}
	klog.Infof("Created rolebinding %s/%s", namespace, name)
	return nil
}

func (r *NamespaceScopeReconciler) UpdateRoleBinding(newRoleBinding *rbacv1.RoleBinding) error {
	currentRoleBinding := &rbacv1.RoleBinding{}
	currentRoleBindingKey := types.NamespacedName{Name: newRoleBinding.Name, Namespace: newRoleBinding.Namespace}
	if err := r.Get(ctx, currentRoleBindingKey, currentRoleBinding); err != nil {
		klog.Errorf("Cannot get rolebinding %s: %v", currentRoleBindingKey.String(), err)
	}
	if len(newRoleBinding.Subjects) != len(currentRoleBinding.Subjects) {
		if err := r.Update(ctx, newRoleBinding); err != nil {
			klog.Errorf("Failed to update rolebinding %s: %v", currentRoleBindingKey.String(), err)
			return err
		}
		klog.Infof("Updated rolebinding %s", currentRoleBindingKey.String())
		return nil
	}
	return nil
}

func (r *NamespaceScopeReconciler) DeleteRoleBinding(labels map[string]string, toNs string) error {
	opts := []client.DeleteAllOfOption{
		client.MatchingLabels(labels),
		client.InNamespace(toNs),
	}
	if err := r.DeleteAllOf(ctx, &rbacv1.RoleBinding{}, opts...); err != nil {
		klog.Errorf("Failed to delete rolebinding with labels %v in namespace %s: %v", labels, toNs, err)
		return err
	}
	klog.Infof("Deleted rolebinding with labels %v in namespace %s", labels, toNs)
	return nil
}

// Restart pods in specific namespace with the matching labels
func (r *NamespaceScopeReconciler) RestartPods(labels map[string]string, namespace string) error {
	klog.Infof("Restarting pods in namespace %s with matching labels: %v", namespace, labels)
	opts := []client.DeleteAllOfOption{
		client.MatchingLabels(labels),
		client.InNamespace(namespace),
	}
	if err := r.DeleteAllOf(ctx, &corev1.Pod{}, opts...); err != nil {
		klog.Errorf("Failed to restart pods with matching labels %s in namespace %s: %v", labels, namespace, err)
		return err
	}
	return nil
}

func setDefaults(instance *operatorv1.NamespaceScope) *operatorv1.NamespaceScope {
	if instance.Spec.ConfigmapName == "" {
		instance.Spec.ConfigmapName = constant.NamespaceScopeConfigmapName
	}

	return instance
}

func (r *NamespaceScopeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Owns(&corev1.ConfigMap{}).
		For(&operatorv1.NamespaceScope{}).
		Complete(r)
}
