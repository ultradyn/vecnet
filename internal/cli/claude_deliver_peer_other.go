//go:build !linux

package cli

// Without a qualified kernel peer-identity method there is no owner fence,
// so `claude deliver` fails closed as a pre-write refusal (exit 5) before
// any connect: native delivery stays legacy on unqualified platforms.

import "net"

func claudeDeliverVerifyPeer(conn *net.UnixConn, expectPID, expectUID, expectStart string) error {
	_ = conn
	return usageErr("claude deliver: peer identity verification is not qualified on this platform")
}

func claudeDeliverSocketValid(path, expectUID string) error {
	return usageErr("claude deliver: peer identity verification is not qualified on this platform")
}
