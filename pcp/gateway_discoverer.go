package pcp

import (
	"context"
	"net"
	"runtime"

	"github.com/libp2p/go-netroute"
)

type gatewayDiscoverer interface {
	Discover(ctx context.Context) (gateway net.IP, localIP net.IP, err error)
	DiscoverV6(ctx context.Context) (gateway net.IP, localIP net.IP, zone string, err error)
}

type defaultGatewayDiscoverer struct{}

var _ gatewayDiscoverer = (*defaultGatewayDiscoverer)(nil)

var newRouter = netroute.New

// Discover returns the default IPv4 gateway and local IP using the system routing table.
func (d *defaultGatewayDiscoverer) Discover(ctx context.Context) (gateway net.IP, localIP net.IP, err error) {
	router, err := newRouter()
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

// DiscoverV6 returns the default IPv6 gateway, local IP, and scope zone.
func (d *defaultGatewayDiscoverer) DiscoverV6(ctx context.Context) (gateway net.IP, localIP net.IP, zone string, err error) {
	router, err := newRouter()
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
