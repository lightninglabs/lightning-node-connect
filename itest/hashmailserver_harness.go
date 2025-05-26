package itest

import (
	"crypto/tls"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/lightninglabs/aperture"
	"github.com/lightningnetwork/lnd/lntest/port"
	"github.com/lightningnetwork/lnd/lntest/wait"
)

type HashmailHarness struct {
	aperture *aperture.Aperture

	// ApertureCfg is the configuration aperture uses when being initialized.
	ApertureCfg *aperture.Config
}

// NewHashmailHarness creates a new instance of the HashmailHarness.
func NewHashmailHarness() *HashmailHarness {
	return &HashmailHarness{
		ApertureCfg: &aperture.Config{
			ListenAddr: fmt.Sprintf("127.0.0.1:%d",
				port.NextAvailablePort()),
			Authenticator: &aperture.AuthConfig{
				Disable: true,
			},
			DatabaseBackend: "etcd",
			Etcd:            &aperture.EtcdConfig{},
			HashMail: &aperture.HashMailConfig{
				Enabled:               true,
				MessageRate:           time.Millisecond,
				MessageBurstAllowance: math.MaxUint32,
			},
			DebugLevel: "debug",
			Tor:        &aperture.TorConfig{},
			Prometheus: &aperture.PrometheusConfig{},
		},
	}
}

// initAperture starts the aperture proxy.
func (hm *HashmailHarness) initAperture() error {
	hm.aperture = aperture.NewAperture(hm.ApertureCfg)
	errChan := make(chan error)
	shutdown := make(chan struct{})

	if err := hm.aperture.Start(errChan, shutdown); err != nil {
		return fmt.Errorf("unable to start aperture: %v", err)
	}

	// Any error while starting?
	select {
	case err := <-errChan:
		return fmt.Errorf("error starting aperture: %v", err)
	default:
	}

	// Wait for aperture to be ready.
	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true,
	}
	return wait.NoError(func() error {
		apertureAddr := fmt.Sprintf("https://%s/dummy",
			hm.ApertureCfg.ListenAddr)

		resp, err := http.Get(apertureAddr)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			return fmt.Errorf("invalid status: %d", resp.StatusCode)
		}

		return nil
	}, 3*time.Second)
}

// Start attempts to start the aperture proxy.
func (hm *HashmailHarness) Start() error {
	if err := hm.initAperture(); err != nil {
		return fmt.Errorf("could not start aperture: %v", err)
	}

	return nil
}

// Stop attempts to stop the aperture proxy.
func (hm *HashmailHarness) Stop() error {
	return hm.aperture.Stop()
}
