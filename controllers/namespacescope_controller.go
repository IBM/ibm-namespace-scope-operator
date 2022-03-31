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

package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"time"

	olmv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	operatorv1 "github.com/IBM/ibm-namespace-scope-operator/api/v1"
	util "github.com/IBM/ibm-namespace-scope-operator/controllers/common"
	"github.com/IBM/ibm-namespace-scope-operator/controllers/constant"
)

var ctx context.Context

// NamespaceScopeReconciler reconciles a NamespaceScope object
type NamespaceScopeReconciler struct {
	client.Reader
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
			instance = r.setDefaults(instance)

			if err := r.UpdateConfigMap(instance); err != nil {
				return ctrl.Result{}, err
			}

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

	instance = r.setDefaults(instance)

	klog.Infof("Reconciling NamespaceScope: %s", req.NamespacedName)

	if err := r.UpdateStatus(instance); err != nil {
		klog.Errorf("Failed to update the status of NamespaceScope %s: %v", req.NamespacedName, err)
		return ctrl.Result{}, err
	}

	if err := r.PushRbacToNamespace(instance); err != nil {
		klog.Errorf("Failed to generate rbac: %v", err)
		return ctrl.Result{}, err
	}

	if err := r.DeleteRbacFromUnmanagedNamespace(instance); err != nil {
		klog.Errorf("Failed to delete rbac: %v", err)
		return ctrl.Result{}, err
	}

	if err := r.UpdateConfigMap(instance); err != nil {
		klog.Errorf("Failed to update configmap: %v", err)
		return ctrl.Result{}, err
	}

	for _, namespaceMember := range instance.Spec.NamespaceMembers {
		if rolesList, _ := r.GetRolesFromNamespace(namespaceMember); rolesList != nil && len(rolesList) != 0 {
			var summarizedRules []rbacv1.PolicyRule
			for _, role := range rolesList {
				if role.Name != constant.NamespaceScopeManagedPrefix+instance.Namespace &&
					role.Name != constant.NamespaceScopeRuntimePrefix+instance.Namespace {
					summarizedRules = append(summarizedRules, []rbacv1.PolicyRule(role.Rules)...)
				}
			}

			if err := r.CreateRuntimeRoleToNamespace(instance, namespaceMember, summarizedRules); err != nil {
				klog.Infof("Failed to create runtime role: %v", err)
				return ctrl.Result{}, nil
			}
		}
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

func (r *NamespaceScopeReconciler) UpdateStatus(instance *operatorv1.NamespaceScope) error {
	// Get validated namespaces
	validatedNamespaces, err := r.getValidatedNamespaces(instance)
	if err != nil {
		klog.Errorf("Failed to get validated namespaces: %v", err)
		return err
	}
	// Update instance status with the validated namespaces
	if !util.StringSliceContentEqual(instance.Status.ValidatedMembers, validatedNamespaces) {
		instance.Status.ValidatedMembers = validatedNamespaces
		if err := r.Status().Update(ctx, instance); err != nil {
			klog.Errorf("Failed to update instance %s/%s: %v", instance.Namespace, instance.Name, err)
			return err
		}
	}

	return nil
}

func (r *NamespaceScopeReconciler) UpdateConfigMap(instance *operatorv1.NamespaceScope) error {
	cm := &corev1.ConfigMap{}
	cmName := instance.Spec.ConfigmapName
	cmNamespace := instance.Namespace
	cmKey := types.NamespacedName{Name: cmName, Namespace: cmNamespace}
	validatedMembers, err := r.getAllValidatedNamespaceMembers(instance)
	if err != nil {
		klog.Errorf("Failed to get all validated namespace members: %v", err)
		return err
	}

	if err := r.Client.Get(ctx, cmKey, cm); err != nil {
		if errors.IsNotFound(err) {
			cm.SetName(cmName)
			cm.SetNamespace(cmNamespace)
			cm.SetLabels(map[string]string{constant.NamespaceScopeLabel: "true"})
			cm.Data = map[string]string{"namespaces": strings.Join(validatedMembers, ",")}
			// Set NamespaceScope instance as the owner of the ConfigMap.
			if err := controllerutil.SetOwnerReference(instance, cm, r.Scheme); err != nil {
				klog.Errorf("Failed to set owner reference for ConfigMap %s: %v", cmKey.String(), err)
				return err
			}

			if err := r.Create(ctx, cm); err != nil {
				klog.Errorf("Failed to create ConfigMap %s: %v", cmKey.String(), err)
				return err
			}
			klog.Infof("Created ConfigMap %s", cmKey.String())

			if err := r.RestartPods(instance.Spec.RestartLabels, cm, instance.Namespace); err != nil {
				return err
			}
			return nil
		}
		return err
	}

	// Get owner uids
	ownerRefUIDs := util.GetOwnerReferenceUIDs(cm.GetOwnerReferences())

	if util.CheckListDifference(validatedMembers, strings.Split(cm.Data["namespaces"], ",")) || !util.UIDContains(ownerRefUIDs, instance.UID) {
		restartpod := util.CheckListDifference(validatedMembers, strings.Split(cm.Data["namespaces"], ","))
		if restartpod {
			cm.Data["namespaces"] = strings.Join(validatedMembers, ",")
		}

		if err := controllerutil.SetOwnerReference(instance, cm, r.Scheme); err != nil {
			klog.Errorf("Failed to set owner reference for ConfigMap %s: %v", cmKey.String(), err)
			return err
		}

		if err := r.Update(ctx, cm); err != nil {
			klog.Errorf("Failed to update ConfigMap %s : %v", cmKey.String(), err)
			return err
		}
		klog.Infof("Updated ConfigMap %s", cmKey.String())

		// When the configmap updated, restart all the pods with the RestartLabels
		if restartpod {
			if err := r.RestartPods(instance.Spec.RestartLabels, cm, instance.Namespace); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *NamespaceScopeReconciler) PushRbacToNamespace(instance *operatorv1.NamespaceScope) error {
	fromNs := instance.Namespace
	saNames, err := r.GetServiceAccountFromNamespace(instance, fromNs)
	if err != nil {
		return err
	}

	operatorNs, err := util.GetOperatorNamespace()
	if err != nil {
		klog.Error("get operator namespace failed: ", err)
		return err
	}

	for _, toNs := range instance.Status.ValidatedMembers {
		if toNs == operatorNs {
			continue
		}
		if err := r.generateRBACForNSS(instance, fromNs, toNs); err != nil {
			return err
		}
		if err := r.generateRBACToNamespace(instance, saNames, fromNs, toNs); err != nil {
			return err
		}
	}
	return nil
}

func (r *NamespaceScopeReconciler) CreateRuntimeRoleToNamespace(instance *operatorv1.NamespaceScope, toNs string, summarizedRules []rbacv1.PolicyRule) error {
	fromNs := instance.Namespace

	operatorNs, err := util.GetOperatorNamespace()
	if err != nil {
		klog.Error("get operator namespace failed: ", err)
		return err
	}
	if toNs == operatorNs {
		return nil
	}
	if err := r.generateRuntimeRoleForNSS(instance, summarizedRules, fromNs, toNs); err != nil {
		return err
	}

	return nil
}

func (r *NamespaceScopeReconciler) DeleteRbacFromUnmanagedNamespace(instance *operatorv1.NamespaceScope) error {
	cm := &corev1.ConfigMap{}
	cmKey := types.NamespacedName{Name: instance.Spec.ConfigmapName, Namespace: instance.Namespace}
	if err := r.Client.Get(ctx, cmKey, cm); err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("ConfigMap %s not found", cmKey.String())
			return nil
		}
		klog.Errorf("Failed to get ConfigMap %s: %v", cmKey.String(), err)
		return err
	}

	var nsInCm []string
	if cm.Data["namespaces"] != "" {
		nsInCm = strings.Split(cm.Data["namespaces"], ",")
	}
	nsInCr, err := r.getAllValidatedNamespaceMembers(instance)
	if err != nil {
		return err
	}
	unmanagedNss := util.GetListDifference(nsInCm, nsInCr)
	labels := map[string]string{
		"namespace-scope-configmap": instance.Namespace + "-" + instance.Spec.ConfigmapName,
	}

	operatorNs, err := util.GetOperatorNamespace()
	if err != nil {
		klog.Error("get operator namespace failed: ", err)
		return err
	}

	for _, toNs := range unmanagedNss {
		if toNs == operatorNs {
			continue
		}

		if err := r.DeleteRoleBinding(labels, toNs); err != nil {
			if errors.IsForbidden(err) {
				r.Recorder.Eventf(instance, corev1.EventTypeWarning, "Forbidden", "cannot delete resource rolebindings in API group rbac.authorization.k8s.io in the namespace %s. Please authorize service account ibm-namespace-scope-operator namespace admin permission of %s namespace", toNs, toNs)
				continue
			}
			return err
		}
		if err := r.DeleteRole(labels, toNs); err != nil {
			if errors.IsForbidden(err) {
				r.Recorder.Eventf(instance, corev1.EventTypeWarning, "Forbidden", "cannot delete resource roles in API group rbac.authorization.k8s.io in the namespace %s. Please authorize service account ibm-namespace-scope-operator namespace admin permission of %s namespace", toNs, toNs)
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
		"namespace-scope-configmap": instance.Namespace + "-" + instance.Spec.ConfigmapName,
	}

	operatorNs, err := util.GetOperatorNamespace()
	if err != nil {
		klog.Error("get operator namespace failed: ", err)
		return err
	}

	usingMembers, err := r.getAllValidatedNamespaceMembers(instance)
	if err != nil {
		return err
	}
	deletedMembers := util.GetListDifference(instance.Spec.NamespaceMembers, usingMembers)

	for _, toNs := range deletedMembers {
		if toNs == operatorNs {
			continue
		}
		if err := r.DeleteRoleBinding(labels, toNs); err != nil {
			if errors.IsForbidden(err) {
				r.Recorder.Eventf(instance, corev1.EventTypeWarning, "Forbidden", "cannot delete resource rolebindings in API group rbac.authorization.k8s.io in the namespace %s. Please authorize service account ibm-namespace-scope-operator namespace admin permission of %s namespace", toNs, toNs)
				continue
			}
			return err
		}
		if err := r.DeleteRole(labels, toNs); err != nil {
			if errors.IsForbidden(err) {
				r.Recorder.Eventf(instance, corev1.EventTypeWarning, "Forbidden", "cannot delete resource roles in API group rbac.authorization.k8s.io in the namespace %s. Please authorize service account ibm-namespace-scope-operator namespace admin permission of %s namespace", toNs, toNs)
				continue
			}
			return err
		}
	}
	return nil
}

func (r *NamespaceScopeReconciler) generateRBACForNSS(instance *operatorv1.NamespaceScope, fromNs, toNs string) error {
	labels := map[string]string{
		"namespace-scope-configmap": instance.Namespace + "-" + instance.Spec.ConfigmapName,
	}
	if err := r.createRoleForNSS(labels, fromNs, toNs); err != nil {
		if errors.IsForbidden(err) {
			r.Recorder.Eventf(instance, corev1.EventTypeWarning, "Forbidden", "cannot create resource roles in API group rbac.authorization.k8s.io in the namespace %s. Please authorize service account ibm-namespace-scope-operator namespace admin permission of %s namespace", toNs, toNs)
		}
		return err
	}
	if err := r.createRoleBindingForNSS(labels, fromNs, toNs); err != nil {
		if errors.IsForbidden(err) {
			r.Recorder.Eventf(instance, corev1.EventTypeWarning, "Forbidden", "cannot create resource rolebindings in API group rbac.authorization.k8s.io in the namespace %s. Please authorize service account ibm-namespace-scope-operator namespace admin permission of %s namespace", toNs, toNs)
		}
		return err
	}

	return nil
}

func (r *NamespaceScopeReconciler) generateRuntimeRoleForNSS(instance *operatorv1.NamespaceScope, summarizedRules []rbacv1.PolicyRule, fromNs, toNs string) error {
	labels := map[string]string{
		"namespace-scope-configmap": instance.Namespace + "-" + instance.Spec.ConfigmapName,
	}

	if err := r.createRuntimeRoleForNSS(labels, summarizedRules, fromNs, toNs); err != nil {
		if errors.IsAlreadyExists(err) {
			if err := r.updateRuntimeRoleForNSS(labels, summarizedRules, fromNs, toNs); err != nil {
				return err
			}
			return nil
		}
		if errors.IsForbidden(err) {
			r.Recorder.Eventf(instance, corev1.EventTypeWarning, "Forbidden", "cannot create resource roles in API group rbac.authorization.k8s.io in the namespace %s. Please authorize service account ibm-namespace-scope-operator namespace admin permission of %s namespace", toNs, toNs)
		}
		return err
	}

	return nil
}

func (r *NamespaceScopeReconciler) createRuntimeRoleForNSS(labels map[string]string, summarizedRules []rbacv1.PolicyRule, fromNs, toNs string) error {
	name := constant.NamespaceScopeRuntimePrefix + fromNs
	namespace := toNs
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Rules: summarizedRules,
	}
	if err := r.Create(ctx, role); err != nil {
		if errors.IsAlreadyExists(err) {
			return err
		}
		klog.Errorf("Failed to create role %s/%s: %v", namespace, name, err)
		return err
	}

	klog.Infof("Created role %s/%s", namespace, name)
	return nil
}

func (r *NamespaceScopeReconciler) updateRuntimeRoleForNSS(labels map[string]string, summarizedRules []rbacv1.PolicyRule, fromNs, toNs string) error {
	name := constant.NamespaceScopeRuntimePrefix + fromNs
	namespace := toNs

	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Rules: summarizedRules,
	}

	if err := r.Update(ctx, role); err != nil {
		klog.Errorf("Failed to create role %s/%s: %v", namespace, name, err)
		return err
	}

	klog.Infof("Updated role %s/%s", namespace, name)

	return nil
}

func (r *NamespaceScopeReconciler) createRoleForNSS(labels map[string]string, fromNs, toNs string) error {
	name := constant.NamespaceScopeManagedPrefix + fromNs
	namespace := toNs
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:     []string{"create", "delete", "get", "list", "patch", "update", "watch", "deletecollection"},
				APIGroups: []string{"*"},
				Resources: []string{"*"},
			},
			{
				Verbs:     []string{"escalate", "bind"},
				APIGroups: []string{"rbac.authorization.k8s.io"},
				Resources: []string{"roles"},
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

func (r *NamespaceScopeReconciler) createRoleBindingForNSS(labels map[string]string, fromNs, toNs string) error {
	name := constant.NamespaceScopeManagedPrefix + fromNs
	namespace := toNs
	subjects := []rbacv1.Subject{}
	subject := rbacv1.Subject{
		Kind:      "ServiceAccount",
		Name:      constant.NamespaceScopeServiceAccount,
		Namespace: fromNs,
	}
	subjects = append(subjects, subject)
	roleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Subjects: subjects,
		RoleRef: rbacv1.RoleRef{
			Kind:     "Role",
			Name:     constant.NamespaceScopeManagedPrefix + fromNs,
			APIGroup: "rbac.authorization.k8s.io",
		},
	}

	if err := r.Create(ctx, roleBinding); err != nil {
		if errors.IsAlreadyExists(err) {
			return nil
		}
		klog.Errorf("Failed to create rolebinding %s/%s: %v", namespace, name, err)
		return err
	}
	klog.Infof("Created rolebinding %s/%s", namespace, name)
	return nil
}

func (r *NamespaceScopeReconciler) generateRBACToNamespace(instance *operatorv1.NamespaceScope, saNames []string, fromNs, toNs string) error {
	labels := map[string]string{
		"namespace-scope-configmap": instance.Namespace + "-" + instance.Spec.ConfigmapName,
	}
	for _, sa := range saNames {
		roleList, err := r.GetRolesFromServiceAccount(sa, fromNs)

		klog.V(2).Infof("Roles waiting to be copied: %v", roleList)

		if err != nil {
			return err
		}

		if err := r.CreateRole(roleList, labels, sa, fromNs, toNs); err != nil {
			if errors.IsForbidden(err) {
				r.Recorder.Eventf(instance, corev1.EventTypeWarning, "Forbidden", "cannot create resource roles in API group rbac.authorization.k8s.io in the namespace %s. Please authorize service account ibm-namespace-scope-operator namespace admin permission of %s namespace", toNs, toNs)
			}
			return err
		}
		if err := r.CreateRoleBinding(roleList, labels, sa, fromNs, toNs); err != nil {
			if errors.IsForbidden(err) {
				r.Recorder.Eventf(instance, corev1.EventTypeWarning, "Forbidden", "cannot create resource rolebindings in API group rbac.authorization.k8s.io in the namespace %s. Please authorize service account ibm-namespace-scope-operator namespace admin permission of %s namespace", toNs, toNs)
			}
			return err
		}
	}
	return nil
}

func (r *NamespaceScopeReconciler) GetRolesFromNamespace(namespace string) ([]rbacv1.Role, error) {
	// ibm-common-services-namespace-scope
	rolesListNSS := &rbacv1.RoleList{}
	labels := map[string]string{
		constant.NamespaceScopeConfigmapLabelKey: constant.NamespaceScopeConfigmapNSSLabelValue,
	}

	opts := []client.ListOption{
		client.MatchingLabels(labels),
		client.InNamespace(namespace),
	}
	if err := r.Reader.List(ctx, rolesListNSS, opts...); err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("Roles not found in namespace %s: %v", namespace, err)
			return nil, nil
		}
		klog.Errorf("Cannot list roles with labels %v in namespace %s: %v", labels, namespace, err)
		return nil, err
	}

	// ibm-common-services-odlm-scope
	rolesListODLM := &rbacv1.RoleList{}
	labels = map[string]string{
		constant.NamespaceScopeConfigmapLabelKey: constant.NamespaceScopeConfigmapODLMLabelValue,
	}

	opts = []client.ListOption{
		client.MatchingLabels(labels),
		client.InNamespace(namespace),
	}
	if err := r.Reader.List(ctx, rolesListODLM, opts...); err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("Roles not found in namespace %s: %v", namespace, err)
			return nil, nil
		}
		klog.Errorf("Cannot list roles with labels %v in namespace %s: %v", labels, namespace, err)
		return nil, err
	}

	rolesList := append(rolesListNSS.Items, rolesListODLM.Items...)
	return rolesList, nil
}

func (r *NamespaceScopeReconciler) GetServiceAccountFromNamespace(instance *operatorv1.NamespaceScope, namespace string) ([]string, error) {
	labels := instance.Spec.RestartLabels
	pods := &corev1.PodList{}
	opts := []client.ListOption{
		client.MatchingLabels(labels),
		client.InNamespace(namespace),
	}

	if err := r.Client.List(ctx, pods, opts...); err != nil {
		klog.Errorf("Cannot list pods with labels %v in namespace %s: %v", labels, namespace, err)
		return nil, err
	}

	var saNames []string

	for _, pod := range pods.Items {
		if len(pod.Spec.ServiceAccountName) != 0 {
			saNames = append(saNames, pod.Spec.ServiceAccountName)
		}
	}

	if len(instance.Spec.ServiceAccountMembers) != 0 {
		for _, sa := range instance.Spec.ServiceAccountMembers {
			serviceaccount := &corev1.ServiceAccount{}
			if err := r.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: sa}, serviceaccount); err != nil {
				klog.Errorf("Failed to get service account %s in namespace %s", sa, namespace)
				continue
			}
			saNames = append(saNames, sa)
		}
	}

	saNames = util.ToStringSlice(util.MakeSet(saNames))

	return saNames, nil
}

func (r *NamespaceScopeReconciler) GetRolesFromServiceAccount(sa string, namespace string) ([]string, error) {
	roleBindings := &rbacv1.RoleBindingList{}
	opts := []client.ListOption{
		client.InNamespace(namespace),
	}

	if err := r.Reader.List(ctx, roleBindings, opts...); err != nil {
		klog.Errorf("Cannot list rolebindings with in namespace %s: %v", namespace, err)
		return nil, err
	}

	var roleNameList []string
	for _, roleBinding := range roleBindings.Items {
		for _, subject := range roleBinding.Subjects {
			if subject.Name == sa && subject.Kind == "ServiceAccount" && subject.Namespace == namespace {
				roleNameList = append(roleNameList, roleBinding.RoleRef.Name)
			}
		}
	}

	return util.ToStringSlice(util.MakeSet(roleNameList)), nil
}

func (r *NamespaceScopeReconciler) CreateRole(roleNames []string, labels map[string]string, saName, fromNs, toNs string) error {
	for _, roleName := range roleNames {
		originalRole := &rbacv1.Role{}
		if err := r.Reader.Get(ctx, types.NamespacedName{Name: roleName, Namespace: fromNs}, originalRole); err != nil {
			if errors.IsNotFound(err) {
				klog.Errorf("role %s not found in namespace %s: %v", roleName, fromNs, err)
				continue
			}
			klog.Errorf("Failed to get role %s in namespace %s: %v", roleName, fromNs, err)
			return err
		}
		hashedServiceAccount := sha256.Sum256([]byte(roleName + saName + fromNs))
		name := strings.Split(roleName, ".")[0] + "-" + hex.EncodeToString(hashedServiceAccount[:7])
		namespace := toNs
		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels:    labels,
			},
			Rules: originalRole.Rules,
		}
		if err := r.Create(ctx, role); err != nil {
			if errors.IsAlreadyExists(err) {
				if err := r.Update(ctx, role); err != nil {
					klog.Errorf("Failed to update role %s/%s: %v", namespace, name, err)
					return err
				}
				continue
			}
			klog.Errorf("Failed to create role %s/%s: %v", namespace, name, err)
			return err
		}
		klog.Infof("Created role %s/%s", namespace, name)
	}
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

func (r *NamespaceScopeReconciler) CreateRoleBinding(roleNames []string, labels map[string]string, saName, fromNs, toNs string) error {
	for _, roleName := range roleNames {
		hashedServiceAccount := sha256.Sum256([]byte(roleName + saName + fromNs))
		name := strings.Split(roleName, ".")[0] + "-" + hex.EncodeToString(hashedServiceAccount[:7])
		namespace := toNs
		subjects := []rbacv1.Subject{}
		subject := rbacv1.Subject{
			Kind:      "ServiceAccount",
			Name:      saName,
			Namespace: fromNs,
		}
		subjects = append(subjects, subject)
		roleBinding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels:    labels,
			},
			Subjects: subjects,
			RoleRef: rbacv1.RoleRef{
				Kind:     "Role",
				Name:     name,
				APIGroup: "rbac.authorization.k8s.io",
			},
		}

		if err := r.Create(ctx, roleBinding); err != nil {
			if errors.IsAlreadyExists(err) {
				return nil
			}
			klog.Errorf("Failed to create rolebinding %s/%s: %v", namespace, name, err)
			return err
		}
		klog.Infof("Created rolebinding %s/%s", namespace, name)
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
func (r *NamespaceScopeReconciler) RestartPods(labels map[string]string, cm *corev1.ConfigMap, namespace string) error {
	klog.Infof("Restarting pods in namespace %s with matching labels: %v", namespace, labels)
	opts := []client.ListOption{
		client.MatchingLabels(labels),
		client.InNamespace(namespace),
	}
	podList := &corev1.PodList{}
	if err := r.Client.List(ctx, podList, opts...); err != nil {
		klog.Errorf("Failed to list pods with matching labels %s in namespace %s: %v", labels, namespace, err)
		return err
	}

	deploymentNameList := make([]string, 0)
	daemonSetNameList := make([]string, 0)
	statefulSetNameList := make([]string, 0)
	for _, pod := range podList.Items {
		// Check if the pod is required to be refreshed
		if !needRestart(pod, cm.Name) {
			continue
		}
		// Delete operator pod to refresh it
		podAnno := pod.GetAnnotations()
		_, ok := podAnno["olm.operatorGroup"]
		if ok {
			if err := r.Client.Delete(ctx, &pod); err != nil {
				klog.Errorf("Failed to delete pods %s in namespace %s: %v", pod.Name, pod.Namespace, err)
				return err
			}
			continue
		}
		// Rolling update operand pods
		ownerReferences := pod.GetOwnerReferences()
		for _, or := range ownerReferences {
			if !*or.Controller {
				continue
			}
			switch or.Kind {
			case "ReplicaSet":
				splitedDN := strings.Split(or.Name, "-")
				deploymentName := strings.Join(splitedDN[:len(splitedDN)-1], "-")
				deploymentNameList = append(deploymentNameList, deploymentName)
			case "DaemonSet":
				daemonSetNameList = append(daemonSetNameList, or.Name)
			case "StatefulSet":
				statefulSetNameList = append(statefulSetNameList, or.Name)
			}
		}
	}

	hashedData := sha256.Sum256([]byte(cm.Data["namespaces"]))
	annotationValue := hex.EncodeToString(hashedData[0:7])

	// Refresh pods from deployment
	deploymentNameList = util.ToStringSlice(util.MakeSet(deploymentNameList))
	for _, deploymentName := range deploymentNameList {
		deploy := &appsv1.Deployment{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, deploy); err != nil {
			klog.Errorf("Failed to get deployment %s in namespace %s: %v", deploymentName, namespace, err)
			return err
		}
		if deploy.Spec.Template.Annotations == nil {
			deploy.Spec.Template.Annotations = make(map[string]string)
		}
		deploy.Spec.Template.Annotations["nss.ibm.com/namespaceList"] = annotationValue
		if err := r.Update(ctx, deploy); err != nil {
			klog.Errorf("Failed to update the annotation of the deployment %s in namespace %s: %v", deploymentName, namespace, err)
			return err
		}
	}

	// Refresh pods from daemonSet
	daemonSetNameList = util.ToStringSlice(util.MakeSet(daemonSetNameList))
	for _, daemonSetName := range daemonSetNameList {
		daemonSet := &appsv1.DaemonSet{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: daemonSetName, Namespace: namespace}, daemonSet); err != nil {
			klog.Errorf("Failed to get daemonSet %s in namespace %s: %v", daemonSetName, namespace, err)
			return err
		}
		originalDaemonSet := daemonSet
		if daemonSet.Spec.Template.Annotations == nil {
			daemonSet.Spec.Template.Annotations = make(map[string]string)
		}
		daemonSet.Spec.Template.Annotations["nss.ibm.com/namespaceList"] = annotationValue
		if err := r.Patch(ctx, daemonSet, client.MergeFrom(originalDaemonSet)); err != nil {
			klog.Errorf("Failed to update the annotation of the daemonSet %s in namespace %s: %v", daemonSetName, namespace, err)
			return err
		}
	}

	// Refresh pods from statefulSet
	statefulSetNameList = util.ToStringSlice(util.MakeSet(statefulSetNameList))
	for _, statefulSetName := range statefulSetNameList {
		statefulSet := &appsv1.StatefulSet{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: statefulSetName, Namespace: namespace}, statefulSet); err != nil {
			klog.Errorf("Failed to get statefulSet %s in namespace %s: %v", statefulSetName, namespace, err)
			return err
		}
		originalStatefulSet := statefulSet
		if statefulSet.Spec.Template.Annotations == nil {
			statefulSet.Spec.Template.Annotations = make(map[string]string)
		}
		statefulSet.Spec.Template.Annotations["nss.ibm.com/namespaceList"] = annotationValue
		if err := r.Patch(ctx, statefulSet, client.MergeFrom(originalStatefulSet)); err != nil {
			klog.Errorf("Failed to update the annotation of the statefulSet %s in namespace %s: %v", statefulSetName, namespace, err)
			return err
		}
	}
	return nil
}

func needRestart(pod corev1.Pod, cmName string) bool {
	for _, volume := range pod.Spec.Volumes {
		if volume.ConfigMap != nil && volume.ConfigMap.Name == cmName {
			return true
		}
	}
	for _, container := range pod.Spec.Containers {
		for _, env := range container.Env {
			if env.ValueFrom != nil && env.ValueFrom.ConfigMapKeyRef != nil && env.ValueFrom.ConfigMapKeyRef.Name == cmName {
				return true
			}
		}
	}
	return false
}

func (r *NamespaceScopeReconciler) setDefaults(instance *operatorv1.NamespaceScope) *operatorv1.NamespaceScope {
	if instance.Spec.ConfigmapName == "" {
		instance.Spec.ConfigmapName = constant.NamespaceScopeConfigmapName
	}
	if len(instance.Spec.RestartLabels) == 0 {
		instance.Spec.RestartLabels = map[string]string{
			constant.DefaultRestartLabelsKey: constant.DefaultRestartLabelsValue,
		}
	}
	return instance
}

func (r *NamespaceScopeReconciler) getAllValidatedNamespaceMembers(instance *operatorv1.NamespaceScope) ([]string, error) {
	// List the instance using the same configmap
	crList := &operatorv1.NamespaceScopeList{}
	namespaceMembers := []string{}
	if err := r.Client.List(ctx, crList, &client.ListOptions{Namespace: instance.Namespace}); err != nil {
		klog.Errorf("Cannot list namespacescope with in namespace %s: %v", instance.Namespace, err)
		return nil, err
	}
	for i := range crList.Items {
		cr := r.setDefaults(&crList.Items[i])
		if !cr.GetDeletionTimestamp().IsZero() {
			continue
		}
		if instance.Spec.ConfigmapName == cr.Spec.ConfigmapName {
			namespaceMembers = append(namespaceMembers, cr.Status.ValidatedMembers...)
		}
	}
	return util.ToStringSlice(util.MakeSet(namespaceMembers)), nil
}

func (r *NamespaceScopeReconciler) checkGetNSAuth() bool {
	sar := &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Verb:     "get",
				Group:    "",
				Resource: "namespaces",
			},
		},
	}

	if err := r.Create(ctx, sar); err != nil {
		klog.Errorf("Failed to check if operator has permission to get namespace: %v", err)
		return false
	}
	klog.V(2).Infof("Get Namespace permission, Allowed: %t, Denied: %t, Reason: %s", sar.Status.Allowed, sar.Status.Denied, sar.Status.Reason)
	return sar.Status.Allowed
}

// Check if operator has namespace admin permission
func (r *NamespaceScopeReconciler) checkNamespaceAdminAuth(namespace string) bool {
	verbs := []string{"create", "delete", "get", "list", "patch", "update", "watch", "deletecollection"}
	for _, verb := range verbs {
		sar := &authorizationv1.SelfSubjectAccessReview{
			Spec: authorizationv1.SelfSubjectAccessReviewSpec{
				ResourceAttributes: &authorizationv1.ResourceAttributes{
					Namespace: namespace,
					Verb:      verb,
					Group:     "*",
					Resource:  "*",
				},
			},
		}
		if err := r.Create(ctx, sar); err != nil {
			klog.Errorf("Failed to check operator namespace permission: %v", err)
			return false
		}

		klog.V(2).Infof("Namespace admin permission in namespace %s, Allowed: %t, Denied: %t, Reason: %s", namespace, sar.Status.Allowed, sar.Status.Denied, sar.Status.Reason)

		if !sar.Status.Allowed {
			return false
		}
	}
	roleVerbs := []string{"escalate", "bind"}
	for _, verb := range roleVerbs {
		sar := &authorizationv1.SelfSubjectAccessReview{
			Spec: authorizationv1.SelfSubjectAccessReviewSpec{
				ResourceAttributes: &authorizationv1.ResourceAttributes{
					Namespace: namespace,
					Verb:      verb,
					Group:     "rbac.authorization.k8s.io",
					Resource:  "roles",
				},
			},
		}
		if err := r.Create(ctx, sar); err != nil {
			klog.Errorf("Failed to check operator namespace permission: %v", err)
			return false
		}

		klog.V(2).Infof("Namespace admin permission in namespace %s, Allowed: %t, Denied: %t, Reason: %s", namespace, sar.Status.Allowed, sar.Status.Denied, sar.Status.Reason)

		if !sar.Status.Allowed {
			return false
		}
	}
	return true
}

func (r *NamespaceScopeReconciler) getValidatedNamespaces(instance *operatorv1.NamespaceScope) ([]string, error) {
	var validatedNs []string
	operatorNs, err := util.GetOperatorNamespace()
	if err != nil {
		klog.Error("get operator namespace failed: ", err)
		return validatedNs, err
	}
	for _, nsMem := range instance.Spec.NamespaceMembers {
		if nsMem == operatorNs {
			validatedNs = append(validatedNs, nsMem)
			continue
		}
		// Check if operator has target namespace admin permission
		if r.checkNamespaceAdminAuth(nsMem) {
			// Check if operator has permission to get namespace resource
			if r.checkGetNSAuth() {
				ns := &corev1.Namespace{}
				key := types.NamespacedName{Name: nsMem}
				if err := r.Client.Get(ctx, key, ns); err != nil {
					if errors.IsNotFound(err) {
						klog.Infof("Namespace %s does not exist and will be ignored", nsMem)
						continue
					} else {
						return nil, err
					}
				}
				if ns.Status.Phase == corev1.NamespaceTerminating {
					klog.Infof("Namespace %s is terminating. Ignore this namespace", nsMem)
					continue
				}
			}
			validatedNs = append(validatedNs, nsMem)
		} else {
			klog.Infof("ibm-namespace-scope-operator doesn't have admin permission in namespace %s", nsMem)
			klog.Infof("NOTE: Please refer to https://ibm.biz/cs_namespace_operator to authorize ibm-namespace-scope-operator permissions to namespace %s", nsMem)
			r.Recorder.Eventf(instance, corev1.EventTypeWarning, "Forbidden", "ibm-namespace-scope-operator doesn't have admin permission in namespace %s. NOTE: Refer to https://ibm.biz/cs_namespace_operator to authorize ibm-namespace-scope-operator permissions to namespace %s", nsMem, nsMem)
		}
	}
	return validatedNs, nil
}

func (r *NamespaceScopeReconciler) CSVReconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx = context.Background()

	// Fetch the NamespaceScope instance
	instance := &operatorv1.NamespaceScope{}

	if err := r.Client.Get(ctx, req.NamespacedName, instance); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	originalInstance := instance.DeepCopy()

	if !instance.Spec.CSVInjector.Enable {
		return ctrl.Result{}, nil
	}

	instance = r.setDefaults(instance)

	klog.Infof("Reconciling NamespaceScope: %s for patching operator CSV", req.NamespacedName)

	csvList := &olmv1alpha1.ClusterServiceVersionList{}
	configmapName := instance.Spec.ConfigmapName
	if err := r.Client.List(ctx, csvList); err != nil {
		klog.Error(err)
		return ctrl.Result{}, err
	}

	var candidateOperatorPackage []string
	for _, csv := range csvList.Items {
		packages, ok := csv.Annotations[constant.InjectorMark]
		if !ok {
			continue
		}
		candidateOperatorPackage = append(candidateOperatorPackage, strings.Split(packages, ",")...)
	}

	candidateSet := util.MakeSet(candidateOperatorPackage)
	var managedCSVList, patchedCSVList []string
	for _, packageName := range candidateSet.ToSlice() {
		managedCSVList = append(managedCSVList, packageName.(string))
		csvList := &olmv1alpha1.ClusterServiceVersionList{}
		if err := r.Client.List(ctx, csvList, &client.ListOptions{
			LabelSelector: labels.SelectorFromSet(map[string]string{
				"operators.coreos.com/" + packageName.(string) + "." + instance.Namespace: "",
			})}); err != nil {
			klog.Error(err)
			return ctrl.Result{}, err
		}
		if len(csvList.Items) == 0 {
			continue
		}
		patchedCSVList = append(patchedCSVList, packageName.(string))
		csv := csvList.Items[0]
		csvOriginal := csv.DeepCopy()
		if csv.Spec.InstallStrategy.StrategyName != "deployment" {
			continue
		}
		deploymentSpecs := csv.Spec.InstallStrategy.StrategySpec.DeploymentSpecs
		for _, deploy := range deploymentSpecs {
			podTemplate := deploy.Spec.Template
			// Insert restartlabel into operator pods
			if podTemplate.Labels == nil {
				podTemplate.Labels = make(map[string]string)
			}
			for k, v := range instance.Spec.RestartLabels {
				podTemplate.Labels[k] = v
			}
			// Insert WATCH_NAMESPACE into operator pod environment variables
			for containerIndex, container := range podTemplate.Spec.Containers {
				var found bool
				optional := true
				configmapEnv := corev1.EnvVar{
					Name: "WATCH_NAMESPACE",
					ValueFrom: &corev1.EnvVarSource{
						ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
							Optional: &optional,
							Key:      "namespaces",
							LocalObjectReference: corev1.LocalObjectReference{
								Name: configmapName,
							},
						},
					},
				}
				for index, env := range container.Env {
					if env.Name == "WATCH_NAMESPACE" {
						found = true
						if env.ValueFrom.ConfigMapKeyRef != nil && env.ValueFrom.ConfigMapKeyRef.Key == "namespaces" && env.ValueFrom.ConfigMapKeyRef.LocalObjectReference.Name == configmapName {
							continue
						}
						container.Env[index] = configmapEnv
					}
				}
				if !found {
					container.Env = append(container.Env, configmapEnv)
				}
				podTemplate.Spec.Containers[containerIndex] = container
			}
		}

		if equality.Semantic.DeepDerivative(csvOriginal.Spec.InstallStrategy.StrategySpec.DeploymentSpecs, csv.Spec.InstallStrategy.StrategySpec.DeploymentSpecs) {
			klog.V(3).Infof("No updates in the CSV, skip patching the CSV %s ", csv.Name)
			continue
		}

		if err := r.Client.Patch(ctx, &csv, client.MergeFrom(csvOriginal)); err != nil {
			klog.Error(err)
			return ctrl.Result{}, err
		}
		klog.V(1).Infof("WATCH_NAMESPACE and restart labels are inserted into CSV %s ", csv.Name)

	}

	if util.CheckListDifference(instance.Status.ManagedCSVList, managedCSVList) {
		instance.Status.ManagedCSVList = managedCSVList
	}

	if util.CheckListDifference(instance.Status.PatchedCSVList, patchedCSVList) {
		instance.Status.PatchedCSVList = patchedCSVList
	}

	if reflect.DeepEqual(originalInstance.Status, instance.Status) {
		return ctrl.Result{RequeueAfter: 180 * time.Second}, nil
	}
	if err := r.Client.Status().Patch(ctx, instance, client.MergeFrom(originalInstance)); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 180 * time.Second}, nil
}

func (r *NamespaceScopeReconciler) csvtoRequest() handler.ToRequestsFunc {
	return func(object handler.MapObject) []ctrl.Request {
		nssList := &operatorv1.NamespaceScopeList{}
		err := r.Client.List(context.TODO(), nssList, &client.ListOptions{Namespace: object.Meta.GetNamespace()})
		if err != nil {
			klog.Error(err)
		}
		requests := []ctrl.Request{}

		for _, request := range nssList.Items {
			namespaceName := types.NamespacedName{Name: request.Name, Namespace: request.Namespace}
			req := ctrl.Request{NamespacedName: namespaceName}
			requests = append(requests, req)
		}
		return requests
	}
}

func (r *NamespaceScopeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	err := ctrl.NewControllerManagedBy(mgr).
		Owns(&corev1.ConfigMap{}).
		For(&operatorv1.NamespaceScope{}).
		Complete(reconcile.Func(r.Reconcile))
	if err != nil {
		return err
	}
	err = ctrl.NewControllerManagedBy(mgr).
		For(&operatorv1.NamespaceScope{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&source.Kind{Type: &olmv1alpha1.ClusterServiceVersion{}}, &handler.EnqueueRequestsFromMapFunc{
			ToRequests: r.csvtoRequest(),
		}, builder.WithPredicates(predicate.Funcs{
			DeleteFunc: func(e event.DeleteEvent) bool {
				// Evaluates to false if the object has been confirmed deleted.
				return !e.DeleteStateUnknown
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				oldObject := e.ObjectOld.(*olmv1alpha1.ClusterServiceVersion)
				newObject := e.ObjectNew.(*olmv1alpha1.ClusterServiceVersion)
				return !equality.Semantic.DeepDerivative(oldObject.Spec.InstallStrategy.StrategySpec.DeploymentSpecs, newObject.Spec.InstallStrategy.StrategySpec.DeploymentSpecs)
			},
			CreateFunc: func(e event.CreateEvent) bool {
				return true
			},
		})).
		Complete(reconcile.Func(r.CSVReconcile))
	if err != nil {
		return err
	}
	return nil
}
