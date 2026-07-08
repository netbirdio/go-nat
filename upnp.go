package nat

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/huin/goupnp"
	"github.com/huin/goupnp/dcps/internetgateway1"
	"github.com/huin/goupnp/dcps/internetgateway2"
	"github.com/huin/goupnp/httpu"
	"github.com/koron/go-ssdp"
)

const (
	ssdpPort      = "1900"
	searchRepeats = 3
)

// upnpUnicastSearchTimeout is how long each unicast SSDP search waits for
// responses. It is a variable so tests can shorten it.
var upnpUnicastSearchTimeout = 2 * time.Second

// upnpUnicastSearchTargets are tried in order until one yields a usable gateway.
var upnpUnicastSearchTargets = []string{
	"urn:schemas-upnp-org:device:InternetGatewayDevice:2",
	"urn:schemas-upnp-org:device:InternetGatewayDevice:1",
	"ssdp:all",
}

// upnpServiceRank orders WAN connection services by preference, matching the
// existing multicast discovery preference.
var upnpServiceRank = map[string]int{
	internetgateway2.URN_WANIPConnection_2:  3,
	internetgateway2.URN_WANIPConnection_1:  2,
	internetgateway2.URN_WANPPPConnection_1: 1,
}

var _ NAT = (*upnp_NAT)(nil)

func discoverUPNP_IG1(ctx context.Context) <-chan NAT {
	res := make(chan NAT)
	go func() {
		defer close(res)

		// find devices
		devs, err := goupnp.DiscoverDevicesCtx(ctx, internetgateway1.URN_WANConnectionDevice_1)
		if err != nil {
			return
		}

		for _, dev := range devs {
			if dev.Root == nil {
				continue
			}

			dev.Root.Device.VisitServices(func(srv *goupnp.Service) {
				if ctx.Err() != nil {
					return
				}
				switch srv.ServiceType {
				case internetgateway1.URN_WANIPConnection_1:
					client := &internetgateway1.WANIPConnection1{ServiceClient: goupnp.ServiceClient{
						SOAPClient: srv.NewSOAPClient(),
						RootDevice: dev.Root,
						Location:   dev.Location,
						Service:    srv,
					}}
					_, isNat, err := client.GetNATRSIPStatusCtx(ctx)
					if err == nil && isNat {
						select {
						case res <- newUPNPNAT(client, "UPNP (IG1-IP1)", dev.Root):
						case <-ctx.Done():
						}
					}

				case internetgateway1.URN_WANPPPConnection_1:
					client := &internetgateway1.WANPPPConnection1{ServiceClient: goupnp.ServiceClient{
						SOAPClient: srv.NewSOAPClient(),
						RootDevice: dev.Root,
						Location:   dev.Location,
						Service:    srv,
					}}
					_, isNat, err := client.GetNATRSIPStatusCtx(ctx)
					if err == nil && isNat {
						select {
						case res <- newUPNPNAT(client, "UPNP (IG1-PPP1)", dev.Root):
						case <-ctx.Done():
						}
					}

				}
			})
		}

	}()
	return res
}

func discoverUPNP_IG2(ctx context.Context) <-chan NAT {
	res := make(chan NAT)
	go func() {
		defer close(res)

		// find devices
		devs, err := goupnp.DiscoverDevicesCtx(ctx, internetgateway2.URN_WANConnectionDevice_2)
		if err != nil {
			return
		}

		for _, dev := range devs {
			if dev.Root == nil {
				continue
			}

			dev.Root.Device.VisitServices(func(srv *goupnp.Service) {
				if ctx.Err() != nil {
					return
				}
				switch srv.ServiceType {
				case internetgateway2.URN_WANIPConnection_1:
					client := &internetgateway2.WANIPConnection1{ServiceClient: goupnp.ServiceClient{
						SOAPClient: srv.NewSOAPClient(),
						RootDevice: dev.Root,
						Location:   dev.Location,
						Service:    srv,
					}}
					_, isNat, err := client.GetNATRSIPStatusCtx(ctx)
					if err == nil && isNat {
						select {
						case res <- newUPNPNAT(client, "UPNP (IG2-IP1)", dev.Root):
						case <-ctx.Done():
						}
					}

				case internetgateway2.URN_WANIPConnection_2:
					client := &internetgateway2.WANIPConnection2{ServiceClient: goupnp.ServiceClient{
						SOAPClient: srv.NewSOAPClient(),
						RootDevice: dev.Root,
						Location:   dev.Location,
						Service:    srv,
					}}
					_, isNat, err := client.GetNATRSIPStatusCtx(ctx)
					if err == nil && isNat {
						select {
						case res <- newUPNPNAT(client, "UPNP (IG2-IP2)", dev.Root):
						case <-ctx.Done():
						}
					}

				case internetgateway2.URN_WANPPPConnection_1:
					client := &internetgateway2.WANPPPConnection1{ServiceClient: goupnp.ServiceClient{
						SOAPClient: srv.NewSOAPClient(),
						RootDevice: dev.Root,
						Location:   dev.Location,
						Service:    srv,
					}}
					_, isNat, err := client.GetNATRSIPStatusCtx(ctx)
					if err == nil && isNat {
						select {
						case res <- newUPNPNAT(client, "UPNP (IG2-PPP1)", dev.Root):
						case <-ctx.Done():
						}
					}

				}
			})
		}

	}()
	return res
}

func discoverUPNP_Unicast(ctx context.Context) <-chan NAT {
	gateway, err := getDefaultGateway()
	if err != nil {
		return nil
	}
	return discoverUPNPUnicastWithAddr(ctx, net.JoinHostPort(gateway.String(), ssdpPort))
}

func discoverUPNPUnicastWithAddr(ctx context.Context, gatewayAddr string) <-chan NAT {
	res := make(chan NAT, 1)
	go func() {
		defer close(res)

		gateway, err := discoverUPNPUnicast(ctx, gatewayAddr)
		if err != nil {
			return
		}

		select {
		case res <- gateway:
		case <-ctx.Done():
		}
	}()
	return res
}

// discoverUPNPUnicast attempts to find a UPnP IGD by sending unicast SSDP
// searches to gatewayAddr. This is useful on networks where multicast SSDP is
// filtered but gateways still answer direct M-SEARCH requests.
func discoverUPNPUnicast(ctx context.Context, gatewayAddr string) (NAT, error) {
	client, err := httpu.NewHTTPUClient()
	if err != nil {
		return nil, fmt.Errorf("create SSDP client: %w", err)
	}
	defer func() { _ = client.Close() }()

	checked := make(map[string]bool)
	for _, target := range upnpUnicastSearchTargets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		locations, err := searchUPNPUnicast(client, gatewayAddr, target)
		if err != nil {
			continue
		}

		for _, location := range locations {
			if checked[location] {
				continue
			}
			checked[location] = true

			gateway, err := natFromUPNPLocation(ctx, location)
			if err == nil {
				return gateway, nil
			}
		}
	}

	return nil, fmt.Errorf("no UPnP gateway found at %s", gatewayAddr)
}

// searchUPNPUnicast sends a unicast M-SEARCH to gatewayAddr and returns the
// description URLs of devices that answered.
func searchUPNPUnicast(client *httpu.HTTPUClient, gatewayAddr, searchTarget string) ([]string, error) {
	mx := max(int(upnpUnicastSearchTimeout/time.Second), 1)
	req := &http.Request{
		Method: "M-SEARCH",
		// httpu sends the request to req.Host, making the search unicast.
		Host: gatewayAddr,
		URL:  &url.URL{Opaque: "*"},
		// Header keys are set verbatim (map literal keys bypass Go's
		// canonicalization) to keep the upper-case field names SSDP
		// implementations expect.
		Header: http.Header{
			"HOST": []string{gatewayAddr},
			"MAN":  []string{`"ssdp:discover"`},
			"MX":   []string{strconv.Itoa(mx)},
			"ST":   []string{searchTarget},
		},
	}

	responses, err := client.Do(req, upnpUnicastSearchTimeout, searchRepeats)
	if err != nil {
		return nil, err
	}

	var locations []string
	for _, resp := range responses {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		if resp.StatusCode != http.StatusOK {
			continue
		}
		if location := resp.Header.Get("Location"); location != "" {
			locations = append(locations, location)
		}
	}
	return locations, nil
}

func discoverUPNP_GenIGDev(ctx context.Context) <-chan NAT {
	res := make(chan NAT, 1)
	go func() {
		defer close(res)

		deviceList, err := ssdp.Search(ssdp.All, 5, "")
		if err != nil {
			return
		}

		for _, service := range deviceList {
			if !strings.Contains(service.Type, "InternetGatewayDevice") {
				continue
			}

			gateway, err := natFromUPNPLocation(ctx, service.Location)
			if err != nil {
				continue
			}

			select {
			case res <- gateway:
			case <-ctx.Done():
			}
			return
		}
	}()
	return res
}

// natFromUPNPLocation fetches the device description and returns a NAT backed
// by the most preferred WAN connection service it offers.
func natFromUPNPLocation(ctx context.Context, location string) (NAT, error) {
	loc, err := url.Parse(location)
	if err != nil {
		return nil, fmt.Errorf("parse location: %w", err)
	}

	root, err := goupnp.DeviceByURLCtx(ctx, loc)
	if err != nil {
		return nil, fmt.Errorf("fetch device description: %w", err)
	}

	var services []*goupnp.Service
	root.Device.VisitServices(func(srv *goupnp.Service) {
		if upnpServiceRank[srv.ServiceType] > 0 {
			services = append(services, srv)
		}
	})
	sort.SliceStable(services, func(i, j int) bool {
		return upnpServiceRank[services[i].ServiceType] > upnpServiceRank[services[j].ServiceType]
	})

	for _, srv := range services {
		gateway, err := natFromUPNPService(ctx, root, loc, srv)
		if err == nil {
			return gateway, nil
		}
	}

	return nil, fmt.Errorf("no usable WAN connection service in device at %s", location)
}

func natFromUPNPService(ctx context.Context, root *goupnp.RootDevice, loc *url.URL, srv *goupnp.Service) (NAT, error) {
	serviceClient := goupnp.ServiceClient{
		SOAPClient: srv.NewSOAPClient(),
		RootDevice: root,
		Location:   loc,
		Service:    srv,
	}

	var client upnp_NAT_Client
	var natType string
	switch srv.ServiceType {
	case internetgateway2.URN_WANIPConnection_2:
		client = &internetgateway2.WANIPConnection2{ServiceClient: serviceClient}
		natType = "UPnP unicast (IP2)"
	case internetgateway2.URN_WANIPConnection_1:
		client = &internetgateway2.WANIPConnection1{ServiceClient: serviceClient}
		natType = "UPnP unicast (IP1)"
	case internetgateway2.URN_WANPPPConnection_1:
		client = &internetgateway2.WANPPPConnection1{ServiceClient: serviceClient}
		natType = "UPnP unicast (PPP1)"
	default:
		return nil, fmt.Errorf("unsupported service type %s", srv.ServiceType)
	}

	_, isNAT, err := client.GetNATRSIPStatusCtx(ctx)
	if err != nil {
		return nil, fmt.Errorf("get NAT status: %w", err)
	}
	if !isNAT {
		return nil, errors.New("gateway reports NAT disabled")
	}

	return newUPNPNAT(client, natType, root), nil
}

type upnp_NAT_Client interface {
	GetExternalIPAddressCtx(context.Context) (string, error)
	AddPortMappingCtx(context.Context, string, uint16, string, uint16, string, bool, string, uint32) error
	DeletePortMappingCtx(context.Context, string, uint16, string) error
	GetNATRSIPStatusCtx(context.Context) (bool, bool, error)
}

type upnp_NAT struct {
	c          upnp_NAT_Client
	typ        string
	rootDevice *goupnp.RootDevice

	mu    sync.Mutex
	ports map[int]int
}

func newUPNPNAT(client upnp_NAT_Client, typ string, root *goupnp.RootDevice) *upnp_NAT {
	return &upnp_NAT{
		c:          client,
		typ:        typ,
		rootDevice: root,
		ports:      make(map[int]int),
	}
}

func (u *upnp_NAT) GetExternalAddress() (addr net.IP, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ipString, err := u.c.GetExternalIPAddressCtx(ctx)
	if err != nil {
		return nil, err
	}

	ip := net.ParseIP(ipString)
	if ip == nil {
		return nil, ErrNoExternalAddress
	}

	return ip, nil
}

func mapProtocol(s string) (string, error) {
	switch s {
	case "udp":
		return "UDP", nil
	case "tcp":
		return "TCP", nil
	default:
		return "", fmt.Errorf("invalid protocol: %s", s)
	}
}

func (u *upnp_NAT) AddPortMapping(ctx context.Context, protocol string, internalPort int, description string, timeout time.Duration) (int, error) {
	proto, err := mapProtocol(protocol)
	if err != nil {
		return 0, err
	}

	ip, err := u.GetInternalAddress()
	if err != nil {
		return 0, fmt.Errorf("get internal address: %w", err)
	}

	timeoutInSeconds := uint32(timeout / time.Second)

	u.mu.Lock()
	externalPort := u.ports[internalPort]
	u.mu.Unlock()

	if externalPort > 0 {
		err = u.c.AddPortMappingCtx(ctx, "", uint16(externalPort), proto, uint16(internalPort), ip.String(), true, description, timeoutInSeconds)
		if err == nil {
			return externalPort, nil
		}
	}

	for range 3 {
		externalPort = randomPort()
		err = u.c.AddPortMappingCtx(ctx, "", uint16(externalPort), proto, uint16(internalPort), ip.String(), true, description, timeoutInSeconds)
		if err == nil {
			u.mu.Lock()
			u.ports[internalPort] = externalPort
			u.mu.Unlock()
			return externalPort, nil
		}
	}

	return 0, err
}

func (u *upnp_NAT) DeletePortMapping(ctx context.Context, protocol string, internalPort int) error {
	proto, err := mapProtocol(protocol)
	if err != nil {
		return err
	}

	u.mu.Lock()
	externalPort := u.ports[internalPort]
	delete(u.ports, internalPort)
	u.mu.Unlock()

	if externalPort == 0 {
		return nil
	}

	return u.c.DeletePortMappingCtx(ctx, "", uint16(externalPort), proto)
}

func (u *upnp_NAT) GetDeviceAddress() (net.IP, error) {
	addr, err := net.ResolveUDPAddr("udp4", u.rootDevice.URLBase.Host)
	if err != nil {
		return nil, err
	}

	return addr.IP, nil
}

func (u *upnp_NAT) GetInternalAddress() (net.IP, error) {
	conn, err := net.Dial("udp4", u.rootDevice.URLBase.Host)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	localAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return nil, ErrNoInternalAddress
	}
	return localAddr.IP, nil
}

func (n *upnp_NAT) Type() string { return n.typ }
