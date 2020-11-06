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

package common

import (
	"sort"

	gset "github.com/deckarep/golang-set"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
