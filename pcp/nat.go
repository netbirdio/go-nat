package pcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"sync"
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
	client *Client

	mu sync.RWMutex
	// client6 is the IPv6 PCP client, nil if IPv6 is unavailable.
	client6 *Client
	// localIP6 caches the local IPv6 address used for PCP requests.
	localIP6 netip.Addr
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
	resp, err := n.client.AddPortMapping(ctx, protocol, internalPort, timeout)
	if err != nil {
		return 0, fmt.Errorf("add mapping: %w", err)
	}

	n.mu.RLock()
	client6 := n.client6
	n.mu.RUnlock()

	if client6 == nil {
		return int(resp.ExternalPort), nil
	}

	// IPv4 NAT mapping already succeeded; IPv6 pinhole failures should not make
	// the overall mapping fail.
	_, _ = client6.AddPortMapping(ctx, protocol, internalPort, timeout)
	return int(resp.ExternalPort), nil
}

// DeletePortMapping removes a port mapping from both IPv4 and IPv6.
func (n *NAT) DeletePortMapping(ctx context.Context, protocol string, internalPort int) error {
	err := n.client.DeletePortMapping(ctx, protocol, internalPort)

	n.mu.RLock()
	client6 := n.client6
	n.mu.RUnlock()

	if client6 != nil {
		_ = client6.DeletePortMapping(ctx, protocol, internalPort)
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

// DiscoverPCP attempts to discover a PCP-capable gateway.
// It returns a NAT if PCP is supported, or an error otherwise. When available,
// it also discovers an IPv6 gateway for firewall pinholes.
func DiscoverPCP(ctx context.Context) (*NAT, error) {
	gateway, localIP, err := getDefaultGateway()
	if err != nil {
		return nil, fmt.Errorf("get default gateway: %w", err)
	}

	client := NewClient(gateway)
	client.SetLocalIP(localIP)
	if _, err := client.Announce(ctx); err != nil {
		return nil, fmt.Errorf("PCP announce: %w", err)
	}

	result := &NAT{client: client}
	discoverIPv6(ctx, result)

	return result, nil
}

// Discover is an alias for DiscoverPCP.
func Discover(ctx context.Context) (*NAT, error) {
	return DiscoverPCP(ctx)
}

func discoverIPv6(ctx context.Context, result *NAT) {
	gateway6, localIP6, err := getDefaultGateway6()
	if err != nil {
		return
	}

	client6 := NewClient(gateway6)
	client6.SetLocalIP(localIP6)
	if _, err := client6.Announce(ctx); err != nil {
		return
	}

	addr, ok := netip.AddrFromSlice(localIP6)
	if !ok {
		return
	}
	result.mu.Lock()
	result.client6 = client6
	result.localIP6 = addr.Unmap()
	result.mu.Unlock()
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

// getDefaultGateway6 returns the default IPv6 gateway IP address using the system routing table.
func getDefaultGateway6() (gateway net.IP, localIP net.IP, err error) {
	router, err := netroute.New()
	if err != nil {
		return nil, nil, err
	}

	dst := net.IPv6zero
	if runtime.GOOS == "linux" || runtime.GOOS == "android" {
		// ::2
		dst = net.IP{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2}
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
