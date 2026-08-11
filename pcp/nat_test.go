package pcp

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

type fakeGatewayDiscoverer struct{}

func (f *fakeGatewayDiscoverer) Discover(ctx context.Context) (net.IP, net.IP, error) {
	return nil, nil, errors.New("no IPv4 route")
}
func (f *fakeGatewayDiscoverer) DiscoverV6(ctx context.Context) (net.IP, net.IP, string, error) {
	return net.ParseIP("::1"), net.ParseIP("::1"), "", nil
}

// A PCP-capable IPv6 gateway must be usable even when IPv4 discovery fails:
// v6-only networks still need firewall pinholes.
func TestDiscoverPCPIPv6WithoutIPv4(t *testing.T) {
	newFakePCPServer(t, "udp6", "[::1]:5351")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	nat, err := discoverPCP(ctx, &fakeGatewayDiscoverer{})
	if err != nil {
		t.Fatalf("DiscoverPCP() error = %v, want IPv6-only NAT when IPv4 discovery fails", err)
	}
	if nat.client6 == nil {
		t.Fatal("DiscoverPCP() NAT has no IPv6 client despite a working IPv6 gateway")
	}
}
