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
	"time"

	"golang.org/x/net/context"

	log "github.com/Sirupsen/logrus"

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
