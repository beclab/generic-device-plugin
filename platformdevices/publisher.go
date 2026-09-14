package platformdevices

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	coordinationclient "k8s.io/client-go/kubernetes/typed/coordination/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
)

const (
	inventoryAnnotation   = "bytetrade.io/devices"
	bluetoothLabel        = "devices.bytetrade.io/bluetooth-capable"
	leaseNamespace        = "kube-node-lease"
	leasePrefix           = "generic-device-plugin-"
	maxInventoryJSONBytes = 200 * 1024
	leaseDuration         = 30 * time.Second
)

type nodeClient interface {
	Patch(context.Context, string, types.PatchType, []byte, metav1.PatchOptions, ...string) (*corev1.Node, error)
}

type leaseClient interface {
	Get(context.Context, string, metav1.GetOptions) (*coordinationv1.Lease, error)
	Create(context.Context, *coordinationv1.Lease, metav1.CreateOptions) (*coordinationv1.Lease, error)
	Update(context.Context, *coordinationv1.Lease, metav1.UpdateOptions) (*coordinationv1.Lease, error)
}

type Publisher struct {
	nodeName string
	nodes    nodeClient
	leases   leaseClient
	now      func() time.Time
	mu       sync.Mutex
	lastHash string
}

func NewPublisher(nodeName string) (*Publisher, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("create in-cluster Kubernetes config: %w", err)
	}
	core, err := coreclient.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes core client: %w", err)
	}
	coordination, err := coordinationclient.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes coordination client: %w", err)
	}
	return newPublisher(nodeName, core.Nodes(), coordination.Leases(leaseNamespace)), nil
}

func newPublisher(nodeName string, nodes nodeClient, leases leaseClient) *Publisher {
	return &Publisher{nodeName: nodeName, nodes: nodes, leases: leases, now: time.Now}
}

func (p *Publisher) Publish(ctx context.Context, inventory Inventory, bluetoothCapable bool) error {
	annotation, err := marshalInventory(inventory, maxInventoryJSONBytes)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(annotation)
	hash := hex.EncodeToString(sum[:]) + fmt.Sprintf(":%t", bluetoothCapable)
	p.mu.Lock()
	changed := p.lastHash != hash
	p.mu.Unlock()
	if changed {
		var bluetoothValue any
		if bluetoothCapable {
			bluetoothValue = "true"
		}
		payload, err := json.Marshal(map[string]any{"metadata": map[string]any{
			"annotations": map[string]string{inventoryAnnotation: string(annotation)},
			"labels":      map[string]any{bluetoothLabel: bluetoothValue},
		}})
		if err != nil {
			return fmt.Errorf("marshal node inventory patch: %w", err)
		}
		if _, err := p.nodes.Patch(ctx, p.nodeName, types.MergePatchType, payload, metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("patch node inventory: %w", err)
		}
		p.mu.Lock()
		p.lastHash = hash
		p.mu.Unlock()
	}
	return p.renewLease(ctx)
}

func (p *Publisher) renewLease(ctx context.Context) error {
	name := leasePrefix + p.nodeName
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		lease, err := p.leases.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			now := metav1.NewMicroTime(p.now().UTC())
			_, createErr := p.leases.Create(ctx, &coordinationv1.Lease{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: leaseNamespace},
				Spec: coordinationv1.LeaseSpec{
					HolderIdentity:       ptr.To(p.nodeName),
					LeaseDurationSeconds: ptr.To(int32(leaseDuration / time.Second)),
					RenewTime:            &now,
				},
			}, metav1.CreateOptions{})
			return createErr
		}
		if err != nil {
			return err
		}
		now := metav1.NewMicroTime(p.now().UTC())
		lease.Spec.HolderIdentity = ptr.To(p.nodeName)
		lease.Spec.LeaseDurationSeconds = ptr.To(int32(leaseDuration / time.Second))
		lease.Spec.RenewTime = &now
		_, err = p.leases.Update(ctx, lease, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return fmt.Errorf("renew lease: %w", err)
	}
	return nil
}

func marshalInventory(inventory Inventory, limit int) ([]byte, error) {
	data, err := json.Marshal(inventory)
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("device inventory JSON is %d bytes, exceeds %d byte limit", len(data), limit)
	}
	return data, nil
}
