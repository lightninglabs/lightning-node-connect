package main

import (
	faraday "github.com/lightninglabs/faraday/frdrpcserver/perms"
	loopd "github.com/lightninglabs/loop/loopd/perms"
	poold "github.com/lightninglabs/pool/perms"
	"github.com/lightningnetwork/lnd"
	"gopkg.in/macaroon-bakery.v2/bakery"
)

// getAllMethodPermissions returns a merged map of all litd's method
// permissions.
func getAllMethodPermissions() map[string][]bakery.Op {
	mapSize := len(lnd.MainRPCServerPermissions()) +
		len(faraday.RequiredPermissions) +
		len(loopd.RequiredPermissions) + len(poold.RequiredPermissions)

	allPerms := make(map[string][]bakery.Op, mapSize)
	for key, value := range lnd.MainRPCServerPermissions() {
		allPerms[key] = value
	}
	for key, value := range faraday.RequiredPermissions {
		allPerms[key] = value
	}
	for key, value := range loopd.RequiredPermissions {
		allPerms[key] = value
	}
	for key, value := range poold.RequiredPermissions {
		allPerms[key] = value
	}
	return allPerms
}
