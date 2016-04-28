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

package handlers

import (
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"time"

	"golang.org/x/net/context"

	log "github.com/Sirupsen/logrus"

	"github.com/go-swagger/go-swagger/httpkit"
	middleware "github.com/go-swagger/go-swagger/httpkit/middleware"
	"github.com/vmware/vic/apiservers/portlayer/models"
	"github.com/vmware/vic/apiservers/portlayer/restapi/operations"
	"github.com/vmware/vic/apiservers/portlayer/restapi/operations/interaction"
	"github.com/vmware/vic/apiservers/portlayer/restapi/options"
	"github.com/vmware/vic/pkg/vsphere/session"
	"github.com/vmware/vic/portlayer/attach"
)

// ExecHandlersImpl is the receiver for all of the exec handler methods
type InteractionHandlersImpl struct {
	attachServer *attach.Server
}

var (
	interactionSession = &session.Session{}
)

func (i *InteractionHandlersImpl) Configure(api *operations.PortLayerAPI, _ *HandlerContext) {
	var err error

	api.InteractionContainerResizeHandler = interaction.ContainerResizeHandlerFunc(i.ContainerResizeHandler)
	api.InteractionContainerSetStdinHandler = interaction.ContainerSetStdinHandlerFunc(i.ContainerSetStdinHandler)
	api.InteractionContainerGetStdoutHandler = interaction.ContainerGetStdoutHandlerFunc(i.ContainerGetStdoutHandler)
	api.InteractionContainerGetStderrHandler = interaction.ContainerGetStderrHandlerFunc(i.ContainerGetStderrHandler)
	api.InteractionContainerDetachStdioHandler = interaction.ContainerDetachStdioHandlerFunc(i.ContainerDetachStdioHandler)

	ctx := context.Background()

	sessionconfig := &session.Config{
		Service:        options.PortLayerOptions.SDK,
		Insecure:       options.PortLayerOptions.Insecure,
		Keepalive:      options.PortLayerOptions.Keepalive,
		DatacenterPath: options.PortLayerOptions.DatacenterPath,
		ClusterPath:    options.PortLayerOptions.ClusterPath,
		PoolPath:       options.PortLayerOptions.PoolPath,
		DatastorePath:  options.PortLayerOptions.DatastorePath,
		NetworkPath:    options.PortLayerOptions.NetworkPath,
	}

	interactionSession, err = session.NewSession(sessionconfig).Create(ctx)
	if err != nil {
		log.Fatalf("InteractionHandler ERROR: %s", err)
	}

	i.attachServer = attach.NewAttachServer("", 0)

	if err := i.attachServer.Start(); err != nil {
		log.Fatalf("Attach server unable to start: %s", err)
		return
	}
}

func (i *InteractionHandlersImpl) GetServer() *attach.Server {
	return i.attachServer
}

func (i *InteractionHandlersImpl) ContainerResizeHandler(params interaction.ContainerResizeParams) middleware.Responder {
	// Get the ssh session to the container
	connContainer, err := i.attachServer.Get(context.Background(), params.ID, 600*time.Second)

	if err != nil {
		retErr := &models.Error{Message: fmt.Sprintf("No such container: %s", params.ID)}
		return interaction.NewContainerResizeNotFound().WithPayload(retErr)
	}

	// Request a resize

	cWidth := uint32(params.Width)
	cHeight := uint32(params.Height)

	err = connContainer.Resize(cWidth, cHeight, 0, 0)

	if err != nil {
		retErr := &models.Error{Message: "SSH Session failed to resize the container's TTY"}
		return interaction.NewContainerResizeInternalServerError().WithPayload(retErr)
	}

	return interaction.NewContainerResizeOK()
}

func (i *InteractionHandlersImpl) ContainerSetStdinHandler(params interaction.ContainerSetStdinParams) middleware.Responder {
	log.Printf("Attempting to get ssh session for container %s", params.ID)
	// Get the ssh session streams
	sshConn, err := i.attachServer.Get(context.Background(), params.ID, 600*time.Second)

	if err != nil {
		e := &models.Error{Message: fmt.Sprintf("No stdin found for %s", params.ID)}
		return interaction.NewContainerSetStdinNotFound().WithPayload(e)
	} else {
		go func() {
			_, err := io.Copy(sshConn.Stdin(), params.RawStream)

			if err != nil {
				log.Printf("Error copying stdin for container %s", params.ID)
			}
		}()
	}

	return interaction.NewContainerSetStdinOK()
}

func (i *InteractionHandlersImpl) ContainerGetStdoutHandler(params interaction.ContainerGetStdoutParams) middleware.Responder {
	log.Printf("Attempting to get ssh session for container %s", params.ID)
	// Get the ssh session streams
	sshConn, err := i.attachServer.Get(context.Background(), params.ID, 600*time.Second)

	if err != nil {
		e := &models.Error{Message: fmt.Sprintf("No stdout found for %s", params.ID)}
		return interaction.NewContainerGetStdoutNotFound().WithPayload(e)
	} else {
		return NewContainerOutputHandler().WithPayload(sshConn.Stdout(), params.ID)
	}

	return interaction.NewContainerGetStdoutOK().WithPayload(ioutil.NopCloser(sshConn.Stdout()))
}

func (i *InteractionHandlersImpl) ContainerGetStderrHandler(params interaction.ContainerGetStderrParams) middleware.Responder {
	log.Printf("Attempting to get ssh session for container %s", params.ID)
	// Get the ssh session streams
	sshConn, err := i.attachServer.Get(context.Background(), params.ID, 600*time.Second)

	if err != nil {
		e := &models.Error{Message: fmt.Sprintf("No stderr found for %s", params.ID)}
		return interaction.NewContainerGetStderrNotFound().WithPayload(e)
	} else {
		return NewContainerOutputHandler().WithPayload(sshConn.Stderr(), params.ID)
	}

	return interaction.NewContainerGetStderrOK()
}

func (i *InteractionHandlersImpl) ContainerDetachStdioHandler(params interaction.ContainerDetachStdioParams) middleware.Responder {
	log.Printf("Attempting to get ssh session for container %s", params.ID)
	// Get the ssh session streams
	//	sshConn, err := i.attachServer.Get(context.Background(), params.ID, 600*time.Second)

	return interaction.NewContainerDetachStdioOK()
}

// Custom return handlers for stdout/stderr

type ContainerOutputHandler struct {
	outputStream io.Reader
	containerId  string
}

// NewContainerSetStdinInternalServerError creates ContainerSetStdinInternalServerError with default headers values
func NewContainerOutputHandler() *ContainerOutputHandler {
	return &ContainerOutputHandler{}
}

// WithPayload adds the payload to the container set stdin internal server error response
func (o *ContainerOutputHandler) WithPayload(payload io.Reader, id string) *ContainerOutputHandler {
	o.outputStream = payload
	o.containerId = id
	return o
}

// WriteResponse to the client
func (o *ContainerOutputHandler) WriteResponse(rw http.ResponseWriter, producer httpkit.Producer) {
	rw.WriteHeader(200)
	rw.Header().Set("Content-Type", "application/octet-streaming")
	rw.Header().Set("Transfer-Encoding", "chunked")
	mw := io.MultiWriter(os.Stdout, rw)
	_, err := io.Copy(mw, o.outputStream)

	if err != nil {
		log.Printf("Error copying output for container %s: %s", o.containerId, err)
	}
}
