package mailbox

import (
	"fmt"
	"net"
	"sync"
)

// SwitchConn is an abstraction over an underlying ProxyConn. It allows us to
// keep the same SwitchConn and use it as a ProxyConn while also allowing us to
// switch out the ProxyConn with a new one if needed.
type SwitchConn struct {
	*SwitchConfig

	ProxyConn

	closeOnce sync.Once
	quit      chan struct{}
}

// SwitchConfig holds all the config values and functions used by SwitchConn.
type SwitchConfig struct {
	ServerHost string
	SID        [64]byte

	// NewProxyConn should return a new ProxyConn given the sid provided.
	NewProxyConn func(sid [64]byte) (ProxyConn, error)

	// RefreshProxyConn should return a ProxyConn that effectively
	// refreshes the one passed in.
	RefreshProxyConn func(conn ProxyConn) (ProxyConn, error)

	// StopProxyCon should stop and clean up the given ProxyConn.
	StopProxyConn func(conn ProxyConn) error
}

// NextSwitchConn returns a new SwitchConn or refreshed SwitchConn depending on
// if a nil SwitchConn is passed in or not.
func NextSwitchConn(sc *SwitchConn, cfg *SwitchConfig) (*SwitchConn, error) {
	if sc == nil {
		return newSwitchConn(cfg)
	}

	return refreshSwitchConn(sc)
}

// newSwitchConn creates a new SwitchConn object with a new underlying
// ProxyConn.
func newSwitchConn(cfg *SwitchConfig) (*SwitchConn, error) {
	mailboxConn, err := cfg.NewProxyConn(cfg.SID)
	if err != nil {
		return nil, fmt.Errorf("couldn't create new server: %v", err)
	}

	return &SwitchConn{
		SwitchConfig: cfg,
		ProxyConn:    mailboxConn,
		quit:         make(chan struct{}),
	}, nil
}

// refreshSwitchConn refreshes both the SwitchConn by creating a new one with
// a new quit channel and also refreshes the underlying ProxyConn.
func refreshSwitchConn(s *SwitchConn) (*SwitchConn, error) {
	sc := &SwitchConn{
		SwitchConfig: s.SwitchConfig,
		quit:         make(chan struct{}),
	}

	conn, err := s.RefreshProxyConn(s.ProxyConn)
	if err != nil {
		return nil, err
	}

	sc.ProxyConn = conn

	return sc, nil
}

// Addr returns the current address of the SwitchConn.
func (s *SwitchConn) Addr() net.Addr {
	return &Addr{SID: s.SID, Server: s.ServerHost}
}

// Close cleans up the SwitchConn and its ProxyConn.
func (s *SwitchConn) Close() error {
	var returnErr error
	s.closeOnce.Do(func() {
		if err := s.ProxyConn.Close(); err != nil {
			returnErr = err
		}

		close(s.quit)
	})

	return returnErr
}

// Done returns the SwitchConn's quit channel which can be used to determine
// if the Switch conn has completed closing or not.
func (s *SwitchConn) Done() chan struct{} {
	return s.quit
}

// Switch stops the current ProxyConn and switches it out with a new ProxyConn.
func (s *SwitchConn) Switch(conn ProxyConn, sid [64]byte) error {
	if err := s.StopProxyConn(s.ProxyConn); err != nil {
		return err
	}

	s.SID = sid
	s.ProxyConn = conn

	return nil
}
