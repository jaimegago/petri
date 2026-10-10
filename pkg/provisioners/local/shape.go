package local

// Allocatable shaping for OASIS labs.
//
// Allocatable is a lab property, shaped once at creation; a scenario only
// verifies it (pkg/oasis compares a node entry's allocatable_cpu /
// allocatable_memory against the bound node's status.allocatable). Nodes are
// shared by every scenario on the lab, so no provision ever reconfigures one.
//
// kind nodes see the whole Docker host, so shaping works downward: each
// worker's kubelet is given a systemReserved reservation sized so that
//
//	allocatable = capacity - kubeReserved - systemReserved - evictionHard[memory.available]
//
// lands exactly on the target. When the host is too small to reach the target
// the lab is still created, unshaped, and the reason is recorded in lab state.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/jaimegago/petri/pkg/types"
)

// OASISNodeAllocatableCPU and OASISNodeAllocatableMemory are the homogeneous
// worker allocatable every OASIS lab is shaped to: the cluster shape the SI
// profile's C-OE-002 (infra.capability.oe.stateful-scaling-001) declares on
// each of its three nodes. A scenario declaring a different allocatable is
// refused at provision rather than served as fiction.
const (
	OASISNodeAllocatableCPU    = "4000m"
	OASISNodeAllocatableMemory = "8Gi"
)

const (
	// shapeTimeout bounds the wait for every restarted kubelet to report
	// the shaped allocatable and Ready again.
	shapeTimeout = 3 * time.Minute
	// shapePollInterval is the node-status poll cadence while waiting.
	shapePollInterval = 3 * time.Second
	// evictionMemorySignal is the kubelet hard-eviction signal that is
	// subtracted from memory capacity when computing allocatable.
	evictionMemorySignal = "memory.available"
)

// allocatableTarget is a parsed shaping target. The text forms keep the
// target as written ("4000m"), which Quantity.String would normalise ("4").
type allocatableTarget struct {
	cpu        resource.Quantity
	memory     resource.Quantity
	cpuText    string
	memoryText string
}

// oasisAllocatableTarget parses the named OASIS target constants.
func oasisAllocatableTarget() (allocatableTarget, error) {
	return parseAllocatableTarget(OASISNodeAllocatableCPU, OASISNodeAllocatableMemory)
}

func parseAllocatableTarget(cpu, memory string) (allocatableTarget, error) {
	c, err := resource.ParseQuantity(cpu)
	if err != nil {
		return allocatableTarget{}, fmt.Errorf("parsing allocatable cpu target %q: %w", cpu, err)
	}
	m, err := resource.ParseQuantity(memory)
	if err != nil {
		return allocatableTarget{}, fmt.Errorf("parsing allocatable memory target %q: %w", memory, err)
	}
	return allocatableTarget{cpu: c, memory: m, cpuText: cpu, memoryText: memory}, nil
}

// shapeOps abstracts the cluster and node-container calls shaping needs, so
// tests can drive the flow without a cluster.
type shapeOps interface {
	// listNodes returns `kubectl get nodes -o json`.
	listNodes(ctx context.Context, kubeconfig string) ([]byte, error)
	// kubeletConfig returns the node's live kubelet configz document.
	kubeletConfig(ctx context.Context, kubeconfig, node string) ([]byte, error)
	// reserve gives the node's kubelet a systemReserved reservation and
	// restarts it.
	reserve(ctx context.Context, node string, cpu, memory resource.Quantity) error
}

// shapeNode is the subset of a Node object shaping reads.
type shapeNode struct {
	name        string
	worker      bool
	ready       bool
	capCPU      resource.Quantity
	capMemory   resource.Quantity
	allocCPU    resource.Quantity
	allocMemory resource.Quantity
}

// parseShapeNodes reads a `kubectl get nodes -o json` list.
func parseShapeNodes(raw []byte) ([]shapeNode, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				Capacity    map[string]string `json:"capacity"`
				Allocatable map[string]string `json:"allocatable"`
				Conditions  []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parsing node list: %w", err)
	}
	nodes := make([]shapeNode, 0, len(list.Items))
	for _, it := range list.Items {
		n := shapeNode{name: it.Metadata.Name}
		_, cp := it.Metadata.Labels["node-role.kubernetes.io/control-plane"]
		n.worker = !cp
		for _, c := range it.Status.Conditions {
			if c.Type == "Ready" {
				n.ready = c.Status == "True"
			}
		}
		var err error
		if n.capCPU, err = parseNodeQuantity(it.Status.Capacity, "cpu", n.name, "capacity"); err != nil {
			return nil, err
		}
		if n.capMemory, err = parseNodeQuantity(it.Status.Capacity, "memory", n.name, "capacity"); err != nil {
			return nil, err
		}
		if n.allocCPU, err = parseNodeQuantity(it.Status.Allocatable, "cpu", n.name, "allocatable"); err != nil {
			return nil, err
		}
		if n.allocMemory, err = parseNodeQuantity(it.Status.Allocatable, "memory", n.name, "allocatable"); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func parseNodeQuantity(m map[string]string, key, node, field string) (resource.Quantity, error) {
	v, ok := m[key]
	if !ok {
		return resource.Quantity{}, fmt.Errorf("node %q reports no %s %s", node, field, key)
	}
	q, err := resource.ParseQuantity(v)
	if err != nil {
		return resource.Quantity{}, fmt.Errorf("node %q: parsing %s %s %q: %w", node, field, key, v, err)
	}
	return q, nil
}

// kubeletReservations is what the live kubelet config already subtracts
// from capacity.
type kubeletReservations struct {
	evictionHard   map[string]string
	kubeReserved   map[string]string
	systemReserved map[string]string
}

func parseKubeletConfigz(raw []byte) (kubeletReservations, error) {
	var doc struct {
		KubeletConfig struct {
			EvictionHard   map[string]string `json:"evictionHard"`
			KubeReserved   map[string]string `json:"kubeReserved"`
			SystemReserved map[string]string `json:"systemReserved"`
		} `json:"kubeletconfig"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return kubeletReservations{}, fmt.Errorf("parsing kubelet configz: %w", err)
	}
	return kubeletReservations{
		evictionHard:   doc.KubeletConfig.EvictionHard,
		kubeReserved:   doc.KubeletConfig.KubeReserved,
		systemReserved: doc.KubeletConfig.SystemReserved,
	}, nil
}

// reservation is the systemReserved a worker needs to land on the target.
type reservation struct {
	cpu    resource.Quantity
	memory resource.Quantity
	// eviction is the memory.available hard-eviction threshold accounted
	// for, kept for the unreachable-target message.
	eviction resource.Quantity
}

// errTargetUnreachable marks a host too small for the target: the lab is
// created unshaped rather than failed.
type errTargetUnreachable struct{ msg string }

func (e *errTargetUnreachable) Error() string { return e.msg }

// planReservation computes the systemReserved that makes a node's allocatable
// exactly the target:
//
//	systemReserved.cpu    = capacity.cpu    - kubeReserved.cpu    - target.cpu
//	systemReserved.memory = capacity.memory - kubeReserved.memory - eviction(memory.available) - target.memory
//
// A negative result means the host cannot reach the target; that is an
// *errTargetUnreachable, not a failure.
func planReservation(n shapeNode, kc kubeletReservations, target allocatableTarget) (reservation, error) {
	if len(kc.systemReserved) > 0 {
		return reservation{}, fmt.Errorf("node %q: kubelet already carries systemReserved %v; refusing to shape over it", n.name, kc.systemReserved)
	}
	eviction, err := evictionMemoryThreshold(kc.evictionHard, n.capMemory)
	if err != nil {
		return reservation{}, fmt.Errorf("node %q: %w", n.name, err)
	}
	kubeCPU, err := optionalQuantity(kc.kubeReserved, "cpu")
	if err != nil {
		return reservation{}, fmt.Errorf("node %q: kubeReserved: %w", n.name, err)
	}
	kubeMem, err := optionalQuantity(kc.kubeReserved, "memory")
	if err != nil {
		return reservation{}, fmt.Errorf("node %q: kubeReserved: %w", n.name, err)
	}

	cpu := n.capCPU.DeepCopy()
	cpu.Sub(kubeCPU)
	cpu.Sub(target.cpu)
	mem := n.capMemory.DeepCopy()
	mem.Sub(kubeMem)
	mem.Sub(eviction)
	mem.Sub(target.memory)

	if cpu.Sign() < 0 || mem.Sign() < 0 {
		return reservation{}, &errTargetUnreachable{msg: fmt.Sprintf(
			"worker %q has capacity cpu=%s memory=%s (kubeReserved cpu=%s memory=%s, eviction threshold memory.available=%s), which cannot reach allocatable cpu=%s memory=%s; the Docker host is too small",
			n.name, n.capCPU.String(), n.capMemory.String(), kubeCPU.String(), kubeMem.String(), eviction.String(), target.cpuText, target.memoryText)}
	}
	return reservation{cpu: cpu, memory: mem, eviction: eviction}, nil
}

// evictionMemoryThreshold resolves the kubelet's memory.available hard
// eviction threshold, absolute or a percentage of capacity, the way the
// kubelet does when it computes allocatable. An absent signal is zero.
func evictionMemoryThreshold(evictionHard map[string]string, capacity resource.Quantity) (resource.Quantity, error) {
	v, ok := evictionHard[evictionMemorySignal]
	if !ok || strings.TrimSpace(v) == "" {
		return resource.Quantity{}, nil
	}
	if pct, isPct := strings.CutSuffix(strings.TrimSpace(v), "%"); isPct {
		f, err := strconv.ParseFloat(pct, 64)
		if err != nil {
			return resource.Quantity{}, fmt.Errorf("parsing eviction threshold %s=%q: %w", evictionMemorySignal, v, err)
		}
		return *resource.NewQuantity(int64(float64(capacity.Value())*f/100), resource.BinarySI), nil
	}
	q, err := resource.ParseQuantity(v)
	if err != nil {
		return resource.Quantity{}, fmt.Errorf("parsing eviction threshold %s=%q: %w", evictionMemorySignal, v, err)
	}
	return q, nil
}

func optionalQuantity(m map[string]string, key string) (resource.Quantity, error) {
	v, ok := m[key]
	if !ok || strings.TrimSpace(v) == "" {
		return resource.Quantity{}, nil
	}
	q, err := resource.ParseQuantity(v)
	if err != nil {
		return resource.Quantity{}, fmt.Errorf("parsing %s %q: %w", key, v, err)
	}
	return q, nil
}

// matchesTarget reports exact quantity equality with the target.
func matchesTarget(n shapeNode, target allocatableTarget) bool {
	return n.allocCPU.Cmp(target.cpu) == 0 && n.allocMemory.Cmp(target.memory) == 0
}

func workerAllocatables(nodes []shapeNode) []types.NodeAllocatable {
	var out []types.NodeAllocatable
	for _, n := range nodes {
		if n.worker {
			out = append(out, types.NodeAllocatable{Name: n.name, CPU: n.allocCPU.String(), Memory: n.allocMemory.String()})
		}
	}
	return out
}

// shapeWorkers shapes every worker of the cluster to the target allocatable.
// It returns an unshaped NodeShape (and no error) when the lab has no workers
// or the host cannot reach the target; an error only when the shaping
// mechanism itself fails.
func shapeWorkers(ctx context.Context, ops shapeOps, kubeconfig string, target allocatableTarget, timeout, poll time.Duration) (*types.NodeShape, error) {
	shape := &types.NodeShape{TargetCPU: target.cpuText, TargetMemory: target.memoryText}

	raw, err := ops.listNodes(ctx, kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}
	nodes, err := parseShapeNodes(raw)
	if err != nil {
		return nil, err
	}
	var workers []shapeNode
	for _, n := range nodes {
		if n.worker {
			workers = append(workers, n)
		}
	}
	if len(workers) == 0 {
		shape.Reason = "the lab has no worker nodes; shaping applies to workers only (a level-1 lab is single-node)"
		return shape, nil
	}

	plans := make([]reservation, len(workers))
	for i, w := range workers {
		kcRaw, err := ops.kubeletConfig(ctx, kubeconfig, w.name)
		if err != nil {
			return nil, fmt.Errorf("reading kubelet config of %q: %w", w.name, err)
		}
		kc, err := parseKubeletConfigz(kcRaw)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", w.name, err)
		}
		plan, err := planReservation(w, kc, target)
		if err != nil {
			var unreachable *errTargetUnreachable
			if errors.As(err, &unreachable) {
				shape.Reason = unreachable.msg
				shape.Nodes = workerAllocatables(nodes)
				return shape, nil
			}
			return nil, err
		}
		plans[i] = plan
	}

	for i, w := range workers {
		if err := ops.reserve(ctx, w.name, plans[i].cpu, plans[i].memory); err != nil {
			return nil, fmt.Errorf("reserving kubelet resources on %q: %w", w.name, err)
		}
	}

	deadline := time.Now().Add(timeout)
	for {
		raw, err := ops.listNodes(ctx, kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("listing nodes after shaping: %w", err)
		}
		nodes, err := parseShapeNodes(raw)
		if err != nil {
			return nil, err
		}
		var pending []string
		for _, n := range nodes {
			if n.worker && (!n.ready || !matchesTarget(n, target)) {
				pending = append(pending, fmt.Sprintf("%s (ready=%t allocatable cpu=%s memory=%s)", n.name, n.ready, n.allocCPU.String(), n.allocMemory.String()))
			}
		}
		if len(pending) == 0 {
			shape.Shaped = true
			shape.Nodes = workerAllocatables(nodes)
			return shape, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("workers did not report allocatable cpu=%s memory=%s within %s: %s",
				target.cpuText, target.memoryText, timeout, strings.Join(pending, "; "))
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for shaped allocatable: %w", ctx.Err())
		case <-time.After(poll):
		}
	}
}

// cliShapeOps implements shapeOps with kubectl and docker exec. kind names
// each node's container after the node, so the node name addresses both.
type cliShapeOps struct{}

func (cliShapeOps) listNodes(ctx context.Context, kubeconfig string) ([]byte, error) {
	out, err := runOutput(ctx, "kubectl", "--kubeconfig", kubeconfig, "get", "nodes", "-o", "json")
	return []byte(out), err
}

func (cliShapeOps) kubeletConfig(ctx context.Context, kubeconfig, node string) ([]byte, error) {
	out, err := runOutput(ctx, "kubectl", "--kubeconfig", kubeconfig, "get", "--raw", "/api/v1/nodes/"+node+"/proxy/configz")
	return []byte(out), err
}

// reserveScript appends a systemReserved block to the kind node's kubelet
// config and restarts the kubelet. It refuses a config that already carries
// one rather than producing a duplicate key. Values arrive as positional
// arguments, never interpolated into the script.
const reserveScript = `set -e
f=/var/lib/kubelet/config.yaml
if grep -q '^systemReserved:' "$f"; then echo "systemReserved already present in $f" >&2; exit 1; fi
printf '\nsystemReserved:\n  cpu: "%s"\n  memory: "%s"\n' "$1" "$2" >> "$f"
systemctl restart kubelet
`

func (cliShapeOps) reserve(ctx context.Context, node string, cpu, memory resource.Quantity) error {
	return runCmd(ctx, "docker", "exec", node, "sh", "-c", reserveScript, "sh", cpu.String(), memory.String())
}
