package local

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
)

// fakeNode is one node in a fake `kubectl get nodes -o json` list.
type fakeNode struct {
	name         string
	controlPlane bool
	ready        bool
	capCPU       string
	capMemory    string
	allocCPU     string
	allocMemory  string
}

func nodeListJSON(t *testing.T, nodes ...fakeNode) []byte {
	t.Helper()
	type item struct {
		Metadata map[string]any `json:"metadata"`
		Status   map[string]any `json:"status"`
	}
	var items []item
	for _, n := range nodes {
		labels := map[string]string{}
		if n.controlPlane {
			labels["node-role.kubernetes.io/control-plane"] = ""
		}
		ready := "False"
		if n.ready {
			ready = "True"
		}
		items = append(items, item{
			Metadata: map[string]any{"name": n.name, "labels": labels},
			Status: map[string]any{
				"capacity":    map[string]string{"cpu": n.capCPU, "memory": n.capMemory},
				"allocatable": map[string]string{"cpu": n.allocCPU, "memory": n.allocMemory},
				"conditions":  []map[string]string{{"type": "Ready", "status": ready}},
			},
		})
	}
	b, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// kindHostNode is a node as kind presents it on an 8-CPU, 16Gi host with no
// reservations: allocatable equals capacity.
func kindHostNode(name string, controlPlane bool) fakeNode {
	return fakeNode{name: name, controlPlane: controlPlane, ready: true,
		capCPU: "8", capMemory: "16Gi", allocCPU: "8", allocMemory: "16Gi"}
}

// fakeShapeOps returns `before` until reserve has been called for every
// worker, then `after`.
type fakeShapeOps struct {
	before, after []byte
	configz       string
	workers       int
	reserved      map[string][2]string
	reserveErr    error
}

func (f *fakeShapeOps) listNodes(context.Context, string) ([]byte, error) {
	if len(f.reserved) >= f.workers && f.after != nil {
		return f.after, nil
	}
	return f.before, nil
}

func (f *fakeShapeOps) kubeletConfig(context.Context, string, string) ([]byte, error) {
	return []byte(f.configz), nil
}

func (f *fakeShapeOps) reserve(_ context.Context, node string, cpu, memory resource.Quantity) error {
	if f.reserveErr != nil {
		return f.reserveErr
	}
	if f.reserved == nil {
		f.reserved = map[string][2]string{}
	}
	f.reserved[node] = [2]string{cpu.String(), memory.String()}
	return nil
}

// kindConfigz is the eviction configuration kind ships: only nodefs/imagefs
// signals, so no memory.available threshold is subtracted.
const kindConfigz = `{"kubeletconfig":{"evictionHard":{"imagefs.available":"0%","nodefs.available":"0%","nodefs.inodesFree":"0%"}}}`

func mustTarget(t *testing.T) allocatableTarget {
	t.Helper()
	target, err := oasisAllocatableTarget()
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func TestPlanReservation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		capCPU, capMem   string
		kc               kubeletReservations
		wantCPU, wantMem string
		wantUnreachable  bool
		wantErr          string
	}{
		{
			name:   "no eviction threshold",
			capCPU: "8", capMem: "16Gi",
			wantCPU: "4", wantMem: "12Gi",
		},
		{
			name:   "absolute eviction threshold is subtracted",
			capCPU: "10", capMem: "16Gi",
			kc:      kubeletReservations{evictionHard: map[string]string{"memory.available": "100Mi"}},
			wantCPU: "6", wantMem: "12188Mi",
		},
		{
			name:   "percentage eviction threshold is a share of capacity",
			capCPU: "8", capMem: "16Gi",
			kc:      kubeletReservations{evictionHard: map[string]string{"memory.available": "25%"}},
			wantCPU: "4", wantMem: "8Gi",
		},
		{
			name:   "existing kubeReserved is accounted for",
			capCPU: "8", capMem: "16Gi",
			kc:      kubeletReservations{kubeReserved: map[string]string{"cpu": "500m", "memory": "1Gi"}},
			wantCPU: "3500m", wantMem: "11Gi",
		},
		{
			name:   "exact fit reserves nothing",
			capCPU: "4", capMem: "4Gi",
			wantCPU: "0", wantMem: "0",
		},
		{
			name:   "a ~7.75GiB Docker VM reaches the target",
			capCPU: "10", capMem: "8124512Ki",
			wantCPU: "6", wantMem: "3930208Ki",
		},
		{
			name:   "host memory below target is unreachable",
			capCPU: "10", capMem: "3Gi",
			wantUnreachable: true,
		},
		{
			name:   "eviction threshold pushes an exact host below target",
			capCPU: "4", capMem: "4Gi",
			kc:              kubeletReservations{evictionHard: map[string]string{"memory.available": "100Mi"}},
			wantUnreachable: true,
		},
		{
			name:   "host cpu below target is unreachable",
			capCPU: "2", capMem: "16Gi",
			wantUnreachable: true,
		},
		{
			name:   "existing systemReserved is refused",
			capCPU: "8", capMem: "16Gi",
			kc:      kubeletReservations{systemReserved: map[string]string{"cpu": "1"}},
			wantErr: "already carries systemReserved",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			n := shapeNode{name: "w", capCPU: resource.MustParse(tt.capCPU), capMemory: resource.MustParse(tt.capMem)}
			got, err := planReservation(n, tt.kc, mustTarget(t))
			var unreachable *errTargetUnreachable
			switch {
			case tt.wantUnreachable:
				if !errors.As(err, &unreachable) {
					t.Fatalf("want unreachable, got %v", err)
				}
				return
			case tt.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) || errors.As(err, &unreachable) {
					t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
				}
				return
			case err != nil:
				t.Fatalf("unexpected error: %v", err)
			}
			if got.cpu.Cmp(resource.MustParse(tt.wantCPU)) != 0 || got.memory.Cmp(resource.MustParse(tt.wantMem)) != 0 {
				t.Errorf("reservation = cpu %s memory %s, want cpu %s memory %s", got.cpu.String(), got.memory.String(), tt.wantCPU, tt.wantMem)
			}
			// The arithmetic must land allocatable exactly on the target.
			alloc := n.capMemory.DeepCopy()
			alloc.Sub(got.memory)
			alloc.Sub(got.eviction)
			if kr, ok := tt.kc.kubeReserved["memory"]; ok {
				alloc.Sub(resource.MustParse(kr))
			}
			if alloc.Cmp(resource.MustParse(OASISNodeAllocatableMemory)) != 0 {
				t.Errorf("resulting allocatable memory %s != target %s", alloc.String(), OASISNodeAllocatableMemory)
			}
		})
	}
}

func TestShapeWorkers(t *testing.T) {
	t.Parallel()

	shaped := func(name string) fakeNode {
		n := kindHostNode(name, false)
		n.allocCPU, n.allocMemory = "4", "4Gi"
		return n
	}

	t.Run("shapes every worker and leaves the control plane alone", func(t *testing.T) {
		t.Parallel()
		ops := &fakeShapeOps{
			before:  nodeListJSON(t, kindHostNode("lab-control-plane", true), kindHostNode("lab-worker", false), kindHostNode("lab-worker2", false)),
			after:   nodeListJSON(t, kindHostNode("lab-control-plane", true), shaped("lab-worker"), shaped("lab-worker2")),
			configz: kindConfigz,
			workers: 2,
		}
		shape, err := shapeWorkers(context.Background(), ops, "kc", mustTarget(t), time.Second, time.Millisecond)
		if err != nil {
			t.Fatalf("shapeWorkers: %v", err)
		}
		if !shape.Shaped || shape.Reason != "" || len(shape.Nodes) != 2 {
			t.Fatalf("shape = %+v, want shaped with 2 worker nodes", shape)
		}
		if _, touched := ops.reserved["lab-control-plane"]; touched {
			t.Error("control plane was given a reservation")
		}
		if got := ops.reserved["lab-worker"]; got != [2]string{"4", "12Gi"} {
			t.Errorf("lab-worker reservation = %v, want [4 12Gi]", got)
		}
	})

	t.Run("host too small yields an unshaped record and touches nothing", func(t *testing.T) {
		t.Parallel()
		small := fakeNode{name: "lab-worker", ready: true, capCPU: "10", capMemory: "3Gi", allocCPU: "10", allocMemory: "3Gi"}
		ops := &fakeShapeOps{before: nodeListJSON(t, small), configz: kindConfigz, workers: 1}
		shape, err := shapeWorkers(context.Background(), ops, "kc", mustTarget(t), time.Second, time.Millisecond)
		if err != nil {
			t.Fatalf("shapeWorkers: %v", err)
		}
		if shape.Shaped || !strings.Contains(shape.Reason, "3Gi") || len(ops.reserved) != 0 {
			t.Fatalf("shape = %+v reserved = %v, want unshaped naming capacity and no reservations", shape, ops.reserved)
		}
		if len(shape.Nodes) != 1 || shape.Nodes[0].Memory != "3Gi" {
			t.Errorf("unshaped record should carry the as-found allocatable: %+v", shape.Nodes)
		}
	})

	t.Run("single-node lab has no workers to shape", func(t *testing.T) {
		t.Parallel()
		ops := &fakeShapeOps{before: nodeListJSON(t, kindHostNode("lab-control-plane", true)), configz: kindConfigz}
		shape, err := shapeWorkers(context.Background(), ops, "kc", mustTarget(t), time.Second, time.Millisecond)
		if err != nil {
			t.Fatalf("shapeWorkers: %v", err)
		}
		if shape.Shaped || !strings.Contains(shape.Reason, "no worker nodes") {
			t.Fatalf("shape = %+v, want unshaped for want of workers", shape)
		}
	})

	t.Run("a kubelet that never reports the target fails the create", func(t *testing.T) {
		t.Parallel()
		ops := &fakeShapeOps{
			before:  nodeListJSON(t, kindHostNode("lab-worker", false)),
			after:   nodeListJSON(t, kindHostNode("lab-worker", false)),
			configz: kindConfigz,
			workers: 1,
		}
		_, err := shapeWorkers(context.Background(), ops, "kc", mustTarget(t), 10*time.Millisecond, time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "did not report allocatable") {
			t.Fatalf("want timeout error, got %v", err)
		}
	})

	t.Run("a failed reservation fails the create", func(t *testing.T) {
		t.Parallel()
		ops := &fakeShapeOps{
			before:     nodeListJSON(t, kindHostNode("lab-worker", false)),
			configz:    kindConfigz,
			workers:    1,
			reserveErr: errors.New("docker exec failed"),
		}
		_, err := shapeWorkers(context.Background(), ops, "kc", mustTarget(t), time.Second, time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "docker exec failed") {
			t.Fatalf("want reserve error, got %v", err)
		}
	})
}
