package pcp

import (
	"bytes"
	"encoding/hex"
	"net/netip"
	"testing"
)

var testNonce = [12]byte{0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}

func TestBuildMapRequestWireFormat(t *testing.T) {
	tests := []struct {
		name             string
		clientIP         string
		protocol         uint8
		internalPort     uint16
		suggestedExtPort uint16
		suggestedExtIP   string // empty = no preference
		lifetime         uint32
		want             []byte
	}{
		{
			name:             "IPv4 mapping with suggested external",
			clientIP:         "192.168.1.10",
			protocol:         ProtoUDP,
			internalPort:     8080,
			suggestedExtPort: 9090,
			suggestedExtIP:   "203.0.113.9",
			lifetime:         7200,
			want: []byte{
				// Version, opcode MAP, reserved.
				0x02, 0x01, 0x00, 0x00,
				// Requested lifetime: 7200s.
				0x00, 0x00, 0x1c, 0x20,
				// Client IP: ::ffff:192.168.1.10.
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0xff, 0xff, 0xc0, 0xa8, 0x01, 0x0a,
				// Mapping nonce.
				0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
				// Protocol UDP, reserved.
				0x11, 0x00, 0x00, 0x00,
				// Internal port 8080, suggested external port 9090.
				0x1f, 0x90, 0x23, 0x82,
				// Suggested external IP: ::ffff:203.0.113.9.
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0xff, 0xff, 0xcb, 0x00, 0x71, 0x09,
			},
		},
		{
			name:         "IPv4 delete with no external preference",
			clientIP:     "192.168.1.10",
			protocol:     ProtoUDP,
			internalPort: 8080,
			lifetime:     0,
			want: []byte{
				// Version, opcode MAP, reserved.
				0x02, 0x01, 0x00, 0x00,
				// Requested lifetime: 0 (delete).
				0x00, 0x00, 0x00, 0x00,
				// Client IP: ::ffff:192.168.1.10.
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0xff, 0xff, 0xc0, 0xa8, 0x01, 0x0a,
				// Mapping nonce.
				0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
				// Protocol UDP, reserved.
				0x11, 0x00, 0x00, 0x00,
				// Internal port 8080, suggested external port 0.
				0x1f, 0x90, 0x00, 0x00,
				// Suggested external IP. RFC 6887 §11.1 + §5: no preference for an
				// IPv4 mapping is the all-zeros IPv4-mapped address ::ffff:0.0.0.0,
				// not the IPv6 unspecified address ::.
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0xff, 0xff, 0x00, 0x00, 0x00, 0x00,
			},
		},
		{
			name:             "IPv6 pinhole",
			clientIP:         "2001:db8::1",
			protocol:         ProtoTCP,
			internalPort:     4242,
			suggestedExtPort: 4242,
			lifetime:         300,
			want: []byte{
				// Version, opcode MAP, reserved.
				0x02, 0x01, 0x00, 0x00,
				// Requested lifetime: 300s.
				0x00, 0x00, 0x01, 0x2c,
				// Client IP: 2001:db8::1.
				0x20, 0x01, 0x0d, 0xb8, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
				// Mapping nonce.
				0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
				// Protocol TCP, reserved.
				0x06, 0x00, 0x00, 0x00,
				// Internal port 4242, suggested external port 4242.
				0x10, 0x92, 0x10, 0x92,
				// Suggested external IP: no preference for IPv6 is ::.
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var extIP netip.Addr
			if tt.suggestedExtIP != "" {
				extIP = netip.MustParseAddr(tt.suggestedExtIP)
			}
			got := buildMapRequest(netip.MustParseAddr(tt.clientIP), testNonce, tt.protocol,
				tt.internalPort, tt.suggestedExtPort, extIP, tt.lifetime)
			if !bytes.Equal(got, tt.want) {
				t.Errorf("buildMapRequest() =\n%swant:\n%s", hex.Dump(got), hex.Dump(tt.want))
			}
		})
	}
}

func TestParseMapResponseWireFormat(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want MapResponse
	}{
		{
			name: "IPv4 mapping response",
			data: []byte{
				// Version, MAP reply, reserved, result SUCCESS.
				0x02, 0x81, 0x00, 0x00,
				// Lifetime: 7200s.
				0x00, 0x00, 0x1c, 0x20,
				// Epoch: 1000.
				0x00, 0x00, 0x03, 0xe8,
				// Reserved.
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				// Mapping nonce.
				0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
				// Protocol UDP, reserved.
				0x11, 0x00, 0x00, 0x00,
				// Internal port 8080, assigned external port 9090.
				0x1f, 0x90, 0x23, 0x82,
				// Assigned external IP: ::ffff:203.0.113.9.
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0xff, 0xff, 0xcb, 0x00, 0x71, 0x09,
			},
			want: MapResponse{
				Response: Response{
					Version:    Version,
					Opcode:     OpMap | OpReply,
					ResultCode: ResultSuccess,
					Lifetime:   7200,
					Epoch:      1000,
				},
				Nonce:        testNonce,
				Protocol:     ProtoUDP,
				InternalPort: 8080,
				ExternalPort: 9090,
				ExternalIP:   netip.MustParseAddr("203.0.113.9"),
			},
		},
		{
			name: "IPv6 pinhole response",
			data: []byte{
				// Version, MAP reply, reserved, result SUCCESS.
				0x02, 0x81, 0x00, 0x00,
				// Lifetime: 300s.
				0x00, 0x00, 0x01, 0x2c,
				// Epoch: 1000.
				0x00, 0x00, 0x03, 0xe8,
				// Reserved.
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				// Mapping nonce.
				0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
				// Protocol TCP, reserved.
				0x06, 0x00, 0x00, 0x00,
				// Internal port 4242, assigned external port 4242.
				0x10, 0x92, 0x10, 0x92,
				// Assigned external IP: 2001:db8::1.
				0x20, 0x01, 0x0d, 0xb8, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
			},
			want: MapResponse{
				Response: Response{
					Version:    Version,
					Opcode:     OpMap | OpReply,
					ResultCode: ResultSuccess,
					Lifetime:   300,
					Epoch:      1000,
				},
				Nonce:        testNonce,
				Protocol:     ProtoTCP,
				InternalPort: 4242,
				ExternalPort: 4242,
				ExternalIP:   netip.MustParseAddr("2001:db8::1"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMapResponse(tt.data)
			if err != nil {
				t.Fatalf("parseMapResponse() error = %v", err)
			}
			if *got != tt.want {
				t.Errorf("parseMapResponse() = %+v, want %+v", *got, tt.want)
			}
		})
	}
}
