package local

import (
	"fmt"
	"net"

	"github.com/jaimegago/petri/pkg/types"
)

// The NodePorts the ingress-nginx Service exposes inside every local lab
// (templates/platform/ingress-nginx.yaml.tmpl). They are container ports on
// the kind control-plane node, so every lab can use the same pair; only the
// host side of the mapping must differ between labs.
const (
	ingressHTTPNodePort  = 30080
	ingressHTTPSNodePort = 30443
)

// portAllocator picks host ports for a lab's kind port mappings.
type portAllocator interface {
	// ingressHostPorts returns two distinct host ports that are free at the
	// time of the call.
	ingressHostPorts() (types.IngressHostPorts, error)
}

// osPortAllocator asks the operating system for free ephemeral ports.
//
// A fixed pair (the old 30080/30443) let only one lab exist per host. Probing
// a preferred pair first would bring that collision back for two labs created
// concurrently, since both would see it free; ephemeral ports are handed out
// by the kernel, so two concurrent creates do not draw the same pair. The
// ports are released before kind binds them, which leaves a narrow window in
// which another process could take one; kind create then fails and the
// create rolls back like any other failure.
type osPortAllocator struct{}

func (osPortAllocator) ingressHostPorts() (types.IngressHostPorts, error) {
	// Hold both listeners until both are chosen so the two ports differ.
	// The wildcard address matches kind's default listenAddress (0.0.0.0).
	httpL, err := net.Listen("tcp", ":0")
	if err != nil {
		return types.IngressHostPorts{}, fmt.Errorf("allocating ingress HTTP host port: %w", err)
	}
	defer httpL.Close() //nolint:errcheck
	httpsL, err := net.Listen("tcp", ":0")
	if err != nil {
		return types.IngressHostPorts{}, fmt.Errorf("allocating ingress HTTPS host port: %w", err)
	}
	defer httpsL.Close() //nolint:errcheck
	httpPort, err := listenerPort(httpL)
	if err != nil {
		return types.IngressHostPorts{}, err
	}
	httpsPort, err := listenerPort(httpsL)
	if err != nil {
		return types.IngressHostPorts{}, err
	}
	return types.IngressHostPorts{HTTP: httpPort, HTTPS: httpsPort}, nil
}

func listenerPort(l net.Listener) (int, error) {
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok || addr.Port == 0 {
		return 0, fmt.Errorf("allocating host port: unexpected listener address %v", l.Addr())
	}
	return addr.Port, nil
}
