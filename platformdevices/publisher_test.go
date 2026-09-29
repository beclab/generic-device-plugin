// Copyright 2026 the generic-device-plugin authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package platformdevices

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

type fakeNodeClient struct {
	node       *corev1.Node
	patchCalls int
}

func (f *fakeNodeClient) Patch(_ context.Context, _ string, _ types.PatchType, data []byte, _ metav1.PatchOptions, _ ...string) (*corev1.Node, error) {
	f.patchCalls++
	var patch struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
			Labels      map[string]*string `json:"labels"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(data, &patch); err != nil {
		return nil, err
	}
	if f.node.Annotations == nil {
		f.node.Annotations = map[string]string{}
	}
	for key, value := range patch.Metadata.Annotations {
		f.node.Annotations[key] = value
	}
	if f.node.Labels == nil {
		f.node.Labels = map[string]string{}
	}
	for key, value := range patch.Metadata.Labels {
		if value == nil {
			delete(f.node.Labels, key)
			continue
		}
		f.node.Labels[key] = *value
	}
	return f.node.DeepCopy(), nil
}

type fakeLeaseClient struct {
	lease *coordinationv1.Lease
}

func (f *fakeLeaseClient) Get(_ context.Context, name string, _ metav1.GetOptions) (*coordinationv1.Lease, error) {
	if f.lease == nil {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: "coordination.k8s.io", Resource: "leases"}, name)
	}
	return f.lease.DeepCopy(), nil
}

func (f *fakeLeaseClient) Create(_ context.Context, lease *coordinationv1.Lease, _ metav1.CreateOptions) (*coordinationv1.Lease, error) {
	f.lease = lease.DeepCopy()
	return f.lease.DeepCopy(), nil
}

func (f *fakeLeaseClient) Update(_ context.Context, lease *coordinationv1.Lease, _ metav1.UpdateOptions) (*coordinationv1.Lease, error) {
	f.lease = lease.DeepCopy()
	return f.lease.DeepCopy(), nil
}

func TestPublisherUsesKubernetesClientForNodeAndLease(t *testing.T) {
	ctx := context.Background()
	nodes := &fakeNodeClient{node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}}
	leases := &fakeLeaseClient{}
	publisher := newPublisher("node-a", nodes, leases)
	firstRenewal := time.Date(2026, time.September, 14, 0, 10, 48, 561532000, time.UTC)
	publisher.now = func() time.Time { return firstRenewal }
	inventory := Inventory{
		SchemaVersion: 1,
		Serial: []Device{{
			UID:          "s-1",
			ResourceName: "devices.bytetrade.io/serial-s-1",
			Online:       true,
			Selectable:   true,
			KernelPaths:  []string{"/dev/ttyUSB0"},
		}},
	}

	if err := publisher.Publish(ctx, inventory, true); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if nodes.node.Labels[bluetoothLabel] != "true" {
		t.Fatalf("bluetooth label = %q, want true", nodes.node.Labels[bluetoothLabel])
	}
	var got Inventory
	if err := json.Unmarshal([]byte(nodes.node.Annotations[inventoryAnnotation]), &got); err != nil {
		t.Fatalf("unmarshal inventory annotation: %v", err)
	}
	if len(got.Serial) != 1 || got.Serial[0].ResourceName != inventory.Serial[0].ResourceName {
		t.Fatalf("inventory annotation = %#v", got)
	}
	lease := leases.lease
	if lease == nil || lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != "node-a" {
		t.Fatalf("lease holder = %v", lease)
	}
	if lease.Spec.LeaseDurationSeconds == nil || *lease.Spec.LeaseDurationSeconds != 30 {
		t.Fatalf("lease duration = %v", lease.Spec.LeaseDurationSeconds)
	}
	if lease.Spec.RenewTime == nil || !lease.Spec.RenewTime.Time.Equal(firstRenewal) {
		t.Fatalf("lease renewal = %v, want %v", lease.Spec.RenewTime, firstRenewal)
	}

	secondRenewal := firstRenewal.Add(5 * time.Second)
	publisher.now = func() time.Time { return secondRenewal }
	if err := publisher.Publish(ctx, inventory, false); err != nil {
		t.Fatalf("second Publish() error = %v", err)
	}
	if _, exists := nodes.node.Labels[bluetoothLabel]; exists {
		t.Fatalf("bluetooth label was not removed: %#v", nodes.node.Labels)
	}
	if leases.lease.Spec.RenewTime == nil || !leases.lease.Spec.RenewTime.Time.Equal(secondRenewal) {
		t.Fatalf("updated lease renewal = %v, want %v", leases.lease.Spec.RenewTime, secondRenewal)
	}
}

func TestPublisherSkipsUnchangedNodePatchButRenewsLease(t *testing.T) {
	ctx := context.Background()
	nodes := &fakeNodeClient{node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}}
	leases := &fakeLeaseClient{}
	publisher := newPublisher("node-a", nodes, leases)
	inventory := Inventory{SchemaVersion: 1}
	if err := publisher.Publish(ctx, inventory, false); err != nil {
		t.Fatalf("first Publish() error = %v", err)
	}
	if err := publisher.Publish(ctx, inventory, false); err != nil {
		t.Fatalf("second Publish() error = %v", err)
	}
	if nodes.patchCalls != 1 {
		t.Fatalf("node patch calls = %d, want 1", nodes.patchCalls)
	}
}
