package nat

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/libp2p/go-nat/pcp"
)

type fakeNAT struct {
	typ        string
	port       int
	addErr     error
	addStarted chan struct{}
	addRelease <-chan struct{}
	adds       int
	deletes    int
}

func (n *fakeNAT) Type() string                        { return n.typ }
func (n *fakeNAT) GetDeviceAddress() (net.IP, error)   { return nil, nil }
func (n *fakeNAT) GetExternalAddress() (net.IP, error) { return nil, nil }
func (n *fakeNAT) GetInternalAddress() (net.IP, error) { return nil, nil }
func (n *fakeNAT) AddPortMapping(context.Context, string, int, string, time.Duration) (int, error) {
	n.adds++
	if n.addStarted != nil {
		close(n.addStarted)
	}
	if n.addRelease != nil {
		<-n.addRelease
	}
	return n.port, n.addErr
}
func (n *fakeNAT) DeletePortMapping(context.Context, string, int) error {
	n.deletes++
	return n.addErr
}

type fakePCPMapper struct {
	addErr     error
	addStarted chan struct{}
	adds       int
	deletes    int
}

func (m *fakePCPMapper) AddPortMapping(context.Context, string, int, time.Duration) (*pcp.MapResponse, error) {
	m.adds++
	if m.addStarted != nil {
		close(m.addStarted)
	}
	return nil, m.addErr
}
func (m *fakePCPMapper) DeletePortMapping(context.Context, string, int) error {
	m.deletes++
	return m.addErr
}

func results[T any](values ...T) <-chan T {
	result := make(chan T, len(values))
	for _, value := range values {
		result <- value
	}
	close(result)
	return result
}

func TestMergeNATs(t *testing.T) {
	first := &fakeNAT{typ: "UPnP"}
	second := &fakeNAT{typ: "NAT-PMP"}
	got := make(map[NAT]bool)
	for nat := range mergeNATs(context.Background(), nil, results[NAT](first), results[NAT](second)) {
		got[nat] = true
	}
	if !got[first] || !got[second] || len(got) != 2 {
		t.Fatalf("merged NATs = %v, want both sources", got)
	}
}

func TestSelectGatewayPrefersPCPv4(t *testing.T) {
	pcp4 := &fakeNAT{typ: "PCP"}
	fallback := &fakeNAT{typ: "UPnP"}
	pcp4s := make(chan NAT)
	fallbacks := make(chan NAT)
	go func() {
		fallbacks <- fallback
		close(fallbacks)
		pcp4s <- pcp4
		close(pcp4s)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := selectGateway(ctx, pcp4s, results[pcpPortMapper](), fallbacks)
	if err != nil {
		t.Fatal(err)
	}
	if got != pcp4 {
		t.Fatalf("selected %s, want PCP", got.Type())
	}
}

func TestSelectGatewayAddsPCPv6ToFallback(t *testing.T) {
	v4Err := errors.New("IPv4 mapping failed")
	fallback := &fakeNAT{typ: "NAT-PMP", port: 4242, addErr: v4Err}
	pcp6 := &fakePCPMapper{addErr: errors.New("IPv6 mapping failed")}

	got, err := selectGateway(context.Background(), results[NAT](), results[pcpPortMapper](pcp6), results[NAT](fallback))
	if err != nil {
		t.Fatal(err)
	}

	port, err := got.AddPortMapping(context.Background(), "tcp", 1234, "test", time.Minute)
	if port != fallback.port || !errors.Is(err, v4Err) {
		t.Fatalf("AddPortMapping() = (%d, %v), want (%d, %v)", port, err, fallback.port, v4Err)
	}
	if fallback.adds != 1 || pcp6.adds != 1 {
		t.Fatalf("mapping calls = (IPv4: %d, IPv6: %d), want (1, 1)", fallback.adds, pcp6.adds)
	}

	_ = got.DeletePortMapping(context.Background(), "tcp", 1234)
	if fallback.deletes != 1 || pcp6.deletes != 1 {
		t.Fatalf("delete calls = (IPv4: %d, IPv6: %d), want (1, 1)", fallback.deletes, pcp6.deletes)
	}
}

func TestDualStackMappingStartsBothStacks(t *testing.T) {
	releaseIPv4 := make(chan struct{})
	startedIPv6 := make(chan struct{})
	ipv4 := &fakeNAT{addRelease: releaseIPv4}
	ipv6 := &fakePCPMapper{addStarted: startedIPv6}
	gateway := newNATWithPCPIPv6(ipv4, ipv6)

	done := make(chan struct{})
	go func() {
		_, _ = gateway.AddPortMapping(context.Background(), "tcp", 1234, "test", time.Minute)
		close(done)
	}()

	select {
	case <-startedIPv6:
	case <-time.After(time.Second):
		close(releaseIPv4)
		<-done
		t.Fatal("IPv6 mapping did not start while IPv4 mapping was blocked")
	}
	close(releaseIPv4)
	<-done
}

func TestSelectGatewayUsesFirstFallback(t *testing.T) {
	first := &fakeNAT{typ: "UPnP"}
	second := &fakeNAT{typ: "NAT-PMP"}

	got, err := selectGateway(context.Background(), results[NAT](), results[pcpPortMapper](), results[NAT](first, second))
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("selected %s, want first fallback", got.Type())
	}

	_, err = selectGateway(context.Background(), results[NAT](), results[pcpPortMapper](), results[NAT]())
	if !errors.Is(err, ErrNoNATFound) {
		t.Fatalf("empty discovery error = %v, want %v", err, ErrNoNATFound)
	}
}
