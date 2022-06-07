//go:build js
// +build js

package main

type config struct {
	DebugLevel string `long:"debuglevel" description:"Logging level for all subsystems {trace, debug, info, warn, error, critical} -- You may also specify <subsystem>=<level>,<subsystem2>=<level>,... to set the log level for individual subsystems -- Use show to list available subsystems"`
	NameSpace  string `long:"namespace" description:"The name of the JS namespace in which all the call back functions should be registered"`

	OnLocalPrivCreate  string `long:"onlocalprivcreate" description:"The name of the js callback function that should be called once the local private key has been generated. This function is expected to persist the key so that it can be provided via a command line flag on a future connection"`
	OnRemoteKeyReceive string `long:"onremotekeyreceive" description:"The name of the js callback function to be called once the client receives the remote static key. This function is expected to persist the key so that it can be provided via a command line flag on a future connection"`

	OnAuthData string `long:"onauthdata" description:"The name of the js callback function to be called once the client receives auth data from the remote peer."`
}
