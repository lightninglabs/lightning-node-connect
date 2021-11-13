package itest

import (
	"crypto/tls"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/lightningnetwork/lnd/lntest"
	"github.com/lightningnetwork/lnd/lntest/wait"

	"github.com/lightninglabs/aperture"
)

type hashmailHarness struct {
	aperture    *aperture.Aperture
	apertureCfg *aperture.Config
}

func newHashmailHarness() *hashmailHarness {
	return &hashmailHarness{
		apertureCfg: &aperture.Config{
			ListenAddr: fmt.Sprintf("127.0.0.1:%d",
				lntest.NextAvailablePort()),
			Authenticator: &aperture.AuthConfig{
				Disable: true,
			},
			Etcd: &aperture.EtcdConfig{},
			HashMail: &aperture.HashMailConfig{
				Enabled:               true,
				MessageRate:           time.Millisecond,
				MessageBurstAllowance: math.MaxUint32,
			},
			DebugLevel: "debug",
		},
	}
}

// initAperture starts the aperture proxy.
func (hm *hashmailHarness) initAperture() error {
	hm.aperture = aperture.NewAperture(hm.apertureCfg)
	errChan := make(chan error)

	if err := hm.aperture.Start(errChan); err != nil {
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
			hm.apertureCfg.ListenAddr)

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

func (hm *hashmailHarness) start() error {
	if err := hm.initAperture(); err != nil {
		return fmt.Errorf("could not start aperture: %v", err)
	}

	return nil
}

func (hm *hashmailHarness) stop() error {
	return hm.aperture.Stop()
}
