/*
Copyright 2019 VMware, Inc

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"k8s.io/apimachinery/pkg/util/sets"
)

func NewFilter(names []string) func(string) bool {
	include := sets.String{}
	exclude := sets.String{}
	for _, name := range names {
		count := countPrefix(name, '!')
		name = name[count:]
		if count%2 == 0 {
			include.Insert(name)
		} else {
			exclude.Insert(name)
		}
	}
	return func(name string) bool {
		if include.Len() != 0 && !include.Has(name) {
			return false
		}
		if exclude.Len() != 0 && exclude.Has(name) {
			return false
		}
		return true
	}
}

func countPrefix(name string, ch byte) int {
	i := 0
	for ; i < len(name); i++ {
		if name[i] != ch {
			break
		}
	}
	return i
}
