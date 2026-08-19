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
	addErr      error
	addStarted  chan struct{}
	announceErr error
	epochLost   bool
	adds        int
	deletes     int
	announces   int
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

func (m *fakePCPMapper) Announce(context.Context) (uint32, error) {
	m.announces++
	return 42, m.announceErr
}

func (m *fakePCPMapper) EpochStateLost() bool { return m.epochLost }

// fakeHealthCheckedNAT is a fallback NAT that also reports gateway health, the
// shape a PCPv4 gateway has once it is wrapped for IPv6 pinholes.
type fakeHealthCheckedNAT struct {
	fakeNAT
	epoch     uint32
	restarted bool
	healthErr error
}

func (n *fakeHealthCheckedNAT) CheckServerHealth(context.Context) (uint32, bool, error) {
	return n.epoch, n.restarted, n.healthErr
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

	// Results already published when the context is cancelled must still be
	// collected, with PCP preferred over the fallback.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := selectGateway(ctx, results[NAT](pcp4), results[pcpIPv6Client](), results[NAT](fallback))
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

	got, err := selectGateway(context.Background(), results[NAT](), results[pcpIPv6Client](pcp6), results[NAT](fallback))
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

func TestDualStackMappingRollsBackIPv6OnIPv4Failure(t *testing.T) {
	ipv4 := &fakeNAT{addErr: errors.New("IPv4 mapping failed")}
	ipv6 := &fakePCPMapper{}
	gateway := newNATWithPCPIPv6(ipv4, ipv6)

	if _, err := gateway.AddPortMapping(context.Background(), "tcp", 1234, "test", time.Minute); err == nil {
		t.Fatal("AddPortMapping() error = nil, want IPv4 failure")
	}
	if ipv6.deletes != 1 {
		t.Fatalf("IPv6 deletes = %d, want 1: the pinhole leaks when the IPv4 mapping fails", ipv6.deletes)
	}
}

func TestDualStackReportsPinholeOutcome(t *testing.T) {
	v6Err := errors.New("IPv6 pinhole failed")
	gateway := newNATWithPCPIPv6(&fakeNAT{port: 4242}, &fakePCPMapper{addErr: v6Err})

	reporter, ok := gateway.(IPv6PinholeReporter)
	if !ok {
		t.Fatal("dual-stack gateway does not implement IPv6PinholeReporter")
	}

	// A pinhole failure must not fail the call: the peer on the other end may
	// have no IPv6 at all, so the IPv4 mapping is what matters.
	port, err := gateway.AddPortMapping(context.Background(), "tcp", 1234, "test", time.Minute)
	if port != 4242 || err != nil {
		t.Fatalf("AddPortMapping() = (%d, %v), want (4242, nil)", port, err)
	}
	if got := reporter.IPv6PinholeError(); !errors.Is(got, v6Err) {
		t.Fatalf("IPv6PinholeError() after add = %v, want %v", got, v6Err)
	}

	if err := gateway.DeletePortMapping(context.Background(), "tcp", 1234); err != nil {
		t.Fatalf("DeletePortMapping() error = %v, want nil", err)
	}
	if got := reporter.IPv6PinholeError(); !errors.Is(got, v6Err) {
		t.Fatalf("IPv6PinholeError() after delete = %v, want %v", got, v6Err)
	}
}

func TestDualStackClearsPinholeErrorOnSuccess(t *testing.T) {
	gateway := newNATWithPCPIPv6(&fakeNAT{}, &fakePCPMapper{})

	if _, err := gateway.AddPortMapping(context.Background(), "tcp", 1234, "test", time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := gateway.(IPv6PinholeReporter).IPv6PinholeError(); got != nil {
		t.Fatalf("IPv6PinholeError() = %v, want nil", got)
	}
}

func TestDualStackTypeNamesBothStacks(t *testing.T) {
	gateway := newNATWithPCPIPv6(&fakeNAT{typ: "UPnP (IG1-IP1)"}, &fakePCPMapper{})
	if got, want := gateway.Type(), "UPnP (IG1-IP1)+PCPv6"; got != want {
		t.Fatalf("Type() = %q, want %q", got, want)
	}
}

func TestDualStackHealthCheck(t *testing.T) {
	tests := []struct {
		name          string
		ipv4          NAT
		ipv6          *fakePCPMapper
		wantRestarted bool
		wantErr       bool
	}{
		{
			name: "IPv6 restart alone forces a remap",
			ipv4: &fakeHealthCheckedNAT{epoch: 7},
			ipv6: &fakePCPMapper{epochLost: true},
			// The pinhole is gone even though the IPv4 gateway is healthy.
			wantRestarted: true,
		},
		{
			name:          "IPv4 restart alone forces a remap",
			ipv4:          &fakeHealthCheckedNAT{epoch: 7, restarted: true},
			ipv6:          &fakePCPMapper{},
			wantRestarted: true,
		},
		{
			name: "neither stack restarted",
			ipv4: &fakeHealthCheckedNAT{epoch: 7},
			ipv6: &fakePCPMapper{},
		},
		{
			name: "fallback IPv4 cannot report health, IPv6 still can",
			ipv4: &fakeNAT{typ: "UPnP"},
			ipv6: &fakePCPMapper{epochLost: true},
			// A UPnP gateway has no epoch, so the pinhole is the only signal.
			wantRestarted: true,
		},
		{
			name: "one unreachable stack is not a restart",
			ipv4: &fakeHealthCheckedNAT{healthErr: errors.New("unreachable")},
			ipv6: &fakePCPMapper{epochLost: true},
			// The IPv6 verdict stands; the unreachable IPv4 server says nothing.
			wantRestarted: true,
		},
		{
			name:    "both stacks unreachable",
			ipv4:    &fakeHealthCheckedNAT{healthErr: errors.New("unreachable")},
			ipv6:    &fakePCPMapper{announceErr: errors.New("unreachable")},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gateway, ok := newNATWithPCPIPv6(tt.ipv4, tt.ipv6).(HealthChecker)
			if !ok {
				t.Fatal("dual-stack gateway does not implement HealthChecker")
			}

			_, restarted, err := gateway.CheckServerHealth(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("CheckServerHealth() error = %v, wantErr %v", err, tt.wantErr)
			}
			if restarted != tt.wantRestarted {
				t.Fatalf("serverRestarted = %v, want %v", restarted, tt.wantRestarted)
			}
		})
	}
}

func TestSelectGatewayAbortsOnCancelledContext(t *testing.T) {
	// Sources that never publish and never close, like discovery probes stuck
	// on an unresponsive network.
	stuck4 := make(chan NAT)
	stuck6 := make(chan pcpIPv6Client)
	stuckFallbacks := make(chan NAT)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() {
		_, err := selectGateway(ctx, stuck4, stuck6, stuckFallbacks)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("selectGateway() error = nil, want non-nil on cancelled context")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("selectGateway did not return after context cancellation")
	}
}

func TestSelectGatewayUsesFirstFallback(t *testing.T) {
	first := &fakeNAT{typ: "UPnP"}
	second := &fakeNAT{typ: "NAT-PMP"}

	got, err := selectGateway(context.Background(), results[NAT](), results[pcpIPv6Client](), results[NAT](first, second))
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("selected %s, want first fallback", got.Type())
	}

	_, err = selectGateway(context.Background(), results[NAT](), results[pcpIPv6Client](), results[NAT]())
	if !errors.Is(err, ErrNoNATFound) {
		t.Fatalf("empty discovery error = %v, want %v", err, ErrNoNATFound)
	}
}
