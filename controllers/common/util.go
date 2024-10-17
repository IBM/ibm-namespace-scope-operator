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

package common

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	gset "github.com/deckarep/golang-set"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog"
)

func MakeSet(strs []string) gset.Set {
	set := gset.NewSet()
	for _, str := range strs {
		set.Add(str)
	}
	return set
}

func ToStringSlice(set gset.Set) []string {
	var strSlice []string
	for s := range set.Iter() {
		strSlice = append(strSlice, s.(string))
	}
	return strSlice
}

func GetListDifference(slice1 []string, slice2 []string) []string {
	set1 := MakeSet(slice1)
	set2 := MakeSet(slice2)
	var diff []string
	for s := range set1.Difference(set2).Iter() {
		diff = append(diff, s.(string))
	}
	return diff
}

func CheckListDifference(slice1 []string, slice2 []string) bool {
	if len(slice1) != len(slice2) {
		return true
	}
	sort.Strings(slice1)
	sort.Strings(slice2)
	for i, v := range slice1 {
		if v != slice2[i] {
			return true
		}
	}
	return false
}

// StringSliceContentEqual checks if the contant from two string slice are the same
func StringSliceContentEqual(slice1, slice2 []string) bool {
	set1 := MakeSet(slice1)
	set2 := MakeSet(slice2)
	return set1.Equal(set2)
}

func GetOwnerReferenceUIDs(ownerRefs []metav1.OwnerReference) []types.UID {
	var ownerRefUIDs []types.UID
	for _, ref := range ownerRefs {
		ownerRefUIDs = append(ownerRefUIDs, ref.UID)
	}
	return ownerRefUIDs
}

func Contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func UIDContains(list []types.UID, s types.UID) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func Reverse(original []string) []string {
	reversed := make([]string, 0, len(original))
	for i := len(original) - 1; i >= 0; i-- {
		reversed = append(reversed, original[i])
	}
	return reversed
}

// GetOperatorNamespace returns the namespace the operator should be running in.
func GetOperatorNamespace() (string, error) {
	ns, found := os.LookupEnv("OPERATOR_NAMESPACE")
	if !found {
		nsBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
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
	klog.V(1).Infof("Found namespace: %s", ns)
	return ns, nil
}

func GetResourcesDynamically(ctx context.Context, dynamic dynamic.Interface, group string, version string, resource string, labelSelector metav1.LabelSelector) (
	[]unstructured.Unstructured, error) {

	resourceID := schema.GroupVersionResource{
		Group:    group,
		Version:  version,
		Resource: resource,
	}

	listOptions := metav1.ListOptions{
		LabelSelector: labels.Set(labelSelector.MatchLabels).String(),
	}
	klog.Infof("Label options: %s", labels.Set(labelSelector.MatchLabels).String())
	// Namespace is empty refer to all namespace
	list, err := dynamic.Resource(resourceID).Namespace("").List(ctx, listOptions)

	if err != nil {
		return nil, err
	}

	return list.Items, nil
}

func GetFirstNCharacter(str string, n int) string {
	if n >= len(str) {
		return str
	}
	return str[:n]
}
