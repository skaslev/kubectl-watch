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
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/skaslev/kubectl-watch/pkg/k8sconfig"
	"github.com/skaslev/kubectl-watch/pkg/signals"

	"github.com/spf13/pflag"
	"github.com/yudai/gojsondiff"
	"github.com/yudai/gojsondiff/formatter"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog"
)

const (
	spawnConcurrency = 4
	configQPS        = 6 * spawnConcurrency
	configBurst      = 100
)

var (
	masterURL             = pflag.String("master", "", "The address of the Kubernetes API server. Overrides any value in kubeconfig. Only required if out-of-cluster.")
	kubeconfig            = pflag.String("kubeconfig", "", "Path to a kubeconfig. Only required if out-of-cluster.")
	colorize              = pflag.BoolP("color", "c", true, "Colorize the output")
	outFormat             = pflag.StringP("out", "o", "", "Output format")
	namespaces            = pflag.StringSliceP("namespace", "n", nil, "Coma separated list of namespaces to watch")
	groupVersions         = pflag.StringSliceP("group-version", "g", nil, "Coma separated list of GroupVersions to watch")
	groupVersionResources = pflag.StringSliceP("group-version-resource", "r", nil, "Coma separated list of GroupVersionResources to watch")

	namespaceFilter   func(string) bool
	emptyUnstructured = &unstructured.Unstructured{Object: map[string]interface{}{}}
)

func getKey(o *unstructured.Unstructured) string {
	var buf strings.Builder
	if ns := o.GetNamespace(); len(ns) != 0 {
		buf.WriteString(ns)
		buf.WriteByte('/')
	}
	buf.WriteString(o.GetName())
	buf.WriteByte(' ')
	if api := o.GetAPIVersion(); len(api) != 0 {
		buf.WriteString(api)
		buf.WriteByte('/')
	}
	buf.WriteString(strings.ToLower(o.GetKind()))
	return buf.String()
}

func processEvent(event watch.Event, cache map[string]*unstructured.Unstructured) *Event {
	switch event.Type {
	case watch.Added, watch.Modified, watch.Deleted, watch.Bookmark:
	default:
		return nil
	}

	now := time.Now()
	new := event.Object.(*unstructured.Unstructured).DeepCopy()
	if !namespaceFilter(new.GetNamespace()) {
		return nil
	}

	key := getKey(new)
	old, ok := cache[key]
	if !ok {
		old = emptyUnstructured
	}
	if event.Type == watch.Deleted {
		old, new = new, emptyUnstructured
		delete(cache, key)
	} else {
		cache[key] = new
	}

	diff := gojsondiff.New().CompareObjects(old.Object, new.Object)
	if !diff.Modified() {
		return nil
	}

	formatter := formatter.NewAsciiFormatter(old.Object, formatter.AsciiFormatterConfig{Coloring: *colorize})
	text, err := formatter.Format(diff)
	if err != nil {
		klog.Error("error formatting diff: ", err)
		return nil
	}

	return &Event{now, key, text}
}

func processEvents(in <-chan watch.Event, out chan<- *Event, cache map[string]*unstructured.Unstructured, stopCh <-chan struct{}) bool {
	for {
		select {
		case <-stopCh:
			return false
		case event, ok := <-in:
			if !ok {
				return true
			}
			e := processEvent(event, cache)
			if e != nil {
				out <- e
			}
		}
	}
}

func watchResource(dc dynamic.Interface, gvr schema.GroupVersionResource, out chan<- *Event, cache map[string]*unstructured.Unstructured, stopCh <-chan struct{}) {
	for {
		var w watch.Interface
		err := wait.PollImmediateUntil(time.Second, func() (done bool, err error) {
			w, err = dc.Resource(gvr).Watch(context.Background(), metav1.ListOptions{})
			if err != nil {
				if errors.IsNotFound(err) {
					return false, nil
				}
				return false, err
			}
			return true, nil
		}, stopCh)
		if err != nil {
			if err != wait.ErrWaitTimeout && !errors.IsMethodNotSupported(err) {
				klog.Errorf("error watching resources '%v': %v", gvr, err)
			}
			return
		}

		ok := processEvents(w.ResultChan(), out, cache, stopCh)
		w.Stop()
		if !ok {
			return
		}
	}
}

func cacheResource(dc dynamic.Interface, gvr schema.GroupVersionResource) map[string]*unstructured.Unstructured {
	cache := map[string]*unstructured.Unstructured{}
	objs, err := dc.Resource(gvr).List(context.Background(), metav1.ListOptions{})
	if err == nil {
		for _, o := range objs.Items {
			cache[getKey(&o)] = o.DeepCopy()
		}
	}
	return cache
}

func spawnWatchers(dc dynamic.Interface, in <-chan schema.GroupVersionResource, out chan<- *Event, stopCh <-chan struct{}) {
	for {
		select {
		case <-stopCh:
			return
		case gvr, ok := <-in:
			if !ok {
				return
			}
			cache := cacheResource(dc, gvr)
			go watchResource(dc, gvr, out, cache, stopCh)
		}
	}
}

func filterResources(resources []*metav1.APIResourceList, in chan<- schema.GroupVersionResource, gvFilter, gvrFilter func(string) bool, stopCh <-chan struct{}) {
	defer close(in)
	for _, g := range resources {
		if !gvFilter(g.GroupVersion) {
			continue
		}

		gv, err := schema.ParseGroupVersion(g.GroupVersion)
		if err != nil {
			klog.Error("error parsing GroupVersion: ", err)
			continue
		}

		for _, r := range g.APIResources {
			if !gvrFilter(g.GroupVersion + "/" + r.Name) {
				continue
			}

			select {
			case <-stopCh:
				return
			case in <- schema.GroupVersionResource{gv.Group, gv.Version, r.Name}:
			}
		}
	}
}

func printEvents(out <-chan *Event, format func(*Event) string, stopCh <-chan struct{}) {
	for {
		select {
		case <-stopCh:
			return
		case e := <-out:
			fmt.Print(format(e))
		}
	}
}

func flushEvents(out <-chan *Event, format func(*Event) string) {
	for {
		select {
		default:
			return
		case e := <-out:
			fmt.Print(format(e))
		}
	}
}

func main() {
	pflag.Parse()

	namespaceFilter = NewFilter(*namespaces)
	gvFilter := NewFilter(*groupVersions)
	gvrFilter := NewFilter(*groupVersionResources)
	var formatter EventFormatter
	switch *outFormat {
	default:
		formatter = &DefaultFormatter{}
	case "trace":
		formatter = &TraceEventFormatter{}
		*colorize = false
	}

	cfg, err := k8sconfig.GetConfig(*masterURL, *kubeconfig)
	if err != nil {
		klog.Fatal("error building kubeconfig: ", err)
	}
	cfg.QPS = configQPS
	cfg.Burst = configBurst

	c, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		klog.Fatal("error creating kubernetes client: ", err)
	}

	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		klog.Fatal("error creating dynamic client: ", err)
	}

	resources, err := c.Discovery().ServerPreferredResources()
	if err != nil {
		klog.Fatal("error getting resources: ", err)
	}

	stopCh := signals.SetupSignalHandler()
	in := make(chan schema.GroupVersionResource, spawnConcurrency)
	out := make(chan *Event, 100)
	for i := 0; i < spawnConcurrency; i++ {
		go spawnWatchers(dc, in, out, stopCh)
	}
	filterResources(resources, in, gvFilter, gvrFilter, stopCh)

	fmt.Print(formatter.Preamble())
	printEvents(out, formatter.Format, stopCh)
	flushEvents(out, formatter.Format)
	fmt.Print(formatter.Epilogue())
}
