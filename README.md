# go-nat

[![GoDoc](https://godoc.org/github.com/netbirdio/go-nat?status.svg)](https://godoc.org/github.com/netbirdio/go-nat) [![status](https://sourcegraph.com/api/repos/github.com/netbirdio/go-nat/.badges/status.png)](https://sourcegraph.com/github.com/netbirdio/go-nat)

Forked from: [libp2p/go-nat](https://github.com/libp2p/go-nat), itself forked from
[fd/go-nat](https://github.com/fd/go-nat).

This fork adds PCP (RFC 6887) with IPv6 firewall pinholes and unicast SSDP discovery.
It carries its own module path so that it can be required directly, rather than through
a `replace` directive: a `replace` applies only to the main module, so anything importing
a consumer of this package as a library would otherwise resolve to upstream go-nat and
fail to build against the added API.

---

The last gx published version of this module was: 1.0.3: QmdwkZHamNNrj7k3G29rnurmW3mFzsDhnyXppNcgYsiBVz
