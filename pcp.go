package nat

import (
	"context"

	"github.com/libp2p/go-nat/pcp"
)

func discoverPCP(ctx context.Context) <-chan NAT {
	res := make(chan NAT, 1)
	go func() {
		defer close(res)

		gateway, err := pcp.DiscoverPCP(ctx)
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
