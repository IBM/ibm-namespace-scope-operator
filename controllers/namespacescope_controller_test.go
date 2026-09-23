//
// Copyright 2026 IBM Corporation
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
	stderrors "errors"
	"reflect"
	"testing"

	operatorv1 "github.com/IBM/ibm-namespace-scope-operator/v4/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type staticDaemonSetPermissionChecker struct {
	result daemonSetAccessResult
	err    error
}

func (c staticDaemonSetPermissionChecker) Check(context.Context, string) (daemonSetAccessResult, error) {
	return c.result, c.err
}

type recordingSSARClient struct {
	client.Client
	deniedVerb string
	verbs      []string
}

func (c *recordingSSARClient) Create(ctx context.Context, object client.Object, opts ...client.CreateOption) error {
	review, ok := object.(*authorizationv1.SelfSubjectAccessReview)
	if !ok {
		return c.Client.Create(ctx, object, opts...)
	}

	verb := review.Spec.ResourceAttributes.Verb
	c.verbs = append(c.verbs, verb)
	review.Status.Allowed = verb != c.deniedVerb
	return nil
}

type daemonSetGetForbiddenClient struct {
	client.Client
}

func (c daemonSetGetForbiddenClient) Get(ctx context.Context, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
	if _, ok := object.(*appsv1.DaemonSet); ok {
		return apierrors.NewForbidden(
			schema.GroupResource{Group: "apps", Resource: "daemonsets"},
			key.Name,
			stderrors.New("forbidden"),
		)
	}
	return c.Client.Get(ctx, key, object, opts...)
}

type daemonSetPatchForbiddenClient struct {
	client.Client
}

func (c daemonSetPatchForbiddenClient) Patch(ctx context.Context, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if daemonSet, ok := object.(*appsv1.DaemonSet); ok {
		return apierrors.NewForbidden(
			schema.GroupResource{Group: "apps", Resource: "daemonsets"},
			daemonSet.Name,
			stderrors.New("forbidden"),
		)
	}
	return c.Client.Patch(ctx, object, patch, opts...)
}

func TestSelfSubjectDaemonSetPermissionCheckerChecksRequiredVerbs(t *testing.T) {
	ctx := context.Background()
	baseClient := newNamespaceScopeTestClient(t)
	recordingClient := &recordingSSARClient{Client: baseClient}
	checker := selfSubjectDaemonSetPermissionChecker{client: recordingClient}

	result, err := checker.Check(ctx, "test-ns")
	if err != nil {
		t.Fatalf("check returned an error: %v", err)
	}
	if !result.allowed {
		t.Fatalf("expected all permissions to be allowed, got %+v", result)
	}
	if want := []string{"get", "patch"}; !reflect.DeepEqual(recordingClient.verbs, want) {
		t.Fatalf("reviewed verbs = %v, want %v", recordingClient.verbs, want)
	}

	recordingClient = &recordingSSARClient{Client: baseClient, deniedVerb: "patch"}
	checker = selfSubjectDaemonSetPermissionChecker{client: recordingClient}
	result, err = checker.Check(ctx, "test-ns")
	if err != nil {
		t.Fatalf("check returned an error: %v", err)
	}
	if result.allowed || result.deniedVerb != "patch" {
		t.Fatalf("expected patch to be denied, got %+v", result)
	}
}

func TestRestartPodsUpdatesDaemonSetWhenPermissionIsGranted(t *testing.T) {
	ctx := context.Background()
	pod, daemonSet, configMap := daemonSetRestartObjects()
	r, baseClient := newNamespaceScopeTestReconciler(t, pod, daemonSet, configMap)
	r.daemonSetPermissionChecker = staticDaemonSetPermissionChecker{
		result: daemonSetAccessResult{allowed: true},
	}

	if err := r.RestartPods(ctx, pod.Labels, configMap, pod.Namespace); err != nil {
		t.Fatalf("RestartPods returned an error: %v", err)
	}

	updated := &appsv1.DaemonSet{}
	if err := baseClient.Get(ctx, types.NamespacedName{Name: daemonSet.Name, Namespace: daemonSet.Namespace}, updated); err != nil {
		t.Fatalf("get updated DaemonSet: %v", err)
	}
	if updated.Spec.Template.Annotations["nss.ibm.com/namespaceList"] == "" {
		t.Fatal("expected namespaceList annotation to be set on the DaemonSet pod template")
	}
}

func TestRestartPodsSkipsDaemonSetWhenPermissionIsDenied(t *testing.T) {
	ctx := context.Background()
	pod, daemonSet, configMap := daemonSetRestartObjects()
	r, baseClient := newNamespaceScopeTestReconciler(t, pod, daemonSet, configMap)
	r.daemonSetPermissionChecker = staticDaemonSetPermissionChecker{
		result: daemonSetAccessResult{deniedVerb: "get"},
	}

	if err := r.RestartPods(ctx, pod.Labels, configMap, pod.Namespace); err != nil {
		t.Fatalf("RestartPods returned an error: %v", err)
	}
	assertDaemonSetNotRestarted(t, ctx, baseClient, daemonSet)
}

func TestRestartPodsSkipsDaemonSetWhenAccessReviewIsForbidden(t *testing.T) {
	ctx := context.Background()
	pod, daemonSet, configMap := daemonSetRestartObjects()
	r, baseClient := newNamespaceScopeTestReconciler(t, pod, daemonSet, configMap)
	r.daemonSetPermissionChecker = staticDaemonSetPermissionChecker{
		err: apierrors.NewForbidden(
			schema.GroupResource{Group: "authorization.k8s.io", Resource: "selfsubjectaccessreviews"},
			"",
			stderrors.New("forbidden"),
		),
	}

	if err := r.RestartPods(ctx, pod.Labels, configMap, pod.Namespace); err != nil {
		t.Fatalf("RestartPods returned an error: %v", err)
	}
	assertDaemonSetNotRestarted(t, ctx, baseClient, daemonSet)
}

func TestRestartPodsReturnsUnexpectedAccessReviewError(t *testing.T) {
	ctx := context.Background()
	pod, daemonSet, configMap := daemonSetRestartObjects()
	r, _ := newNamespaceScopeTestReconciler(t, pod, daemonSet, configMap)
	r.daemonSetPermissionChecker = staticDaemonSetPermissionChecker{
		err: stderrors.New("access review failed"),
	}

	if err := r.RestartPods(ctx, pod.Labels, configMap, pod.Namespace); err == nil {
		t.Fatal("expected RestartPods to return an unexpected access review error")
	}
}

func TestRestartPodsSkipsDaemonSetWhenGetBecomesForbidden(t *testing.T) {
	ctx := context.Background()
	pod, daemonSet, configMap := daemonSetRestartObjects()
	r, baseClient := newNamespaceScopeTestReconciler(t, pod, daemonSet, configMap)
	r.Client = daemonSetGetForbiddenClient{Client: baseClient}
	r.daemonSetPermissionChecker = staticDaemonSetPermissionChecker{
		result: daemonSetAccessResult{allowed: true},
	}

	if err := r.RestartPods(ctx, pod.Labels, configMap, pod.Namespace); err != nil {
		t.Fatalf("RestartPods returned an error: %v", err)
	}
	assertDaemonSetNotRestarted(t, ctx, baseClient, daemonSet)
}

func TestRestartPodsSkipsDaemonSetWhenPatchBecomesForbidden(t *testing.T) {
	ctx := context.Background()
	pod, daemonSet, configMap := daemonSetRestartObjects()
	r, baseClient := newNamespaceScopeTestReconciler(t, pod, daemonSet, configMap)
	r.Client = daemonSetPatchForbiddenClient{Client: baseClient}
	r.daemonSetPermissionChecker = staticDaemonSetPermissionChecker{
		result: daemonSetAccessResult{allowed: true},
	}

	if err := r.RestartPods(ctx, pod.Labels, configMap, pod.Namespace); err != nil {
		t.Fatalf("RestartPods returned an error: %v", err)
	}
	assertDaemonSetNotRestarted(t, ctx, baseClient, daemonSet)
}

func newNamespaceScopeTestReconciler(t *testing.T, objects ...client.Object) (*NamespaceScopeReconciler, client.Client) {
	t.Helper()
	testClient := newNamespaceScopeTestClient(t, objects...)
	return &NamespaceScopeReconciler{Reader: testClient, Client: testClient}, testClient
}

func newNamespaceScopeTestClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add apps scheme: %v", err)
	}
	if err := authorizationv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add authorization scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := operatorv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add operator scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&operatorv1.NamespaceScope{}).WithObjects(objects...).Build()
}

func daemonSetRestartObjects() (*corev1.Pod, *appsv1.DaemonSet, *corev1.ConfigMap) {
	controller := true
	labels := map[string]string{"restart": "true"}
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "namespace-scope", Namespace: "test-ns"},
		Data:       map[string]string{"namespaces": "test-ns,member-ns"},
	}
	daemonSet := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test-daemonset", Namespace: "test-ns"},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "test"}}},
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-daemonset-pod",
			Namespace: "test-ns",
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "DaemonSet",
				Name:       daemonSet.Name,
				Controller: &controller,
			}},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "test", Image: "test"}},
			Volumes: []corev1.Volume{{
				Name: "namespace-scope",
				VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: configMap.Name},
				}},
			}},
		},
	}
	return pod, daemonSet, configMap
}

func assertDaemonSetNotRestarted(t *testing.T, ctx context.Context, c client.Client, daemonSet *appsv1.DaemonSet) {
	t.Helper()
	updated := &appsv1.DaemonSet{}
	if err := c.Get(ctx, types.NamespacedName{Name: daemonSet.Name, Namespace: daemonSet.Namespace}, updated); err != nil {
		t.Fatalf("get DaemonSet: %v", err)
	}
	if updated.Spec.Template.Annotations["nss.ibm.com/namespaceList"] != "" {
		t.Fatalf("expected DaemonSet to remain unchanged, got annotations %#v", updated.Spec.Template.Annotations)
	}
}

func TestRecordOperationTimingAndStatusSuccess(t *testing.T) {
	ctx := context.Background()
	nss := &operatorv1.NamespaceScope{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-nss",
			Namespace: "test-ns",
		},
	}
	r, c := newNamespaceScopeTestReconciler(t, nss)

	startTime := metav1.Now()
	r.recordOperationTimingAndStatus(ctx, nss, startTime, "Completed", "Reconcile operation completed successfully", nil)

	updated := &operatorv1.NamespaceScope{}
	if err := c.Get(ctx, types.NamespacedName{Name: nss.Name, Namespace: nss.Namespace}, updated); err != nil {
		t.Fatalf("failed to get updated NamespaceScope: %v", err)
	}

	if updated.Status.Status != "Completed" {
		t.Fatalf("expected status to be 'Completed', got %q", updated.Status.Status)
	}

	if len(updated.Status.ReconcileHistory) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(updated.Status.ReconcileHistory))
	}

	if len(updated.Status.OperationTiming) != 1 {
		t.Fatalf("expected 1 operationTiming entry, got %d", len(updated.Status.OperationTiming))
	}

	timing := updated.Status.OperationTiming[0]
	if timing.Phase != "Completed" {
		t.Errorf("expected timing phase to be 'Completed', got %q", timing.Phase)
	}
	if timing.TotalDuration == "" {
		t.Error("expected non-empty totalDuration")
	}
}

func TestRecordOperationTimingAndStatusFailureAndRollingLimit(t *testing.T) {
	ctx := context.Background()
	nss := &operatorv1.NamespaceScope{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-nss",
			Namespace: "test-ns",
		},
	}
	r, c := newNamespaceScopeTestReconciler(t, nss)

	// Simulate 6 operations to test rolling limit of 5 for timing and 3 for history
	for i := 1; i <= 6; i++ {
		startTime := metav1.Now()
		errMsg := stderrors.New("reconcile error")
		r.recordOperationTimingAndStatus(ctx, nss, startTime, "Failed", "Operation failed", errMsg)
	}

	updated := &operatorv1.NamespaceScope{}
	if err := c.Get(ctx, types.NamespacedName{Name: nss.Name, Namespace: nss.Namespace}, updated); err != nil {
		t.Fatalf("failed to get updated NamespaceScope: %v", err)
	}

	if updated.Status.Status != "Failed" {
		t.Fatalf("expected status to be 'Failed', got %q", updated.Status.Status)
	}

	if len(updated.Status.ReconcileHistory) != 3 {
		t.Fatalf("expected max 3 reconcileHistory entries, got %d", len(updated.Status.ReconcileHistory))
	}

	if len(updated.Status.OperationTiming) != 5 {
		t.Fatalf("expected max 5 operationTiming entries, got %d", len(updated.Status.OperationTiming))
	}
}

