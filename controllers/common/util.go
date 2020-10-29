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
	"fmt"

	gset "github.com/deckarep/golang-set"
)

func makeSet(strs []string) gset.Set {
	set := gset.NewSet()
	for _, str := range strs {
		set.Add(str)
	}
	return set
}
func GetListDifference(slice1 []string, slice2 []string) []string {
	set1 := makeSet(slice1)
	set2 := makeSet(slice2)
	var diff []string
	for s := range set1.Difference(set2).Iter() {
		diff = append(diff, fmt.Sprintf("%v", s))
	}
	return diff
}

func Contains(list []string, s string) bool {
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
