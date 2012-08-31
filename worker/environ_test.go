package worker_test

import (
	. "launchpad.net/gocheck"
	"launchpad.net/juju-core/environs"
	"launchpad.net/juju-core/environs/config"
	"launchpad.net/juju-core/juju/testing"
	coretesting "launchpad.net/juju-core/testing"
	"launchpad.net/juju-core/worker"
	"launchpad.net/tomb"
	stdtesting "testing"
)

type suite struct {
	testing.JujuConnSuite
}

var _ = Suite(&suite{})

func TestPackage(t *stdtesting.T) {
	coretesting.ZkTestPackage(t)
}

func (s *suite) TestStop(c *C) {
	w := s.State.WatchEnvironConfig()
	stop := make(chan struct{})
	done := make(chan error)
	go func() {
		env, err := worker.WaitForEnviron(w, stop)
		c.Assert(env, IsNil)
		done <- err
	}()
	close(stop)
	c.Assert(<-done, Equals, tomb.ErrDying)
}

func (s *suite) TestInvalidConfig(c *C) {
	// Create an invalid config by taking the current config and
	// tweaking the provider type.
	cfg, err := s.State.EnvironConfig()
	c.Assert(err, IsNil)
	m := cfg.AllAttrs()
	m["type"] = "unknown"
	invalidCfg, err := config.New(m)
	c.Assert(err, IsNil)

	err = s.State.SetEnvironConfig(invalidCfg)
	c.Assert(err, IsNil)

	w := s.State.WatchEnvironConfig()
	done := make(chan environs.Environ)
	go func() {
		env, err := worker.WaitForEnviron(w, nil)
		c.Assert(err, IsNil)
		done <- env
	}()
	// Wait for the loop to process the invalid configuratrion
	<-worker.LoadedInvalid

	// Then load a valid configuration back in.
	m = cfg.AllAttrs()
	m["secret"] = "environ_test"
	validCfg, err := config.New(m)
	c.Assert(err, IsNil)

	err = s.State.SetEnvironConfig(validCfg)
	c.Assert(err, IsNil)

	env := <-done
	c.Assert(env.Config().AllAttrs()["secret"], Equals, "environ_test")
}
