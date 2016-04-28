// Copyright 2016 VMware, Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package middleware

import (
	//	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/context"

	log "github.com/Sirupsen/logrus"

	"github.com/docker/docker/pkg/stdcopy"

	"github.com/vmware/vic/apiservers/portlayer/models"
	"github.com/vmware/vic/pkg/attachutils"
	"github.com/vmware/vic/portlayer/attach"
)

type AttachHijacker struct {
	origHandler  http.Handler
	attachServer *attach.Server

	containerID string
	useStdin    bool
	useStdout   bool
	useStderr   bool
	stream      bool
}

const basePath = "/containers"
const operation = "/attach"

//NewAttachHijacker handles attach for the interaction handler.  With the current
//version of go-swagger, we cannot implement attach with a standard swagger
//handler.
func NewAttachHijacker(orig http.Handler, as *attach.Server) *AttachHijacker {
	return &AttachHijacker{origHandler: orig, attachServer: as}
}

func (a *AttachHijacker) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	// Make sure the call is for /containers/{id}/attach
	if !strings.HasPrefix(r.URL.Path, basePath) || !strings.Contains(r.URL.Path, operation) {
		a.origHandler.ServeHTTP(rw, r)
		return
	}

	code, err := a.GetParams(r)
	if err != nil {
		log.Errorf("interaction attach container failed %#v\n", err)
		a.WritePortlayerError(rw, code, err.Error())
		return
	}

	// Get the stdio streams from the hijacked connection
	hijackedStdin, hijackedStdout, hijackedStderr, err := attachutils.HijackConnection(rw, r)
	if err != nil {
		a.WritePortlayerError(rw, http.StatusInternalServerError, "Error getting hijacked streams")
		return
	}

	log.Printf("Attempting to get ssh session for container %s", a.containerID)
	// Get the ssh session streams
	sshConn, err := a.attachServer.Get(context.Background(), a.containerID, 600*time.Second)

	log.Printf("Attached!")

	// replace the stdout/stderr with Docker's multiplex stream
	hijackedStderr = stdcopy.NewStdWriter(hijackedStderr, stdcopy.Stderr)
	hijackedStdout = stdcopy.NewStdWriter(hijackedStdout, stdcopy.Stdout)

	//	 Starts up the stream copying and wait for them to stop or error.  The
	//	 streams are from the hijacked connection of this API server.  The conn
	//	 is the connection to the portlayer server.
	err = attachutils.AttachStreams(
		false,
		hijackedStdin,
		hijackedStdout,
		hijackedStderr,
		sshConn.Stdin(),
		sshConn.Stdout(),
		sshConn.Stderr(),
		[]byte{})

	//We return errors, but it really doesn't matter.  We've hijacked the connection
	//from both go-swagger and the go http handlers.
	if err != nil {
		log.Printf("Got error during stream copy %s", err)
		a.WritePortlayerError(rw, http.StatusBadRequest, "Error attaching the connection to the container stio streams")
		return
	}

	//Return success
	a.WritePortlayerSuccess(rw)
}

func (a *AttachHijacker) GetParams(r *http.Request) (int, error) {
	//Extract container id from path.
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 {
		return http.StatusBadRequest, fmt.Errorf("Invalid URL for container attach")
	}

	a.containerID = parts[2]

	//Extract boolean values stdin, stdout, stderr, and stream from query inputs
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return http.StatusBadRequest, fmt.Errorf("Could not retrieve request input for container attach")
	}

	if reqParts, exist := values["stdin"]; exist {
		if len(reqParts) > 0 {
			a.useStdin = (strings.Compare("1", reqParts[0]) == 0)
		}
	} else {
		a.useStdin = false
	}

	if reqParts, exist := values["stdout"]; exist {
		if len(reqParts) > 0 {
			a.useStdout = (strings.Compare("1", reqParts[0]) == 0)
		}
	} else {
		a.useStdout = false
	}

	if reqParts, exist := values["stderr"]; exist {
		if len(reqParts) > 0 {
			a.useStderr = (strings.Compare("1", reqParts[0]) == 0)
		}
	} else {
		a.useStderr = false
	}

	if reqParts, exist := values["stream"]; exist {
		if len(reqParts) > 0 {
			a.stream = (strings.Compare("1", reqParts[0]) == 0)
		}
	} else {
		a.stream = false
	}

	return http.StatusOK, nil
}

func (a *AttachHijacker) WritePortlayerError(rw http.ResponseWriter, statusCode int, errMsg string) {
	retErr := &models.Error{Message: errMsg}

	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(statusCode)

	json.NewEncoder(rw).Encode(retErr)
}

func (a *AttachHijacker) WritePortlayerSuccess(rw http.ResponseWriter) {
	rw.WriteHeader(http.StatusOK)
	rw.Write([]byte("OK"))
}
