// Copyright 2012, 2013 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package apiserver_test

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"os"
	stdtesting "testing"
	"time"

	gc "launchpad.net/gocheck"

	jujutesting "launchpad.net/juju-core/juju/testing"
	"launchpad.net/juju-core/rpc"
	"launchpad.net/juju-core/state"
	"launchpad.net/juju-core/state/api"
	"launchpad.net/juju-core/state/api/params"
	"launchpad.net/juju-core/state/apiserver"
	coretesting "launchpad.net/juju-core/testing"
	jc "launchpad.net/juju-core/testing/checkers"
	"launchpad.net/juju-core/utils"
)

func TestAll(t *stdtesting.T) {
	coretesting.MgoTestPackage(t)
}

var fastDialOpts = api.DialOpts{}

type serverSuite struct {
	jujutesting.JujuConnSuite
}

var _ = gc.Suite(&serverSuite{})

func (s *serverSuite) TestStop(c *gc.C) {
	// Start our own instance of the server so we have
	// a handle on it to stop it.
	srv, err := apiserver.NewServer(s.State, "localhost:0", []byte(coretesting.ServerCert), []byte(coretesting.ServerKey))
	c.Assert(err, gc.IsNil)
	defer srv.Stop()

	stm, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	err = stm.SetProvisioned("foo", "fake_nonce", nil)
	c.Assert(err, gc.IsNil)
	password, err := utils.RandomPassword()
	c.Assert(err, gc.IsNil)
	err = stm.SetPassword(password)
	c.Assert(err, gc.IsNil)

	// Note we can't use openAs because we're not connecting to
	// s.APIConn.
	apiInfo := &api.Info{
		Tag:      stm.Tag(),
		Password: password,
		Nonce:    "fake_nonce",
		Addrs:    []string{srv.Addr()},
		CACert:   []byte(coretesting.CACert),
	}
	st, err := api.Open(apiInfo, fastDialOpts)
	c.Assert(err, gc.IsNil)
	defer st.Close()

	_, err = st.Machiner().Machine(stm.Tag())
	c.Assert(err, gc.IsNil)

	err = srv.Stop()
	c.Assert(err, gc.IsNil)

	_, err = st.Machiner().Machine(stm.Tag())
	// The client has not necessarily seen the server shutdown yet,
	// so there are two possible errors.
	if err != rpc.ErrShutdown && err != io.ErrUnexpectedEOF {
		c.Fatalf("unexpected error from request: %v", err)
	}

	// Check it can be stopped twice.
	err = srv.Stop()
	c.Assert(err, gc.IsNil)
}

func (s *serverSuite) TestOpenAsMachineErrors(c *gc.C) {
	assertNotProvisioned := func(err error) {
		c.Assert(err, gc.NotNil)
		c.Assert(err, jc.Satisfies, params.IsCodeNotProvisioned)
		c.Assert(err, gc.ErrorMatches, `machine \d+ is not provisioned`)
	}
	stm, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	err = stm.SetProvisioned("foo", "fake_nonce", nil)
	c.Assert(err, gc.IsNil)
	password, err := utils.RandomPassword()
	c.Assert(err, gc.IsNil)
	err = stm.SetPassword(password)
	c.Assert(err, gc.IsNil)

	// This does almost exactly the same as OpenAPIAsMachine but checks
	// for failures instead.
	_, info, err := s.APIConn.Environ.StateInfo()
	info.Tag = stm.Tag()
	info.Password = password
	info.Nonce = "invalid-nonce"
	st, err := api.Open(info, fastDialOpts)
	assertNotProvisioned(err)
	c.Assert(st, gc.IsNil)

	// Try with empty nonce as well.
	info.Nonce = ""
	st, err = api.Open(info, fastDialOpts)
	assertNotProvisioned(err)
	c.Assert(st, gc.IsNil)

	// Finally, with the correct one succeeds.
	info.Nonce = "fake_nonce"
	st, err = api.Open(info, fastDialOpts)
	c.Assert(err, gc.IsNil)
	c.Assert(st, gc.NotNil)
	st.Close()

	// Now add another machine, intentionally unprovisioned.
	stm1, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	err = stm1.SetPassword(password)
	c.Assert(err, gc.IsNil)

	// Try connecting, it will fail.
	info.Tag = stm1.Tag()
	info.Nonce = ""
	st, err = api.Open(info, fastDialOpts)
	assertNotProvisioned(err)
	c.Assert(st, gc.IsNil)
}

func (s *serverSuite) TestMachineLoginStartsPinger(c *gc.C) {
	// This is the same steps as OpenAPIAsNewMachine but we need to assert
	// the agent is not alive before we actually open the API.
	// Create a new machine to verify "agent alive" behavior.
	machine, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	err = machine.SetProvisioned("foo", "fake_nonce", nil)
	c.Assert(err, gc.IsNil)
	password, err := utils.RandomPassword()
	c.Assert(err, gc.IsNil)
	err = machine.SetPassword(password)
	c.Assert(err, gc.IsNil)

	// Not alive yet.
	s.assertAlive(c, machine, false)

	// Login as the machine agent of the created machine.
	st := s.OpenAPIAsMachine(c, machine.Tag(), password, "fake_nonce")

	// Make sure the pinger has started.
	s.assertAlive(c, machine, true)

	// Now make sure it stops when connection is closed.
	c.Assert(st.Close(), gc.IsNil)

	// Sync, then wait for a bit to make sure the state is updated.
	s.State.StartSync()
	<-time.After(coretesting.ShortWait)
	s.State.StartSync()

	s.assertAlive(c, machine, false)
}

func (s *serverSuite) TestUnitLoginStartsPinger(c *gc.C) {
	// Create a new service and unit to verify "agent alive" behavior.
	service, err := s.State.AddService("wordpress", s.AddTestingCharm(c, "wordpress"))
	c.Assert(err, gc.IsNil)
	unit, err := service.AddUnit()
	c.Assert(err, gc.IsNil)
	password, err := utils.RandomPassword()
	c.Assert(err, gc.IsNil)
	err = unit.SetPassword(password)
	c.Assert(err, gc.IsNil)

	// Not alive yet.
	s.assertAlive(c, unit, false)

	// Login as the unit agent of the created unit.
	st := s.OpenAPIAs(c, unit.Tag(), password)

	// Make sure the pinger has started.
	s.assertAlive(c, unit, true)

	// Now make sure it stops when connection is closed.
	c.Assert(st.Close(), gc.IsNil)

	// Sync, then wait for a bit to make sure the state is updated.
	s.State.StartSync()
	<-time.After(coretesting.ShortWait)
	s.State.StartSync()

	s.assertAlive(c, unit, false)
}

type agentAliver interface {
	AgentAlive() (bool, error)
}

func (s *serverSuite) assertAlive(c *gc.C, entity agentAliver, isAlive bool) {
	s.State.StartSync()
	alive, err := entity.AgentAlive()
	c.Assert(err, gc.IsNil)
	c.Assert(alive, gc.Equals, isAlive)
}

type charmsSuite struct {
	jujutesting.JujuConnSuite
	machineTag string
	password   string
}

var _ = gc.Suite(&charmsSuite{})

func (s *charmsSuite) SetUpTest(c *gc.C) {
	s.JujuConnSuite.SetUpTest(c)
	machine, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	err = machine.SetProvisioned("foo", "fake_nonce", nil)
	c.Assert(err, gc.IsNil)
	password, err := utils.RandomPassword()
	c.Assert(err, gc.IsNil)
	err = machine.SetPassword(password)
	c.Assert(err, gc.IsNil)
	s.machineTag = machine.Tag()
	s.password = password
}

func (s *charmsSuite) charmsUri(c *gc.C, query string) string {
	_, info, err := s.APIConn.Environ.StateInfo()
	c.Assert(err, gc.IsNil)
	return "https://" + info.Addrs[0] + "/charms" + query
}

func (s *charmsSuite) sendRequest(c *gc.C, tag, password, method, uri, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, uri, body)
	c.Assert(err, gc.IsNil)
	if tag != "" && password != "" {
		req.SetBasicAuth(tag, password)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return utils.GetNonValidatingHTTPClient().Do(req)
}

func (s *charmsSuite) authRequest(c *gc.C, method, uri, contentType string, body io.Reader) (*http.Response, error) {
	return s.sendRequest(c, s.machineTag, s.password, method, uri, contentType, body)
}

func (s *charmsSuite) uploadRequest(c *gc.C, uri string, paths ...string) (*http.Response, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	prepare := func(i int, path string) error {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		name := fmt.Sprintf("charm%d", i)
		part, err := writer.CreateFormFile(name, name+".zip")
		if err != nil {
			return err
		}
		if _, err := io.Copy(part, file); err != nil {
			return err
		}
		return nil
	}

	for i, path := range paths {
		if err := prepare(i, path); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return s.authRequest(c, "POST", uri, writer.FormDataContentType(), body)
}

func (s *charmsSuite) TestCharmsServedSecurely(c *gc.C) {
	_, info, err := s.APIConn.Environ.StateInfo()
	c.Assert(err, gc.IsNil)
	uri := "http://" + info.Addrs[0] + "/charms"
	_, err = s.sendRequest(c, "", "", "GET", uri, "", nil)
	c.Assert(err, gc.ErrorMatches, `.*malformed HTTP response.*`)
}

func (s *charmsSuite) TestCharmsUploadRequiresAuth(c *gc.C) {
	resp, err := s.sendRequest(c, "", "", "GET", s.charmsUri(c, ""), "", nil)
	c.Assert(err, gc.IsNil)
	c.Assert(resp.StatusCode, gc.Equals, http.StatusUnauthorized)
}

func (s *charmsSuite) TestCharmsUploadRequiresPOST(c *gc.C) {
	resp, err := s.authRequest(c, "GET", s.charmsUri(c, ""), "", nil)
	c.Assert(err, gc.IsNil)
	c.Assert(resp.StatusCode, gc.Equals, http.StatusMethodNotAllowed)
	assertBody(c, resp, "unsupported method: GET\n")
}

func (s *charmsSuite) TestCharmsUploadRequiresSeries(c *gc.C) {
	resp, err := s.authRequest(c, "POST", s.charmsUri(c, ""), "", nil)
	c.Assert(err, gc.IsNil)
	c.Assert(resp.StatusCode, gc.Equals, http.StatusBadRequest)
	assertBody(c, resp, "expected series= URL argument\n")
}

func (s *charmsSuite) TestCharmsUploadRequiresMultipartForm(c *gc.C) {
	resp, err := s.authRequest(c, "POST", s.charmsUri(c, "?series=quantal"), "", nil)
	c.Assert(err, gc.IsNil)
	c.Assert(resp.StatusCode, gc.Equals, http.StatusBadRequest)
	assertBody(c, resp, "request Content-Type isn't multipart/form-data\n")
}

func (s *charmsSuite) TestCharmsUploadRequiresUploadedFile(c *gc.C) {
	resp, err := s.uploadRequest(c, s.charmsUri(c, "?series=quantal"))
	c.Assert(err, gc.IsNil)
	c.Assert(resp.StatusCode, gc.Equals, http.StatusBadRequest)
	assertBody(c, resp, "expected a single uploaded file, got none\n")
}

func (s *charmsSuite) TestCharmsUploadRequiresSingleUploadedFile(c *gc.C) {
	// Create an empty file.
	tempFile, err := ioutil.TempFile(c.MkDir(), "charm")
	c.Assert(err, gc.IsNil)
	path := tempFile.Name()

	resp, err := s.uploadRequest(c, s.charmsUri(c, "?series=quantal"), path, path)
	c.Assert(err, gc.IsNil)
	c.Assert(resp.StatusCode, gc.Equals, http.StatusBadRequest)
	assertBody(c, resp, "expected a single uploaded file, got more\n")
}

func (s *charmsSuite) TestChramsFailsWithInvalidZip(c *gc.C) {
	// Create an empty file.
	tempFile, err := ioutil.TempFile(c.MkDir(), "charm")
	c.Assert(err, gc.IsNil)

	resp, err := s.uploadRequest(c, s.charmsUri(c, "?series=quantal"), tempFile.Name())
	c.Assert(err, gc.IsNil)
	c.Assert(resp.StatusCode, gc.Equals, http.StatusBadRequest)
	assertBody(c, resp, "invalid charm archive: zip: not a valid zip file\n")
}

func (s *charmsSuite) TestCharmsUploadSuccess(c *gc.C) {
	archivePath := coretesting.Charms.BundlePath(c.MkDir(), "dummy")
	resp, err := s.uploadRequest(c, s.charmsUri(c, "?series=quantal"), archivePath)
	c.Assert(err, gc.IsNil)
	c.Assert(resp.StatusCode, gc.Equals, http.StatusOK)
	assertBody(c, resp, "local:quantal/dummy\n")
}

func assertBody(c *gc.C, resp *http.Response, expected string) {
	body, err := ioutil.ReadAll(resp.Body)
	defer resp.Body.Close()
	c.Assert(err, gc.IsNil)
	c.Assert(string(body), gc.Matches, expected)
}
