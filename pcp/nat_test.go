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

// A v6 pinhole opened alongside a failed v4 mapping must be rolled back: the
// caller treats the whole mapping as failed and never deletes it.
func TestAddPortMappingRollsBackIPv6OnIPv4Failure(t *testing.T) {
	server4 := newFakePCPServer(t, "udp4", "127.0.0.1:5351")
	server6 := newFakePCPServer(t, "udp6", "[::1]:5351")

	// Seed the v4 mapping with a foreign nonce so the v4 MAP fails fast with
	// NOT_AUTHORIZED while the v6 pinhole succeeds.
	server4.mu.Lock()
	server4.mappings[mappingKey{proto: ProtoTCP, port: 1234}] = [12]byte{1}
	server4.mu.Unlock()

	client := NewClient(net.ParseIP("127.0.0.1"))
	client.SetLocalIP(net.ParseIP("127.0.0.1"))
	client6 := NewClient(net.ParseIP("::1"))
	client6.SetLocalIP(net.ParseIP("::1"))
	nat := &NAT{client: client, client6: client6}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := nat.AddPortMapping(ctx, "tcp", 1234, "", time.Minute); err == nil {
		t.Fatal("AddPortMapping() error = nil, want v4 NOT_AUTHORIZED")
	}
	if server6.hasMapping(ProtoTCP, 1234) {
		t.Fatal("v6 pinhole still open after failed v4 mapping")
	}
}

// A failing v6 delete must surface its error, not be silently dropped: the
// pinhole stays open and the caller needs to see why.
func TestDeletePortMappingSurfacesIPv6Error(t *testing.T) {
	newFakePCPServer(t, "udp4", "127.0.0.1:5351")
	server6 := newFakePCPServer(t, "udp6", "[::1]:5351")

	// A pinhole whose nonce the client does not hold: the delete's fresh
	// nonce is answered with NOT_AUTHORIZED.
	server6.mu.Lock()
	server6.mappings[mappingKey{proto: ProtoTCP, port: 4321}] = [12]byte{1}
	server6.mu.Unlock()

	client := NewClient(net.ParseIP("127.0.0.1"))
	client.SetLocalIP(net.ParseIP("127.0.0.1"))
	client6 := NewClient(net.ParseIP("::1"))
	client6.SetLocalIP(net.ParseIP("::1"))
	nat := &NAT{client: client, client6: client6}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := nat.DeletePortMapping(ctx, "tcp", 4321); err == nil {
		t.Fatal("DeletePortMapping() error = nil, want surfaced v6 NOT_AUTHORIZED")
	}
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
