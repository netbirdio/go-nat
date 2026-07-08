package nat

import (
	"net"
	"runtime"

	"github.com/libp2p/go-netroute"
)

func getDefaultGateway() (net.IP, error) {
	router, err := netroute.New()
	if err != nil {
		return nil, err
	}

	dst := net.IPv4zero
	if runtime.GOOS == "linux" || runtime.GOOS == "android" {
		// go-netroute v0.4.0 rejects unspecified destinations client-side on Linux/Android.
		dst = net.IPv4(0, 0, 0, 1)
	}

	_, ip, _, err := router.Route(dst)
	if err != nil {
		return nil, err
	}
	if ip == nil {
		return nil, ErrNoNATFound
	}
	return ip, nil
}
