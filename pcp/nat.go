package pcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"time"

	"github.com/libp2p/go-netroute"
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

// GetDeviceAddress returns the gateway IP address.
func (n *NAT) GetDeviceAddress() (net.IP, error) {
	return n.client.Gateway(), nil
}

// GetExternalAddress returns the external IP address.
func (n *NAT) GetExternalAddress() (net.IP, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return n.client.GetExternalAddress(ctx)
}

// GetInternalAddress returns the local IP address used to communicate with the gateway.
func (n *NAT) GetInternalAddress() (net.IP, error) {
	addr, err := n.client.getLocalIP()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoInternalAddress, err)
	}
	return append(net.IP(nil), addr.AsSlice()...), nil
}

// AddPortMapping creates a port mapping on both IPv4 and IPv6 (if available).
func (n *NAT) AddPortMapping(ctx context.Context, protocol string, internalPort int, _ string, timeout time.Duration) (int, error) {
	client6 := n.client6

	var client6Done chan struct{}
	if client6 != nil {
		client6Done = make(chan struct{})
		go func() {
			_, _ = client6.AddPortMapping(ctx, protocol, internalPort, timeout)
			close(client6Done)
		}()
	}

	resp, err := n.client.AddPortMapping(ctx, protocol, internalPort, timeout)
	if client6Done != nil {
		<-client6Done
	}
	if err != nil {
		return 0, fmt.Errorf("add mapping: %w", err)
	}
	return int(resp.ExternalPort), nil
}

// DeletePortMapping removes a port mapping from both IPv4 and IPv6.
func (n *NAT) DeletePortMapping(ctx context.Context, protocol string, internalPort int) error {
	var client6Done chan struct{}
	if n.client6 != nil {
		client6Done = make(chan struct{})
		go func() {
			_ = n.client6.DeletePortMapping(ctx, protocol, internalPort)
			close(client6Done)
		}()
	}

	err := n.client.DeletePortMapping(ctx, protocol, internalPort)
	if client6Done != nil {
		<-client6Done
	}
	if err != nil {
		return fmt.Errorf("delete mapping: %w", err)
	}
	return nil
}

// CheckServerHealth sends an ANNOUNCE to verify the server is still responsive.
// It returns the current epoch and whether the server may have restarted.
func (n *NAT) CheckServerHealth(ctx context.Context) (epoch uint32, serverRestarted bool, err error) {
	epoch, err = n.client.Announce(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("announce: %w", err)
	}
	return epoch, n.client.EpochStateLost(), nil
}

// DiscoverPCP attempts to discover a PCP-capable IPv4 gateway.
// When available, it also discovers an IPv6 gateway for firewall pinholes.
func DiscoverPCP(ctx context.Context) (*NAT, error) {
	result, err := DiscoverPCPIPv4(ctx)
	if err != nil {
		return nil, err
	}

	client6, err := DiscoverPCPIPv6(ctx)
	if err == nil {
		result.client6 = client6
	}
	return result, nil
}

// DiscoverPCPIPv4 discovers a PCP-capable IPv4 gateway.
func DiscoverPCPIPv4(ctx context.Context) (*NAT, error) {
	gateway, localIP, err := getDefaultGateway()
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
	gateway, localIP, zone, err := getDefaultGateway6()
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
	return DiscoverPCP(ctx)
}

// getDefaultGateway returns the default IPv4 gateway and local IP using the system routing table.
func getDefaultGateway() (gateway net.IP, localIP net.IP, err error) {
	router, err := netroute.New()
	if err != nil {
		return nil, nil, err
	}

	dst := net.IPv4zero
	if runtime.GOOS == "linux" || runtime.GOOS == "android" {
		// go-netroute v0.4.0 rejects unspecified destinations client-side on Linux/Android.
		// TODO: on android/ios, use platform APIs (ConnectivityManager.getLinkProperties /
		// NWPathMonitor) when netlink-based lookup is restricted or unavailable.
		dst = net.IPv4(0, 0, 0, 1)
	}
	_, gateway, localIP, err = router.Route(dst)
	if err != nil {
		return nil, nil, err
	}

	if gateway == nil {
		return nil, nil, ErrNoNATFound
	}
	if localIP == nil {
		return nil, nil, ErrNoInternalAddress
	}

	return gateway, localIP, nil
}

// getDefaultGateway6 returns the default IPv6 gateway, local IP, and scope zone.
func getDefaultGateway6() (gateway net.IP, localIP net.IP, zone string, err error) {
	router, err := netroute.New()
	if err != nil {
		return nil, nil, "", err
	}

	dst := net.IPv6zero
	if runtime.GOOS == "linux" || runtime.GOOS == "android" {
		// ::2
		dst = net.IP{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2}
	}
	iface, gateway, localIP, err := router.Route(dst)
	if err != nil {
		return nil, nil, "", err
	}

	if gateway == nil {
		return nil, nil, "", ErrNoNATFound
	}
	if localIP == nil {
		return nil, nil, "", ErrNoInternalAddress
	}
	if gateway.IsLinkLocalUnicast() && iface != nil {
		zone = iface.Name
	}

	return gateway, localIP, zone, nil
}
