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
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	olmv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ownerutil "github.com/operator-framework/operator-lifecycle-manager/pkg/lib/ownerutil"
	admissionv1 "k8s.io/api/admissionregistration/v1"
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
	"k8s.io/client-go/rest"
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

	operatorv1 "github.com/IBM/ibm-namespace-scope-operator/v4/api/v1"
	util "github.com/IBM/ibm-namespace-scope-operator/v4/controllers/common"
	"github.com/IBM/ibm-namespace-scope-operator/v4/controllers/constant"
)

//var ctx context.Context

// NamespaceScopeReconciler reconciles a NamespaceScope object
type NamespaceScopeReconciler struct {
	client.Reader
	client.Client
	Recorder record.EventRecorder
	Scheme   *runtime.Scheme
	Config   *rest.Config
}

func (r *NamespaceScopeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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

			if err := r.UpdateConfigMap(ctx, instance); err != nil {
				return ctrl.Result{}, err
			}

			if err := r.DeleteAllRbac(ctx, instance); err != nil {
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

	nssObjectList := &operatorv1.NamespaceScopeList{}
	if err := r.Client.List(ctx, nssObjectList); err != nil {
		return ctrl.Result{}, err
	}

	licenseAccepted := false

	for _, nss := range nssObjectList.Items {
		if nss.GetDeletionTimestamp() != nil {
			continue
		}
		if nss.Spec.License.Accept {
			licenseAccepted = true
			break
		}
	}

	if !licenseAccepted {
		klog.Infof("Accept license by changing .spec.license.accept to true in the NamespaceScope: %s", req.NamespacedName)
	}

	// Add finalizer for this instance
	if !util.Contains(instance.GetFinalizers(), constant.NamespaceScopeFinalizer) {
		if err := r.addFinalizer(ctx, instance); err != nil {
			return ctrl.Result{}, err
		}
	}

	instance = r.setDefaults(instance)

	klog.Infof("Reconciling NamespaceScope: %s", req.NamespacedName)

	if err := r.UpdateStatus(ctx, instance); err != nil {
		klog.Errorf("Failed to update the status of NamespaceScope %s: %v", req.NamespacedName, err)
		return ctrl.Result{}, err
	}

	if err := r.PushRbacToNamespace(ctx, instance); err != nil {
		klog.Errorf("Failed to generate rbac: %v", err)
		return ctrl.Result{}, err
	}

	if err := r.DeleteRbacFromUnmanagedNamespace(ctx, instance); err != nil {
		klog.Errorf("Failed to delete rbac: %v", err)
		return ctrl.Result{}, err
	}

	if err := r.UpdateConfigMap(ctx, instance); err != nil {
		klog.Errorf("Failed to update configmap: %v", err)
		return ctrl.Result{}, err
	}

	reg, err := regexp.Compile(`^nss-(managed|runtime)-role-from.*`)
	if err != nil {
		klog.Errorf("Failed to compile regular expression: %v", err)
		return ctrl.Result{}, err
	}
	for _, namespaceMember := range instance.Spec.NamespaceMembers {
		if rolesList, _ := r.GetRolesFromNamespace(ctx, instance, namespaceMember); len(rolesList) != 0 {
			var summarizedRules []rbacv1.PolicyRule
			for _, role := range rolesList {
				if !reg.MatchString(role.Name) {
					summarizedRules = append(summarizedRules, role.Rules...)
				}
			}
			if err := r.CreateRuntimeRoleToNamespace(ctx, instance, namespaceMember, summarizedRules); err != nil {
				klog.Infof("Failed to create runtime role: %v", err)
				return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
			}
		}
	}

	klog.Infof("Finished reconciling NamespaceScope: %s", req.NamespacedName)
	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

func (r *NamespaceScopeReconciler) addFinalizer(ctx context.Context, nss *operatorv1.NamespaceScope) error {
	controllerutil.AddFinalizer(nss, constant.NamespaceScopeFinalizer)
	if err := r.Update(ctx, nss); err != nil {
		klog.Errorf("Failed to update NamespaceScope with finalizer: %v", err)
		return err
	}
	klog.Infof("Added finalizer for the NamespaceScope instance %s/%s", nss.Namespace, nss.Name)
	return nil
}

func (r *NamespaceScopeReconciler) UpdateStatus(ctx context.Context, instance *operatorv1.NamespaceScope) error {
	// Get validated namespaces
	validatedNamespaces, err := r.getValidatedNamespaces(ctx, instance)
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

func (r *NamespaceScopeReconciler) UpdateConfigMap(ctx context.Context, instance *operatorv1.NamespaceScope) error {
	cm := &corev1.ConfigMap{}
	cmName := instance.Spec.ConfigmapName
	cmNamespace := instance.Namespace
	cmKey := types.NamespacedName{Name: cmName, Namespace: cmNamespace}
	validatedMembers, err := r.getAllValidatedNamespaceMembers(ctx, instance)
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

			return r.RestartPods(ctx, instance.Spec.RestartLabels, cm, instance.Namespace)
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
			if err := r.RestartPods(ctx, instance.Spec.RestartLabels, cm, instance.Namespace); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *NamespaceScopeReconciler) PushRbacToNamespace(ctx context.Context, instance *operatorv1.NamespaceScope) error {
	fromNs := instance.Namespace
	saNames, err := r.GetServiceAccountFromNamespace(ctx, instance, fromNs)
	if err != nil {
		return err
	}

	operatorNs, err := util.GetOperatorNamespace()
	if err != nil {
		klog.Error("get operator namespace failed: ", err)
		return err
	}

	var wg sync.WaitGroup
	errorChannel := make(chan error, len(instance.Status.ValidatedMembers))

	for _, toNs := range instance.Status.ValidatedMembers {
		if toNs == operatorNs {
			continue
		}

		wg.Add(1)
		go func(toNs string) {
			defer wg.Done()
			if err := r.generateRBACToNamespace(ctx, instance, saNames, fromNs, toNs); err != nil {
				errorChannel <- err
			}
		}(toNs)
	}

	// Wait for all RBAC generation to finish
	wg.Wait()
	close(errorChannel)

	// Return the first error encountered, if any
	if len(errorChannel) > 0 {
		return <-errorChannel
	}

	return nil
}

func (r *NamespaceScopeReconciler) CreateRuntimeRoleToNamespace(ctx context.Context, instance *operatorv1.NamespaceScope, toNs string, summarizedRules []rbacv1.PolicyRule) error {
	fromNs := instance.Namespace

	operatorNs, err := util.GetOperatorNamespace()
	if err != nil {
		klog.Error("get operator namespace failed: ", err)
		return err
	}
	if toNs == operatorNs {
		return nil
	}
	return r.generateRuntimeRoleForNSS(ctx, instance, summarizedRules, fromNs, toNs)
}

func (r *NamespaceScopeReconciler) DeleteRbacFromUnmanagedNamespace(ctx context.Context, instance *operatorv1.NamespaceScope) error {
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
	nsInCr, err := r.getAllValidatedNamespaceMembers(ctx, instance)
	if err != nil {
		return err
	}
	unmanagedNss := util.GetListDifference(nsInCm, nsInCr)
	configmapValue := util.GetFirstNCharacter(instance.Spec.ConfigmapName+"-"+instance.Namespace, 63)
	labels := map[string]string{
		"namespace-scope-configmap": configmapValue,
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

		if err := r.DeleteRoleBinding(ctx, labels, toNs); err != nil {
			if errors.IsForbidden(err) {
				r.Recorder.Eventf(instance, corev1.EventTypeWarning, "Forbidden", "cannot delete resource rolebindings in API group rbac.authorization.k8s.io in the namespace %s. Please authorize service account ibm-namespace-scope-operator namespace admin permission of %s namespace", toNs, toNs)
				continue
			}
			return err
		}
		if err := r.DeleteRole(ctx, labels, toNs); err != nil {
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
func (r *NamespaceScopeReconciler) DeleteAllRbac(ctx context.Context, instance *operatorv1.NamespaceScope) error {
	configmapValue := util.GetFirstNCharacter(instance.Spec.ConfigmapName+"-"+instance.Namespace, 63)
	labels := map[string]string{
		"namespace-scope-configmap": configmapValue,
	}

	operatorNs, err := util.GetOperatorNamespace()
	if err != nil {
		klog.Error("get operator namespace failed: ", err)
		return err
	}

	usingMembers, err := r.getAllValidatedNamespaceMembers(ctx, instance)
	if err != nil {
		return err
	}
	deletedMembers := util.GetListDifference(instance.Spec.NamespaceMembers, usingMembers)

	for _, toNs := range deletedMembers {
		if toNs == operatorNs {
			continue
		}
		if err := r.DeleteRoleBinding(ctx, labels, toNs); err != nil {
			if errors.IsForbidden(err) {
				r.Recorder.Eventf(instance, corev1.EventTypeWarning, "Forbidden", "cannot delete resource rolebindings in API group rbac.authorization.k8s.io in the namespace %s. Please authorize service account ibm-namespace-scope-operator namespace admin permission of %s namespace", toNs, toNs)
				continue
			}
			return err
		}
		if err := r.DeleteRole(ctx, labels, toNs); err != nil {
			if errors.IsForbidden(err) {
				r.Recorder.Eventf(instance, corev1.EventTypeWarning, "Forbidden", "cannot delete resource roles in API group rbac.authorization.k8s.io in the namespace %s. Please authorize service account ibm-namespace-scope-operator namespace admin permission of %s namespace", toNs, toNs)
				continue
			}
			return err
		}
	}
	return nil
}

func (r *NamespaceScopeReconciler) generateRuntimeRoleForNSS(ctx context.Context, instance *operatorv1.NamespaceScope, summarizedRules []rbacv1.PolicyRule, fromNs, toNs string) error {
	if err := r.createRuntimeRoleForNSS(ctx, summarizedRules, fromNs, toNs); err != nil {
		if errors.IsAlreadyExists(err) {
			return r.updateRuntimeRoleForNSS(ctx, summarizedRules, fromNs, toNs)
		}
		if errors.IsForbidden(err) {
			r.Recorder.Eventf(instance, corev1.EventTypeWarning, "Forbidden", "cannot create resource roles in API group rbac.authorization.k8s.io in the namespace %s. Please authorize service account ibm-namespace-scope-operator namespace admin permission of %s namespace", toNs, toNs)
		}
		return err
	}

	return nil
}

func (r *NamespaceScopeReconciler) createRuntimeRoleForNSS(ctx context.Context, summarizedRules []rbacv1.PolicyRule, fromNs, toNs string) error {
	name := constant.NamespaceScopeRuntimePrefix + fromNs
	namespace := toNs
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
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

func (r *NamespaceScopeReconciler) updateRuntimeRoleForNSS(ctx context.Context, summarizedRules []rbacv1.PolicyRule, fromNs, toNs string) error {
	name := constant.NamespaceScopeRuntimePrefix + fromNs
	namespace := toNs

	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
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

func (r *NamespaceScopeReconciler) generateRBACToNamespace(ctx context.Context, instance *operatorv1.NamespaceScope, saNames []string, fromNs, toNs string) error {
	configmapValue := util.GetFirstNCharacter(instance.Spec.ConfigmapName+"-"+instance.Namespace, 63)
	labels := map[string]string{
		"namespace-scope-configmap":    configmapValue,
		"app.kubernetes.io/instance":   "namespace-scope",
		"app.kubernetes.io/managed-by": "ibm-namespace-scope-operator",
		"app.kubernetes.io/name":       instance.Spec.ConfigmapName,
	}

	var wg sync.WaitGroup
	errorChannel := make(chan error, len(saNames))

	for _, sa := range saNames {
		wg.Add(1)

		go func(sa string) {
			defer wg.Done()

			roleList, err := r.GetRolesFromServiceAccount(ctx, sa, fromNs)
			if err != nil {
				errorChannel <- err
				return
			}

			klog.V(2).Infof("Roles waiting to be copied for SA %s: %v", sa, roleList)

			if err := r.CreateRole(ctx, roleList, labels, sa, fromNs, toNs); err != nil {
				if errors.IsForbidden(err) {
					r.Recorder.Eventf(instance, corev1.EventTypeWarning, "Forbidden", "cannot create resource roles in API group rbac.authorization.k8s.io in the namespace %s. Please authorize service account ibm-namespace-scope-operator namespace admin permission of %s namespace", toNs, toNs)
				}
				errorChannel <- err
				return
			}

			if err := r.CreateRoleBinding(ctx, roleList, labels, sa, fromNs, toNs); err != nil {
				if errors.IsForbidden(err) {
					r.Recorder.Eventf(instance, corev1.EventTypeWarning, "Forbidden", "cannot create resource rolebindings in API group rbac.authorization.k8s.io in the namespace %s. Please authorize service account ibm-namespace-scope-operator namespace admin permission of %s namespace", toNs, toNs)
				}
				errorChannel <- err
				return
			}
		}(sa)
	}

	wg.Wait()
	close(errorChannel)

	if len(errorChannel) > 0 {
		return <-errorChannel
	}

	return nil
}

func (r *NamespaceScopeReconciler) GetRolesFromNamespace(ctx context.Context, instance *operatorv1.NamespaceScope, namespace string) ([]rbacv1.Role, error) {
	rolesList := &rbacv1.RoleList{}

	opts := []client.ListOption{
		client.InNamespace(namespace),
	}
	if err := r.Reader.List(ctx, rolesList, opts...); err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("Roles not found in namespace %s: %v", namespace, err)
			return nil, nil
		}
		klog.Errorf("Cannot list roles in namespace %s: %v", namespace, err)
		return nil, err
	}

	roles := []rbacv1.Role{}
	for _, role := range rolesList.Items {
		configmapValue := util.GetFirstNCharacter(instance.Spec.ConfigmapName+"-"+instance.Namespace, 63)
		if value, ok := role.Labels[constant.NamespaceScopeConfigmapLabelKey]; ok && value == configmapValue {
			roles = append(roles, role)
		}
	}

	return roles, nil
}

func (r *NamespaceScopeReconciler) GetServiceAccountFromNamespace(ctx context.Context, instance *operatorv1.NamespaceScope, namespace string) ([]string, error) {
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

func (r *NamespaceScopeReconciler) GetRolesFromServiceAccount(ctx context.Context, sa string, namespace string) ([]string, error) {
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

func (r *NamespaceScopeReconciler) CreateRole(ctx context.Context, roleNames []string, labels map[string]string, saName, fromNs, toNs string) error {
	// Get the permissions that NamespaceScope Operator has from the toNs
	nssManagedRole := &rbacv1.Role{}
	if err := r.Reader.Get(ctx, types.NamespacedName{Name: constant.NamespaceScopeManagedPrefix + fromNs, Namespace: toNs}, nssManagedRole); err != nil {
		if errors.IsNotFound(err) {
			klog.Errorf("Role %s not found in namespace %s: %v", constant.NamespaceScopeManagedPrefix+fromNs, toNs, err)
			return err
		}
		klog.Errorf("Failed to get role %s in namespace %s: %v", constant.NamespaceScopeManagedPrefix+fromNs, toNs, err)
		return err
	}

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
		rules := rulesFilter(roleName, fromNs, toNs, originalRole.Rules, nssManagedRole.Rules)
		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels:    labels,
			},
			Rules: rules,
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

func (r *NamespaceScopeReconciler) DeleteRole(ctx context.Context, labels map[string]string, toNs string) error {
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

func (r *NamespaceScopeReconciler) CreateRoleBinding(ctx context.Context, roleNames []string, labels map[string]string, saName, fromNs, toNs string) error {
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

func (r *NamespaceScopeReconciler) DeleteRoleBinding(ctx context.Context, labels map[string]string, toNs string) error {
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
func (r *NamespaceScopeReconciler) RestartPods(ctx context.Context, labels map[string]string, cm *corev1.ConfigMap, namespace string) error {
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
		pod := pod
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

func (r *NamespaceScopeReconciler) getAllValidatedNamespaceMembers(ctx context.Context, instance *operatorv1.NamespaceScope) ([]string, error) {
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

func (r *NamespaceScopeReconciler) checkGetNSAuth(ctx context.Context) bool {
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

func rulesFilter(roleName, fromNs, toNs string, orgRule, nssManagedRule []rbacv1.PolicyRule) []rbacv1.PolicyRule {
	verbMap := make(map[string]struct{})
	verbs := []string{"create", "delete", "get", "list", "patch", "update", "watch", "deletecollection"}
	for _, v := range verbs {
		verbMap[v] = struct{}{}
	}
	needRuleAppending := false
	for i := 0; i < len(orgRule); {
		// filter out the wildcard in APIGroups
		for j := 0; j < len(orgRule[i].APIGroups); {
			if orgRule[i].APIGroups[j] == "*" {
				orgRule[i].APIGroups = append(orgRule[i].APIGroups[:j], orgRule[i].APIGroups[j+1:]...)
				klog.Warningf("Role %s in namespace %s has wildcard * in APIGroups, which is removed from its copied Role in namespace %s", roleName, fromNs, toNs)
				needRuleAppending = true
				continue
			}
			j++
		}
		if len(orgRule[i].APIGroups) == 0 {
			orgRule = append(orgRule[:i], orgRule[i+1:]...)
			continue
		}
		// filter out the wildcard in Resources
		for j := 0; j < len(orgRule[i].Resources); {
			if orgRule[i].Resources[j] == "*" {
				orgRule[i].Resources = append(orgRule[i].Resources[:j], orgRule[i].Resources[j+1:]...)
				klog.Warningf("Role %s in namespace %s has wildcard * in Resources, which is removed from its copied Role in namespace %s", roleName, fromNs, toNs)
				needRuleAppending = true
				continue
			}
			j++
		}
		if len(orgRule[i].Resources) == 0 {
			orgRule = append(orgRule[:i], orgRule[i+1:]...)
			continue
		}

		for j := 0; j < len(orgRule[i].Verbs); {
			if orgRule[i].Verbs[j] == "*" {
				orgRule[i].Verbs = append(orgRule[i].Verbs[:j], orgRule[i].Verbs[j+1:]...)
				orgRule[i].Verbs = append(orgRule[i].Verbs, verbs...)
				klog.Warningf("Role %s in namespace %s has wildcard * in Verbs, which is replaced with all verbs in its copied Role in namespace %s", roleName, fromNs, toNs)
				continue
			}
			if _, ok := verbMap[orgRule[i].Verbs[j]]; !ok {
				orgRule[i].Verbs = append(orgRule[i].Verbs[:j], orgRule[i].Verbs[j+1:]...)
				continue
			}
			j++
		}

		if len(orgRule[i].Verbs) == 0 {
			orgRule = append(orgRule[:i], orgRule[i+1:]...)
			continue
		}
		i++
	}

	if needRuleAppending {
		orgRule = append(orgRule, nssManagedRule...)
		klog.Infof("Role %s has been appended with role %s in namespace %s", roleName, constant.NamespaceScopeManagedPrefix+fromNs, toNs)
	}

	return orgRule
}

func (r *NamespaceScopeReconciler) getValidatedNamespaces(ctx context.Context, instance *operatorv1.NamespaceScope) ([]string, error) {
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
		// Check if operator has permission to get namespace resource
		if r.checkGetNSAuth(ctx) {
			ns := &corev1.Namespace{}
			key := types.NamespacedName{Name: nsMem}
			if err := r.Reader.Get(ctx, key, ns); err != nil {
				if errors.IsNotFound(err) {
					klog.Infof("Namespace %s does not exist and will be ignored", nsMem)
					continue
				}
				return nil, err
			}
			if ns.Status.Phase == corev1.NamespaceTerminating {
				klog.Infof("Namespace %s is terminating. Ignore this namespace", nsMem)
				continue
			}
		}
		validatedNs = append(validatedNs, nsMem)
	}
	return validatedNs, nil
}

func (r *NamespaceScopeReconciler) CSVReconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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
	var managedWebhookList, patchedWebhookList []string
	validatedMembers, err := r.getAllValidatedNamespaceMembers(ctx, instance)
	if err != nil {
		klog.Errorf("Failed to get all validated namespace members: %v", err)
		return ctrl.Result{}, err
	}

	for _, packageName := range candidateSet.ToSlice() {
		managedCSVList = append(managedCSVList, packageName.(string))
		csvList := &olmv1alpha1.ClusterServiceVersionList{}
		labelKey := packageName.(string) + "." + instance.Namespace
		labelKey = util.GetFirstNCharacter(labelKey, 63)
		if err := r.Client.List(ctx, csvList, &client.ListOptions{
			LabelSelector: labels.SelectorFromSet(map[string]string{
				"operators.coreos.com/" + labelKey: "",
			})}); err != nil {
			klog.Error(err)
			return ctrl.Result{}, err
		}
		if len(csvList.Items) == 0 {
			continue
		}
		patchedCSVList = append(patchedCSVList, packageName.(string))

		for _, c := range csvList.Items {
			// avoid Implicit memory aliasing in for loop
			csv := c
			klog.V(2).Infof("Found CSV %s for packageManifest %s", csv.Name, packageName.(string))
			// check if this csv has patch webhook annotation
			_, patchWebhook := csv.Annotations[constant.WebhookMark]
			if patchWebhook {
				klog.Infof("Patching webhookconfiguration for CSV %s", csv.Name)
				if err := r.patchWebhook(ctx, instance, &csv, &managedWebhookList, &patchedWebhookList, validatedMembers); err != nil {
					return ctrl.Result{}, err
				}
			}
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
								klog.V(2).Infof("WATCH_NAMESPACE ENV variable is found in CSV %s, and match the configmap %s, skip it", csv.Name, configmapName)
								continue
							}
							klog.V(2).Infof("WATCH_NAMESPACE ENV variable is found in CSV %s, but not match the configmap %s, replace it", csv.Name, configmapName)
							container.Env[index] = configmapEnv
						}
					}
					if !found {
						klog.V(2).Infof("WATCH_NAMESPACE ENV variable is not found in CSV %s, insert it", csv.Name)
						container.Env = append(container.Env, configmapEnv)
					}
					podTemplate.Spec.Containers[containerIndex] = container
				}
			}
			if equality.Semantic.DeepEqual(csvOriginal.Spec.InstallStrategy.StrategySpec.DeploymentSpecs, csv.Spec.InstallStrategy.StrategySpec.DeploymentSpecs) {
				klog.V(3).Infof("No updates in the CSV, skip patching the CSV %s ", csv.Name)
				continue
			}

			if err := r.Client.Patch(ctx, &csv, client.MergeFrom(csvOriginal)); err != nil {
				klog.Error(err)
				return ctrl.Result{}, err
			}
			klog.V(1).Infof("WATCH_NAMESPACE and restart labels are inserted into CSV %s ", csv.Name)
		}
	}

	if _, err := r.CheckListDifference(ctx, instance, originalInstance, managedCSVList, managedWebhookList, patchedCSVList, patchedWebhookList); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 180 * time.Second}, nil
}

func (r *NamespaceScopeReconciler) CheckListDifference(ctx context.Context, instance *operatorv1.NamespaceScope, originalInstance *operatorv1.NamespaceScope, managedCSVList []string,
	managedWebhookList []string, patchedCSVList []string, patchedWebhookList []string) (ctrl.Result, error) {

	if util.CheckListDifference(instance.Status.ManagedCSVList, managedCSVList) {
		instance.Status.ManagedCSVList = managedCSVList
	}

	if util.CheckListDifference(instance.Status.PatchedCSVList, patchedCSVList) {
		instance.Status.PatchedCSVList = patchedCSVList
	}

	if util.CheckListDifference(instance.Status.ManagedWebhookList, managedWebhookList) {
		instance.Status.ManagedWebhookList = managedWebhookList
	}

	if util.CheckListDifference(instance.Status.PatchedWebhookList, patchedWebhookList) {
		instance.Status.PatchedWebhookList = patchedWebhookList
	}

	if reflect.DeepEqual(originalInstance.Status, instance.Status) {
		return ctrl.Result{RequeueAfter: 180 * time.Second}, nil
	}
	if err := r.Client.Status().Patch(ctx, instance, client.MergeFrom(originalInstance)); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 180 * time.Second}, nil
}

func (r *NamespaceScopeReconciler) patchWebhook(ctx context.Context, instance *operatorv1.NamespaceScope, csv *olmv1alpha1.ClusterServiceVersion,
	managedWebhookList *[]string, patchedWebhookList *[]string, validatedMembers []string) error {
	// get webhooklists
	mWebhookList, vWebhookList, err := r.getWebhooks(ctx, csv.Name, instance.Namespace)
	if err != nil {
		return err
	}
	// add them to the list
	for _, mwbh := range mWebhookList.Items {
		webhook := mwbh
		*managedWebhookList = append(*managedWebhookList, mwbh.GetName())
		if err := r.patchMutatingWebhook(ctx, &webhook, validatedMembers, csv.Name, csv.Namespace); err != nil {
			return err
		}
		*patchedWebhookList = append(*patchedWebhookList, mwbh.GetName())
	}
	for _, vwbh := range vWebhookList.Items {
		webhook := vwbh
		*managedWebhookList = append(*managedWebhookList, vwbh.GetName())
		if err := r.patchValidatingWebhook(ctx, &webhook, validatedMembers, csv.Name, csv.Namespace); err != nil {
			return err
		}
		*patchedWebhookList = append(*patchedWebhookList, vwbh.GetName())
	}
	return nil
}

func (r *NamespaceScopeReconciler) getWebhooks(ctx context.Context, csvName string, csvNs string) (*admissionv1.MutatingWebhookConfigurationList, *admissionv1.ValidatingWebhookConfigurationList, error) {
	vWebhookList := &admissionv1.ValidatingWebhookConfigurationList{}
	mWebhookList := &admissionv1.MutatingWebhookConfigurationList{}

	labelSelector := labels.SelectorFromSet(
		map[string]string{
			ownerutil.OwnerKey:          csvName,
			ownerutil.OwnerKind:         "ClusterServiceVersion",
			ownerutil.OwnerNamespaceKey: csvNs,
		},
	)

	if err := r.Client.List(ctx, vWebhookList, &client.ListOptions{
		LabelSelector: labelSelector,
	}); err != nil {
		return mWebhookList, vWebhookList, err
	}

	if err := r.Client.List(ctx, mWebhookList, &client.ListOptions{
		LabelSelector: labelSelector,
	}); err != nil {
		return mWebhookList, vWebhookList, err
	}

	return mWebhookList, vWebhookList, nil
}

func (r *NamespaceScopeReconciler) patchMutatingWebhook(ctx context.Context, webhookconfig *admissionv1.MutatingWebhookConfiguration, nsList []string, csvName string, csvNS string) error {
	klog.Infof("Patching mutatingwebhookconfig scope for: %s, control by csv %s/%s", webhookconfig.Name, csvName, csvNS)
	for i, webhook := range webhookconfig.Webhooks {
		klog.V(2).Infof("Patching webhook scope for: %s", webhook.Name)
		skipPatch := false
		wbhNSSelector := metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: constant.MatchExpressionsKey,
					Operator: metav1.LabelSelectorOpIn,
					Values:   nsList},
			},
		}

		if webhook.NamespaceSelector == nil {
			return fmt.Errorf("fail to get namespaceSelector")
		}

		if webhook.NamespaceSelector.MatchExpressions != nil {
			if webhook.NamespaceSelector.MatchExpressions[0].Values != nil {
				namespaces := webhook.NamespaceSelector.MatchExpressions[0].Values
				if reflect.DeepEqual(namespaces, nsList) {
					klog.V(2).Infof("Namespace selector has been patched, skip patching webhook")
					skipPatch = true
				}
			}
		}

		if !skipPatch {
			webhookconfig.Webhooks[i].NamespaceSelector = &wbhNSSelector
		}
	}
	klog.Infof("Patching webhook scope for: %s", webhookconfig.Name)
	if err := r.Update(ctx, webhookconfig); err != nil {
		klog.Errorf("failed to update webhook %s: %v", webhookconfig.Name, err)
		return err
	}
	return nil
}

func (r *NamespaceScopeReconciler) patchValidatingWebhook(ctx context.Context, webhookconfig *admissionv1.ValidatingWebhookConfiguration,
	nsList []string, csvName string, csvNS string) error {
	klog.Infof("Patching validatingwebhookconfig scope for: %s, control by csv %s/%s", webhookconfig.Name, csvName, csvNS)
	for i, webhook := range webhookconfig.Webhooks {
		klog.V(2).Infof("Patching webhook scope for: %s", webhook.Name)
		skipPatch := false
		wbhNSSelector := metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: constant.MatchExpressionsKey,
					Operator: metav1.LabelSelectorOpIn,
					Values:   nsList},
			},
		}

		if webhook.NamespaceSelector == nil {
			return fmt.Errorf("fail to get namespaceSelector")
		}
		if webhook.NamespaceSelector.MatchExpressions != nil {
			if webhook.NamespaceSelector.MatchExpressions[0].Values != nil {
				namespaces := webhook.NamespaceSelector.MatchExpressions[0].Values
				if reflect.DeepEqual(namespaces, nsList) {
					klog.V(2).Infof("Namespace selector has been patched, skip patching webhook")
					skipPatch = true
				}
			}
		}

		if !skipPatch {
			webhookconfig.Webhooks[i].NamespaceSelector = &wbhNSSelector
		}
	}

	klog.Infof("Patching webhookconfig scope for: %s", webhookconfig.Name)
	if err := r.Update(ctx, webhookconfig); err != nil {
		klog.Errorf("failed to update webhook %s: %v", webhookconfig.Name, err)
		return err
	}
	return nil
}

func (r *NamespaceScopeReconciler) csvtoRequest() handler.MapFunc {
	return func(object client.Object) []ctrl.Request {
		nssList := &operatorv1.NamespaceScopeList{}
		err := r.Client.List(context.TODO(), nssList, &client.ListOptions{Namespace: object.GetNamespace()})
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

func (r *NamespaceScopeReconciler) validatingwebhookconfigtoRequest() handler.MapFunc {
	return func(object client.Object) []ctrl.Request {
		nssList := &operatorv1.NamespaceScopeList{}
		err := r.Client.List(context.TODO(), nssList, &client.ListOptions{Namespace: object.GetNamespace()})
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

func (r *NamespaceScopeReconciler) mutatingwebhookconfigtoRequest() handler.MapFunc {
	return func(object client.Object) []ctrl.Request {
		nssList := &operatorv1.NamespaceScopeList{}
		err := r.Client.List(context.TODO(), nssList, &client.ListOptions{Namespace: object.GetNamespace()})
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
		Watches(&source.Kind{Type: &olmv1alpha1.ClusterServiceVersion{}}, handler.EnqueueRequestsFromMapFunc(r.csvtoRequest()),
			builder.WithPredicates(predicate.Funcs{
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
		Watches(&source.Kind{Type: &admissionv1.ValidatingWebhookConfiguration{}}, handler.EnqueueRequestsFromMapFunc(r.validatingwebhookconfigtoRequest()),
			builder.WithPredicates(predicate.Funcs{
				DeleteFunc: func(e event.DeleteEvent) bool {
					// Evaluates to false if the object has been confirmed deleted.
					return !e.DeleteStateUnknown
				},
				UpdateFunc: func(e event.UpdateEvent) bool {
					oldObject := e.ObjectOld.(*admissionv1.ValidatingWebhookConfiguration)
					newObject := e.ObjectNew.(*admissionv1.ValidatingWebhookConfiguration)
					return !equality.Semantic.DeepDerivative(oldObject.Webhooks, newObject.Webhooks)
				},
				CreateFunc: func(e event.CreateEvent) bool {
					return true
				},
			})).
		Watches(&source.Kind{Type: &admissionv1.MutatingWebhookConfiguration{}}, handler.EnqueueRequestsFromMapFunc(r.mutatingwebhookconfigtoRequest()),
			builder.WithPredicates(predicate.Funcs{
				DeleteFunc: func(e event.DeleteEvent) bool {
					// Evaluates to false if the object has been confirmed deleted.
					return !e.DeleteStateUnknown
				},
				UpdateFunc: func(e event.UpdateEvent) bool {
					oldObject := e.ObjectOld.(*admissionv1.MutatingWebhookConfiguration)
					newObject := e.ObjectNew.(*admissionv1.MutatingWebhookConfiguration)
					return !equality.Semantic.DeepDerivative(oldObject.Webhooks, newObject.Webhooks)
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
