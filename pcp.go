package nat

import (
	"context"

	"github.com/libp2p/go-nat/pcp"
)

func discoverPCP(ctx context.Context) <-chan NAT {
	results := make(chan NAT, 1)
	go func() {
		defer close(results)

		gateway, err := pcp.DiscoverPCP(ctx)
		if err == nil {
			results <- gateway
		}
	}()
	return results
}

func discoverPCPv4(ctx context.Context) <-chan NAT {
	results := make(chan NAT, 1)
	go func() {
		defer close(results)

		gateway, err := pcp.DiscoverPCPIPv4(ctx)
		if err == nil {
			results <- gateway
		}
	}()
	return results
}

func discoverPCPv6(ctx context.Context) <-chan pcpPortMapper {
	results := make(chan pcpPortMapper, 1)
	go func() {
		defer close(results)

		client, err := pcp.DiscoverPCPIPv6(ctx)
		if err == nil {
			results <- client
		}
	}()
	return results
}
