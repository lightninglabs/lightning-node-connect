//go:build js
// +build js

package main

import "fmt"

type config struct {
	DebugLevel string `long:"debuglevel" description:"Logging level for all subsystems {trace, debug, info, warn, error, critical} -- You may also specify <subsystem>=<level>,<subsystem2>=<level>,... to set the log level for individual subsystems -- Use show to list available subsystems"`
	NameSpace  string `long:"namespace" description:"The name of the JS namespace in which all the call back functions should be registered"`
}

func (c *config) validate() error {
	if c.NameSpace == "" {
		return fmt.Errorf("a non-empty namespace is required")
	}

	return nil
}
