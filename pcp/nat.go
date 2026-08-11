package pcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

var (
	// ErrNoNATFound is returned when no PCP-capable gateway can be found.
	ErrNoNATFound = errors.New("no NAT found")
	// ErrNoInternalAddress is returned when the local address for the gateway is unavailable.
	ErrNoInternalAddress = errors.New("no internal address")
)

type natInterface interface {
	Type() string
	GetDeviceAddress() (net.IP, error)
	GetExternalAddress() (net.IP, error)
	GetInternalAddress() (net.IP, error)
	AddPortMapping(context.Context, string, int, string, time.Duration) (int, error)
	DeletePortMapping(context.Context, string, int) error
}

var _ natInterface = (*NAT)(nil)

// NAT implements the go-nat NAT interface using PCP.
// It supports dual-stack (IPv4 and IPv6) when available.
// All methods are safe for concurrent use.
//
// TODO: IPv6 pinholes use the local IPv6 address. If the address changes
// (e.g. due to SLAAC rotation or network change), the pinhole becomes stale
// and needs to be recreated with the new address.
type NAT struct {
	client  *Client
	client6 *Client
}

// NewNAT creates a new NAT instance backed by PCP.
func NewNAT(gateway, localIP net.IP) *NAT {
	client := NewClient(gateway)
	client.SetLocalIP(localIP)
	return &NAT{
		client: client,
	}
}

// Type returns "PCP" as the NAT type.
func (n *NAT) Type() string {
	return "PCP"
}

// primary returns the IPv4 client when present, falling back to the IPv6
// client for v6-only discovery.
func (n *NAT) primary() *Client {
	if n.client != nil {
		return n.client
	}
	return n.client6
}

// GetDeviceAddress returns the gateway IP address.
func (n *NAT) GetDeviceAddress() (net.IP, error) {
	return n.primary().Gateway(), nil
}

// GetExternalAddress returns the external IP address.
func (n *NAT) GetExternalAddress() (net.IP, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return n.primary().GetExternalAddress(ctx)
}

// GetInternalAddress returns the local IP address used to communicate with the gateway.
func (n *NAT) GetInternalAddress() (net.IP, error) {
	addr, err := n.primary().getLocalIP()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoInternalAddress, err)
	}
	return append(net.IP(nil), addr.AsSlice()...), nil
}

// AddPortMapping creates a port mapping on both IPv4 and IPv6 (if available).
func (n *NAT) AddPortMapping(ctx context.Context, protocol string, internalPort int, _ string, timeout time.Duration) (int, error) {
	if n.client == nil {
		resp, err := n.client6.AddPortMapping(ctx, protocol, internalPort, timeout)
		if err != nil {
			return 0, fmt.Errorf("add mapping: %w", err)
		}
		return int(resp.ExternalPort), nil
	}

	client6 := n.client6

	var client6Done chan struct{}
	var err6 error
	if client6 != nil {
		client6Done = make(chan struct{})
		go func() {
			_, err6 = client6.AddPortMapping(ctx, protocol, internalPort, timeout)
			close(client6Done)
		}()
	}

	resp, err := n.client.AddPortMapping(ctx, protocol, internalPort, timeout)
	if client6Done != nil {
		<-client6Done
	}
	if err != nil {
		if err6 == nil && client6 != nil {
			rollbackPinhole(ctx, client6, protocol, internalPort)
		}
		return 0, fmt.Errorf("add mapping: %w", err)
	}
	return int(resp.ExternalPort), nil
}

// rollbackPinhole closes a v6 pinhole opened alongside a failed v4 mapping:
// the caller sees a failed mapping and will never delete it, so the pinhole
// would leak until its lifetime expires. Detached from ctx because the v4
// failure may be the caller's deadline expiring.
func rollbackPinhole(ctx context.Context, client6 *Client, protocol string, internalPort int) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultTimeout)
	defer cancel()
	_ = client6.DeletePortMapping(ctx, protocol, internalPort)
}

// DeletePortMapping removes a port mapping from both IPv4 and IPv6.
func (n *NAT) DeletePortMapping(ctx context.Context, protocol string, internalPort int) error {
	if n.client == nil {
		if err := n.client6.DeletePortMapping(ctx, protocol, internalPort); err != nil {
			return fmt.Errorf("delete mapping: %w", err)
		}
		return nil
	}

	var client6Done chan struct{}
	var err6 error
	if n.client6 != nil {
		client6Done = make(chan struct{})
		go func() {
			err6 = n.client6.DeletePortMapping(ctx, protocol, internalPort)
			close(client6Done)
		}()
	}

	err := n.client.DeletePortMapping(ctx, protocol, internalPort)
	if client6Done != nil {
		<-client6Done
	}
	if err6 != nil {
		err6 = fmt.Errorf("ipv6: %w", err6)
	}
	if err := errors.Join(err, err6); err != nil {
		return fmt.Errorf("delete mapping: %w", err)
	}
	return nil
}

// CheckServerHealth sends an ANNOUNCE to verify the server is still responsive.
// It returns the current epoch and whether the server may have restarted.
func (n *NAT) CheckServerHealth(ctx context.Context) (epoch uint32, serverRestarted bool, err error) {
	client := n.primary()
	epoch, err = client.Announce(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("announce: %w", err)
	}
	return epoch, client.EpochStateLost(), nil
}

// DiscoverPCP attempts to discover PCP-capable IPv4 and IPv6 gateways
// independently. It fails only when neither is found, so a v6-only network
// still yields a NAT that can open firewall pinholes.
func DiscoverPCP(ctx context.Context) (*NAT, error) {
	return discoverPCP(ctx, &defaultGatewayDiscoverer{})
}

func discoverPCP(ctx context.Context, gds gatewayDiscoverer) (*NAT, error) {
	type v6Result struct {
		client *Client
		err    error
	}
	v6Ch := make(chan v6Result, 1)
	go func() {
		client, err := discoverPCPIPv6(ctx, gds)
		v6Ch <- v6Result{client, err}
	}()

	result, v4Err := discoverPCPIPv4(ctx, gds)
	v6 := <-v6Ch

	if v4Err != nil && v6.err != nil {
		return nil, errors.Join(v4Err, v6.err)
	}
	if result == nil {
		result = &NAT{}
	}
	if v6.err == nil {
		result.client6 = v6.client
	}
	return result, nil
}

// DiscoverPCPIPv4 discovers a PCP-capable IPv4 gateway.
func DiscoverPCPIPv4(ctx context.Context) (*NAT, error) {
	return discoverPCPIPv4(ctx, &defaultGatewayDiscoverer{})
}

func discoverPCPIPv4(ctx context.Context, gds gatewayDiscoverer) (*NAT, error) {
	gateway, localIP, err := gds.Discover(ctx)
	if err != nil {
		return nil, fmt.Errorf("get default gateway: %w", err)
	}

	result := NewNAT(gateway, localIP)
	if _, err := result.client.Announce(ctx); err != nil {
		return nil, fmt.Errorf("PCP announce: %w", err)
	}
	return result, nil
}

// DiscoverPCPIPv6 discovers a PCP-capable IPv6 gateway for firewall pinholes.
func DiscoverPCPIPv6(ctx context.Context) (*Client, error) {
	return discoverPCPIPv6(ctx, &defaultGatewayDiscoverer{})
}

func discoverPCPIPv6(ctx context.Context, gds gatewayDiscoverer) (*Client, error) {
	gateway, localIP, zone, err := gds.DiscoverV6(ctx)
	if err != nil {
		return nil, fmt.Errorf("get default IPv6 gateway: %w", err)
	}

	client := NewClient(gateway)
	if zone != "" {
		client.gateway = client.gateway.WithZone(zone)
	}
	client.SetLocalIP(localIP)
	if _, err := client.Announce(ctx); err != nil {
		return nil, fmt.Errorf("PCP IPv6 announce: %w", err)
	}
	return client, nil
}

// Discover is an alias for DiscoverPCP.
func Discover(ctx context.Context) (*NAT, error) {
	return discoverPCP(ctx, &defaultGatewayDiscoverer{})
}
