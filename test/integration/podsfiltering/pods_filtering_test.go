/*
Copyright 2015 The Kubernetes Authors.

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

package podsfiltering

import (
	"context"
	"fmt"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/kubernetes/pkg/kubelet/config"
	"k8s.io/kubernetes/pkg/kubelet/types"
	"testing"
	"time"

	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientset "k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/kubernetes/test/integration/framework"
)

func TestPodsFiltering(t *testing.T) {
	_, s, closeFn := framework.RunAMaster(nil)
	defer closeFn()

	ns := framework.CreateTestingNamespace("pods-filtering", s, t)
	defer framework.DeleteTestingNamespace(ns, s, t)

	client := clientset.NewForConfigOrDie(&restclient.Config{
		Host: s.URL,
		ContentConfig: restclient.ContentConfig{
			GroupVersion: &schema.GroupVersion{
				Group:   "",
				Version: "v1",
			},
		},
		QPS:   1000,
		Burst: 1000,
	})

	updates := make(chan interface{})
	pods := map[string]bool{}
	go func() {
		for {
			select {
			case val, ok := <-updates:
				if !ok {
					return
				}
				update := val.(types.PodUpdate)
				if update.Op == types.SET {
					for _, pod := range update.Pods {
						pods[pod.Name] = true
					}
				}
			}
		}
	}()

	stop := make(chan struct{})
	defer close(stop)

	config.NewSourceApiserverWithStop(client, "test-node", updates, stop)

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "noname",
			Labels: map[string]string{
				"network.titus.com/eni": "unallocated",
			},
		},
		Spec: v1.PodSpec{
			NodeName: "test-node",
			Containers: []v1.Container{
				{
					Name:  "fake-name",
					Image: "fake-image",
				},
			},
		},
	}

	for i := 0; i < 20; i++ {
		pod.ObjectMeta.Name = fmt.Sprintf("pod%02d", i)
		if i > 9 {
			pod.ObjectMeta.Labels["network.titus.com/eni"] = "allocated"
		}
		_, err := client.CoreV1().Pods(ns.Name).Create(context.TODO(), pod, metav1.CreateOptions{})
		if err != nil {
			t.Errorf("failed to create pod %s, %s", pod.ObjectMeta.Name, err)
		}
	}

	err := wait.PollImmediate(2*time.Second, 10*time.Second, func() (done bool, err error) {
		return len(pods) == 10, nil
	})
	close(updates)

	if err != nil {
		t.Errorf("%d pods got selected", len(pods))
	}

	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("pod%02d", i)
		if i > 9 {
			if !pods[name] {
				t.Errorf("%s not selected", name)
			}
		} else {
			if pods[name] {
				t.Errorf("%s got selected", name)
			}
		}
	}
}
