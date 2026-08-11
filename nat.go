// Package nat implements NAT handling facilities
package nat

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"net"
	"sync"
	"time"
)

var ErrNoExternalAddress = errors.New("no external address")
var ErrNoInternalAddress = errors.New("no internal address")
var ErrNoNATFound = errors.New("no NAT found")

// protocol is either "udp" or "tcp"
type NAT interface {
	// Type returns the kind of NAT port mapping service that is used
	Type() string

	// GetDeviceAddress returns the internal address of the gateway device.
	GetDeviceAddress() (addr net.IP, err error)

	// GetExternalAddress returns the external address of the gateway device.
	GetExternalAddress() (addr net.IP, err error)

	// GetInternalAddress returns the address of the local host.
	GetInternalAddress() (addr net.IP, err error)

	// AddPortMapping maps a port on the local host to an external port.
	AddPortMapping(ctx context.Context, protocol string, internalPort int, description string, timeout time.Duration) (mappedExternalPort int, err error)

	// DeletePortMapping removes a port mapping.
	DeletePortMapping(ctx context.Context, protocol string, internalPort int) (err error)
}

// DiscoverNATs returns all NATs discovered in the network.
// Callers choose between the returned protocols; use DiscoverGateway for
// PCP preference and automatic dual-stack mapping.
func DiscoverNATs(ctx context.Context) <-chan NAT {
	return mergeNATs(ctx, discoverFallbackNATs(ctx), discoverPCP(ctx))
}

func discoverFallbackNATs(ctx context.Context) <-chan NAT {
	return mergeNATs(ctx,
		discoverUPNP_IG1(ctx),
		discoverUPNP_IG2(ctx),
		discoverUPNP_Unicast(ctx),
		discoverUPNP_GenIGDev(ctx),
		discoverNATPMP(ctx),
	)
}

func mergeNATs(ctx context.Context, sources ...<-chan NAT) <-chan NAT {
	nats := make(chan NAT)
	var wg sync.WaitGroup

	for _, source := range sources {
		if source == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case nat, ok := <-source:
					if !ok {
						return
					}
					select {
					case nats <- nat:
					case <-ctx.Done():
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(nats)
	}()
	return nats
}

// DiscoverGateway attempts to find a gateway device. It prefers PCP for IPv4,
// independently enables PCP for IPv6, then falls back to the first UPnP or
// NAT-PMP gateway discovered.
func DiscoverGateway(ctx context.Context) (NAT, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	return selectGateway(ctx, discoverPCPv4(ctx), discoverPCPv6(ctx), discoverFallbackNATs(ctx))
}

func selectGateway(ctx context.Context, pcp4s <-chan NAT, pcp6s <-chan pcpPortMapper, fallbacks <-chan NAT) (NAT, error) {
	var pcp4NAT, fallback NAT
	var pcp6 pcpPortMapper

	for {
		if pcp4s == nil && pcp6s == nil && (pcp4NAT != nil || fallback != nil || fallbacks == nil) {
			break
		}

		select {
		case nat, ok := <-pcp4s:
			pcp4s = nil
			if ok {
				pcp4NAT = nat
			}
		case client, ok := <-pcp6s:
			pcp6s = nil
			if ok {
				pcp6 = client
			}
		case nat, ok := <-fallbacks:
			if !ok {
				fallbacks = nil
			} else if fallback == nil {
				fallback = nat
			}
		case <-ctx.Done():
			// Collect results already published (sources use buffered channels),
			// then stop waiting on anything still in flight.
			for pcp4s != nil || pcp6s != nil || fallbacks != nil {
				select {
				case nat, ok := <-pcp4s:
					pcp4s = nil
					if ok {
						pcp4NAT = nat
					}
				case client, ok := <-pcp6s:
					pcp6s = nil
					if ok {
						pcp6 = client
					}
				case nat, ok := <-fallbacks:
					if !ok {
						fallbacks = nil
					} else if fallback == nil {
						fallback = nat
					}
				default:
					// Empty all channels to stop waiting on them.
					pcp4s, pcp6s, fallbacks = nil, nil, nil
				}
			}
		}
	}

	gateway := pcp4NAT
	if gateway == nil {
		gateway = fallback
	}
	if gateway == nil {
		return nil, ErrNoNATFound
	}
	if pcp6 != nil {
		gateway = newNATWithPCPIPv6(gateway, pcp6)
	}
	return gateway, nil
}

var (
	random   = rand.New(rand.NewSource(time.Now().UnixNano()))
	randomMu sync.Mutex
)

func randomPort() int {
	randomMu.Lock()
	defer randomMu.Unlock()
	return random.Intn(math.MaxUint16-10000) + 10000
}
