package nat

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/libp2p/go-nat/pcp"
)

// pcpPortMapper is the part of pcp.Client needed for IPv6 pinholes. The
// interface keeps dual-stack behavior testable without network access.
type pcpPortMapper interface {
	AddPortMapping(context.Context, string, int, time.Duration) (*pcp.MapResponse, error)
	DeletePortMapping(context.Context, string, int) error
}

// natWithPCPIPv6 adds best-effort PCPv6 pinholes to an IPv4 NAT.
type natWithPCPIPv6 struct {
	NAT
	pcp6 pcpPortMapper
}

var _ NAT = (*natWithPCPIPv6)(nil)

func newNATWithPCPIPv6(ipv4 NAT, pcp6 pcpPortMapper) NAT {
	return &natWithPCPIPv6{NAT: ipv4, pcp6: pcp6}
}

func (n *natWithPCPIPv6) AddPortMapping(ctx context.Context, protocol string, internalPort int, description string, timeout time.Duration) (int, error) {
	pcp6Done := make(chan struct{})
	var pcp6Err error
	go func() {
		_, pcp6Err = n.pcp6.AddPortMapping(ctx, protocol, internalPort, timeout)
		close(pcp6Done)
	}()

	port, err := n.NAT.AddPortMapping(ctx, protocol, internalPort, description, timeout)
	<-pcp6Done
	if err != nil && pcp6Err == nil {
		// The caller sees a failed mapping and will never delete it, so close
		// the v6 pinhole rather than leaking it until its lifetime expires.
		// Detached from ctx because the v4 failure may be the caller's
		// deadline expiring.
		delCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = n.pcp6.DeletePortMapping(delCtx, protocol, internalPort)
	}
	return port, err
}

func (n *natWithPCPIPv6) DeletePortMapping(ctx context.Context, protocol string, internalPort int) error {
	pcp6Done := make(chan struct{})
	var pcp6Err error
	go func() {
		pcp6Err = n.pcp6.DeletePortMapping(ctx, protocol, internalPort)
		close(pcp6Done)
	}()

	err := n.NAT.DeletePortMapping(ctx, protocol, internalPort)
	<-pcp6Done
	if pcp6Err != nil {
		pcp6Err = fmt.Errorf("pcp ipv6: %w", pcp6Err)
	}
	return errors.Join(err, pcp6Err)
}
