//go:build js
// +build js

package main

import (
	"errors"
	"fmt"
)

type config struct {
	DebugLevel string `long:"debuglevel" description:"Logging level for all subsystems {trace, debug, info, warn, error, critical} -- You may also specify <subsystem>=<level>,<subsystem2>=<level>,... to set the log level for individual subsystems -- Use show to list available subsystems"`
	NameSpace  string `long:"namespace" description:"The name of the JS namespace in which all the call back functions should be registered"`

	LocalPrivate string `long:"localprivate" description:"The static local private key to be used for creating the noise connection"`
	RemotePublic string `long:"remotepublic" description:"The static remote public key to be used for creating the noise connection"`

	OnLocalPrivCreate  string `long:"onlocalprivcreate" description:"The name of the js callback function that should be called once the local private key has been generated. This function is expected to persist the key so that it can be provided via a command line flag on a future connection"`
	OnRemoteKeyReceive string `long:"onremotekeyreceive" description:"The name of the js callback function to be called once the client receives the remote static key. This function is expected to persist the key so that it can be provided via a command line flag on a future connection"`

	OnAuthData string `long:"onauthdata" description:"The name of the js callback function to be called once the client receives auth data from the remote peer."`
}

func (c *config) validate() error {
	if c.NameSpace == "" {
		return fmt.Errorf("a non-empty namespace is required")
	}

	if c.RemotePublic != "" && c.LocalPrivate == "" {
		return errors.New("cannot set remote pub key if local priv " +
			"key is not also set")
	}

	if c.LocalPrivate == "" && c.OnLocalPrivCreate == "" {
		return errors.New("OnLocalPrivCreate must be defined if a " +
			"local key is not provided")
	}

	if c.RemotePublic == "" && c.OnRemoteKeyReceive == "" {
		return errors.New("OnRemoteKeyReceive must be defined if a " +
			"remote key is not provided")
	}

	return nil
}
