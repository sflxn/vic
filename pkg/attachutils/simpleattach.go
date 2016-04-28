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

package attachutils

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"sync"
	"time"

	log "github.com/Sirupsen/logrus"

	"github.com/docker/docker/pkg/ioutils"
)

// HijackClosure function type returned from HijackConnection().  The call can
// later be used to return stdio streams for the connection.
type HijackClosure func() (io.ReadCloser, io.Writer, io.Writer, error)

// AttachToDockerLikeServer is a utility function that assumes the server that
// is being attempted for attach follows a URL path along the line of
// /containers/{containerID}/attach?
func AttachToDockerLikeServer(baseURL, containerID, attachVerb string, useStdin, useStdout, useStderr, stream bool) (net.Conn, *bufio.Reader, error) {
	var options map[string]string
	var buf bytes.Buffer

	// Build up the request endpoint with params from ContainerAttachConfig
	options = make(map[string]string)

	if useStdin {
		options["stdin"] = "1"
	}

	if useStdout {
		options["stdout"] = "1"
	}

	if useStderr {
		options["stderr"] = "1"
	}

	if stream {
		options["stream"] = "1"
	}

	for key, value := range options {
		if buf.Len() > 0 {
			buf.WriteString("&")
		}
		buf.WriteString(key)
		buf.WriteString("=")
		buf.WriteString(value)
	}

	endpoint := baseURL + "/containers/" + containerID + "/attach?" + buf.String()

	return AttachToServer(endpoint)
}

// AttachToServer connects to a server, given the address in 'server' and returns
// a net conn and buffered reader.
func AttachToServer(server string) (net.Conn, *bufio.Reader, error) {
	method := "POST"

	// Create a connection client and request
	c, err := net.DialTimeout("tcp", server, time.Duration(10*time.Second))
	if err != nil {
		retErr := fmt.Errorf("could not dial VIC portlayer for attach: %v", err)
		log.Errorf(retErr.Error())
		return nil, nil, retErr
	}

	if tcpConn, ok := c.(*net.TCPConn); ok {
		tcpConn.SetKeepAlive(true)
		tcpConn.SetKeepAlivePeriod(30 * time.Second)
	}

	client := httputil.NewClientConn(c, nil)
	defer client.Close()

	req, err := http.NewRequest(method, "http://"+server, nil)
	if err != nil {
		retErr := fmt.Errorf("could not create request for VIC portlayer attach: %v", err)
		log.Errorf(retErr.Error())
		return nil, nil, retErr
	}

	//	req.Header.Set("Connection", "Upgrade")
	//	req.Header.Set("Upgrade", "tcp")
	req.Header.Set("Content-Type", "text/plain")

	// Make the connection and hijack it
	client.Do(req)
	conn, br := client.Hijack()

	return conn, br, nil
}

// Code copied from Docker and modified.  stdin, stdout, stderr are the client
// (hijacked connection) and conn, bufReader are the server
func AttachStreamsToConn(openStdin, stdinOnce, tty bool, stdin io.ReadCloser, stdout io.Writer, stderr io.Writer, conn net.Conn, bufReader *bufio.Reader, keys []byte) error {
	var (
		wg     sync.WaitGroup
		errors = make(chan error, 3)
	)

	if stdin != nil && openStdin {
		wg.Add(1)
	}

	if stdout != nil {
		wg.Add(1)
	}

	if stderr != nil {
		wg.Add(1)
	}

	// Connect stdin of container to the http conn.
	go func() {
		if stdin == nil || !openStdin {
			return
		}
		log.Debugf("attach: stdin: begin")

		var err error
		if tty {
			_, err = copyEscapable(conn, stdin, keys)
		} else {
			_, err = io.Copy(conn, stdin)
		}
		if err == io.ErrClosedPipe {
			err = nil
		}
		if err != nil {
			log.Printf("attach: stdin: %s\n", err)
			errors <- err
		}
		if stdinOnce && !tty {
			conn.Close()
		} else {
			// No matter what, when stdin is closed (io.Copy unblock), close stdout and stderr
			if stdout != nil {
				conn.Close()
			}
			if stderr != nil {
				conn.Close()
			}
		}
		log.Debugf("attach: stdin: end")
		wg.Done()
	}()

	attachStream := func(name string, stream io.Writer) {
		if stream == nil {
			return
		}

		log.Debugf("attach: %s: begin", name)
		_, err := io.Copy(stream, bufReader)
		if err == io.ErrClosedPipe {
			err = nil
		}
		if err != nil {
			log.Errorf("attach: %s: %v", name, err)
			errors <- err
		}
		// Make sure stdin gets closed
		if stdin != nil {
			stdin.Close()
		}
		conn.Close()
		log.Debugf("attach: %s: end", name)
		wg.Done()
	}

	go attachStream("stdout", stdout)
	go attachStream("stderr", stderr)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	<-done
	close(errors)
	for err := range errors {
		if err != nil {
			return err
		}
	}
	return nil
}

// Code copied from Docker and modified.  client streams belong to the hijacked
// connection and server streams belong to the tether ssh server
func AttachStreams(tty bool, clientStdin io.ReadCloser, clientStdout, clientStderr io.Writer, svrStdin io.WriteCloser, svrStdout io.Reader, svrStderr io.Reader, keys []byte) error {
	var (
		wg     sync.WaitGroup
		errors = make(chan error, 3)
	)

	if clientStdin != nil {
		wg.Add(1)
	}

	if clientStdout != nil {
		wg.Add(1)
	}

	if clientStderr != nil {
		wg.Add(1)
	}

	// Connect stdin of container to the http conn.
	go func() {
		if clientStdin == nil {
			return
		}
		log.Debugf("attach: stdin: begin")

		var err error
		if tty {
			_, err = copyEscapable(svrStdin, clientStdin, keys)
		} else {
			_, err = io.Copy(svrStdin, clientStdin)
		}
		if err == io.ErrClosedPipe {
			err = nil
		}
		if err != nil {
			log.Printf("attach: stdin: %s\n", err)
			errors <- err
		}
		if !tty {
			svrStdin.Close()
		}
		log.Debugf("attach: stdin: end")
		wg.Done()
	}()

	attachStream := func(name string, clientWriter io.Writer, svrReader io.Reader) {
		log.Debugf("attach: %s: begin", name)
		_, err := io.Copy(clientWriter, svrReader)
		if err == io.ErrClosedPipe {
			err = nil
		}
		if err != nil {
			log.Errorf("attach: %s: %v", name, err)
			errors <- err
		}
		// Make sure stdin gets closed
		if clientStdin != nil {
			clientStdin.Close()
		}
		log.Debugf("attach: %s: end", name)
		wg.Done()
	}

	go attachStream("stdout", clientStdout, svrStdout)
	go attachStream("stderr", clientStderr, svrStderr)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	<-done
	close(errors)
	for err := range errors {
		if err != nil {
			return err
		}
	}
	return nil
}

// Code c/c from io.Copy() modified to handle escape sequence
func copyEscapable(dst io.Writer, src io.ReadCloser, keys []byte) (written int64, err error) {
	if len(keys) == 0 {
		// Default keys : ctrl-p ctrl-q
		keys = []byte{16, 17}
	}
	buf := make([]byte, 32*1024)
	for {
		nr, er := src.Read(buf)
		if nr > 0 {
			// ---- Docker addition
			for i, key := range keys {
				if nr != 1 || buf[0] != key {
					break
				}
				if i == len(keys)-1 {
					if err := src.Close(); err != nil {
						return 0, err
					}
					return 0, nil
				}
				nr, er = src.Read(buf)
			}
			// ---- End of docker
			nw, ew := dst.Write(buf[0:nr])
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

// HijackConnection is a helper function to call from HTTP middleware that has
// access to the http response writer.  Docker derived.
func HijackConnection(rw http.ResponseWriter, r *http.Request) (io.ReadCloser, io.Writer, io.Writer, error) {
	_, upgrade := r.Header["Upgrade"]

	hijacker, ok := rw.(http.Hijacker)
	if !ok {
		return nil, nil, nil, fmt.Errorf("Failed to hijack connection")
	}

	conn, _, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, nil, err
	}

	// GetStreams hijacks the the connection from the http handler. This code originated
	// in the docker engine-api image router.
	if upgrade {
		fmt.Fprintf(conn, "HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.vmware.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
	} else {
		fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/vnd.vmware.raw-stream\r\n\r\n")
	}

	closer := func() error {
		return conn.Close()
	}

	return ioutils.NewReadCloserWrapper(conn, closer), conn, conn, nil
}
