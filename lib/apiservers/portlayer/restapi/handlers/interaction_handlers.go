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
	//	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"golang.org/x/net/context"

	log "github.com/Sirupsen/logrus"

	"github.com/go-swagger/go-swagger/httpkit"
	middleware "github.com/go-swagger/go-swagger/httpkit/middleware"
	"github.com/vmware/vic/lib/apiservers/portlayer/models"
	"github.com/vmware/vic/lib/apiservers/portlayer/restapi/operations"
	"github.com/vmware/vic/lib/apiservers/portlayer/restapi/operations/interaction"
	"github.com/vmware/vic/lib/apiservers/portlayer/restapi/options"
	"github.com/vmware/vic/lib/portlayer/attach"
	"github.com/vmware/vic/pkg/vsphere/session"
)

// ExecHandlersImpl is the receiver for all of the exec handler methods
type InteractionHandlersImpl struct {
	attachServer *attach.Server
	inStreamMap  map[string]*DetachableReader
	outStreamMap map[string]*DetachableReader
	errStreamMap map[string]*DetachableReader
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
	api.InteractionContainerDetachHandler = interaction.ContainerDetachHandlerFunc(i.ContainerDetachHandler)

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

	i.inStreamMap = make(map[string]*DetachableReader)
	i.outStreamMap = make(map[string]*DetachableReader)
	i.errStreamMap = make(map[string]*DetachableReader)

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
	log.Printf("Attempting to get ssh session for container %s stdin", params.ID)
	sshConn, err := i.attachServer.Get(context.Background(), params.ID, 600*time.Second)
	if err != nil {
		e := &models.Error{Message: fmt.Sprintf("No stdin found for %s", params.ID)}
		return interaction.NewContainerSetStdinNotFound().WithPayload(e)
	}

	detachableIn := NewDetachableReader(params.RawStream)
	i.inStreamMap[params.ID] = detachableIn
	_, err = io.Copy(sshConn.Stdin(), detachableIn)
	if err != nil {
		log.Printf("Error copying stdin for container %s", params.ID)
	}

	log.Printf("*Done copying stdin")

	return interaction.NewContainerSetStdinOK()
}

func (i *InteractionHandlersImpl) ContainerGetStdoutHandler(params interaction.ContainerGetStdoutParams) middleware.Responder {
	log.Printf("Attempting to get ssh session for container %s stdout", params.ID)
	sshConn, err := i.attachServer.Get(context.Background(), params.ID, 600*time.Second)
	if err != nil {
		e := &models.Error{Message: fmt.Sprintf("No stdout found for %s", params.ID)}
		return interaction.NewContainerGetStdoutNotFound().WithPayload(e)
	}

	detachableOut := NewDetachableReader(sshConn.Stdout())
	i.outStreamMap[params.ID] = detachableOut
	return NewContainerOutputHandler().WithPayload(detachableOut, params.ID)
}

func (i *InteractionHandlersImpl) ContainerGetStderrHandler(params interaction.ContainerGetStderrParams) middleware.Responder {
	log.Printf("Attempting to get ssh session for container %s stderr", params.ID)
	sshConn, err := i.attachServer.Get(context.Background(), params.ID, 600*time.Second)
	if err != nil {
		e := &models.Error{Message: fmt.Sprintf("No stderr found for %s", params.ID)}
		return interaction.NewContainerGetStderrNotFound().WithPayload(e)
	}

	detachableErr := NewDetachableReader(sshConn.Stderr())
	i.outStreamMap[params.ID] = detachableErr
	return NewContainerOutputHandler().WithPayload(detachableErr, params.ID)
}

func (i *InteractionHandlersImpl) ContainerDetachHandler(params interaction.ContainerDetachParams) middleware.Responder {
	if inReader, ok := i.inStreamMap[params.ID]; ok {
		inReader.Detach()
		delete(i.inStreamMap, params.ID)
	}
	if outReader, ok := i.outStreamMap[params.ID]; ok {
		outReader.Detach()
		delete(i.outStreamMap, params.ID)
	}
	if errReader, ok := i.errStreamMap[params.ID]; ok {
		errReader.Detach()
		delete(i.errStreamMap, params.ID)
	}

	return interaction.NewContainerDetachOK()
}

// Custom reader to allow us to detach cleanly during an io.Copy

type GenericFlusher interface {
	Flush()
}

type DetachableReader struct {
	io.Reader
	io.WriterTo

	detached bool
	flusher  GenericFlusher
}

func NewDetachableReader(rdr io.Reader) *DetachableReader {
	return &DetachableReader{Reader: rdr, detached: false, flusher: nil}
}

func (d *DetachableReader) Detach() {
	d.detached = true
}

func (d *DetachableReader) AddFlusher(flusher GenericFlusher) {
	d.flusher = flusher
}

// Derived from go's io.Copy
func (d *DetachableReader) WriteTo(w io.Writer) (written int64, err error) {
	buf := make([]byte, 64)

	for {
		if d.detached {
			err = nil
			break
		}
		nr, er := d.Read(buf)
		if nr > 0 {
			if d.detached {
				err = nil
				break
			}
			nw, ew := w.Write(buf[0:nr])
			os.Stdout.Write(buf[0:nr])
			if d.flusher != nil {
				d.flusher.Flush()
			}
			if nw > 0 {
				written += int64(nw)
			}
			if ew != nil {
				err = ew
				break
			}
			if nr != nw {
				err = io.ErrShortWrite
				break
			}
		}
		if er == io.EOF {
			break
		}
		if er != nil {
			err = er
			break
		}
	}
	return written, err
}

// Custom return handlers for stdout/stderr

type ContainerOutputHandler struct {
	outputStream *DetachableReader
	containerID  string
}

// NewContainerSetStdinInternalServerError creates ContainerSetStdinInternalServerError with default headers values
func NewContainerOutputHandler() *ContainerOutputHandler {
	return &ContainerOutputHandler{}
}

// WithPayload adds the payload to the container set stdin internal server error response
func (c *ContainerOutputHandler) WithPayload(payload *DetachableReader, id string) *ContainerOutputHandler {
	c.outputStream = payload
	c.containerID = id
	return c
}

func (c *ContainerOutputHandler) DetachReader() {
	c.outputStream.Detach()
}

// WriteResponse to the client
func (c *ContainerOutputHandler) WriteResponse(rw http.ResponseWriter, producer httpkit.Producer) {
	rw.WriteHeader(http.StatusOK)
	rw.Header().Set("Content-Type", "application/octet-streaming")
	rw.Header().Set("Transfer-Encoding", "chunked")
	if f, ok := rw.(http.Flusher); ok {
		c.outputStream.AddFlusher(f)
	}
	_, err := io.Copy(rw, c.outputStream)

	if err != nil {
		log.Printf("Error copying output for container %s: %s", c.containerID, err)
	} else {
		log.Printf("Finished copying stream for container %s", c.containerID)
	}
}
