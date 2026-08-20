package nat

import (
	"errors"
	"net/netip"
	"net/url"
	"testing"

	"github.com/huin/goupnp"
)

func TestLocationHasAddr(t *testing.T) {
	gateway := netip.MustParseAddr("192.168.1.1")

	tests := []struct {
		name     string
		location string
		want     bool
	}{
		{
			name:     "the gateway itself",
			location: "http://192.168.1.1:5000/rootDesc.xml",
			want:     true,
		},
		{
			name:     "the gateway without a port",
			location: "http://192.168.1.1/rootDesc.xml",
			want:     true,
		},
		{
			name: "another host on the link answered",
			// A rogue responder steering discovery at a device it controls.
			location: "http://192.168.1.99:5000/rootDesc.xml",
		},
		{
			name:     "an off-link address",
			location: "http://203.0.113.7/rootDesc.xml",
		},
		{
			name: "a host name cannot be checked without resolving it",
			// A responder free to choose the name is also choosing what the
			// resolver returns, so the name proves nothing.
			location: "http://gateway.local:5000/rootDesc.xml",
		},
		{
			name:     "unparseable",
			location: "://nonsense",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := locationHasAddr(tt.location, gateway); got != tt.want {
				t.Fatalf("locationHasAddr(%q) = %v, want %v", tt.location, got, tt.want)
			}
		})
	}
}

func TestLocationHasAddrMatchesMappedGateway(t *testing.T) {
	// A gateway parsed from a net.IP may arrive as a v4-mapped v6 address.
	mapped := netip.MustParseAddr("::ffff:192.168.1.1")
	if !locationHasAddr("http://192.168.1.1:5000/rootDesc.xml", mapped) {
		t.Fatal("a v4-mapped gateway did not match its own address")
	}
}

func TestDeviceHostPort(t *testing.T) {
	tests := []struct {
		name    string
		urlBase string
		want    string
		wantErr error
	}{
		{
			name:    "explicit port",
			urlBase: "http://192.168.1.1:5000/",
			want:    "192.168.1.1:5000",
		},
		{
			name: "no port defaults to HTTP",
			// The UPnP Device Architecture lets a device URL omit the port.
			urlBase: "http://192.168.1.1/",
			want:    "192.168.1.1:80",
		},
		{
			name:    "IPv6 literal keeps its brackets",
			urlBase: "http://[fe80::1]:5000/",
			want:    "[fe80::1]:5000",
		},
		{
			name:    "IPv6 literal without a port",
			urlBase: "http://[fe80::1]/",
			want:    "[fe80::1]:80",
		},
		{
			name:    "hostname",
			urlBase: "http://gateway.local:49152/",
			want:    "gateway.local:49152",
		},
		{
			name:    "no base URL at all",
			urlBase: "",
			wantErr: errNoDeviceHost,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base, err := url.Parse(tt.urlBase)
			if err != nil {
				t.Fatal(err)
			}

			got, err := deviceHostPort(&goupnp.RootDevice{URLBase: *base})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("deviceHostPort() error = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("deviceHostPort() = %q, want %q", got, tt.want)
			}
		})
	}
}
