package gardensystemd

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"code.cloudfoundry.org/garden"
	"github.com/vito/garden-systemd/ginit"
)

type UndefinedPropertyError struct {
	Key string
}

func (err UndefinedPropertyError) Error() string {
	return fmt.Sprintf("property does not exist: %s", err.Key)
}

type container struct {
	id string

	dir string

	handle string

	properties  garden.Properties
	propertiesL sync.RWMutex

	env []string

	graceTime  time.Duration
	graceTimeL sync.RWMutex
}

func newContainer(spec garden.ContainerSpec, dir string, id string) *container {
	if spec.Properties == nil {
		spec.Properties = garden.Properties{}
	}

	if spec.GraceTime == 0 {
		spec.GraceTime = 5 * time.Minute
	}

	return &container{
		id: id,

		dir: dir,

		handle: spec.Handle,

		properties: spec.Properties,

		env: spec.Env,

		graceTime: spec.GraceTime,
	}
}

func (container *container) Handle() string {
	return container.handle
}

func (container *container) Stop(kill bool) error {
	var signal string
	if kill {
		signal = "SIGKILL"
	} else {
		signal = "SIGTERM"
	}

	return run(exec.Command("machinectl", "kill", "-s", signal, container.id))
}

func (container *container) Info() (garden.ContainerInfo, error) { return garden.ContainerInfo{}, nil }

func (container *container) StreamIn(spec garden.StreamInSpec) error {
	destDir := strings.TrimRight(spec.Path, "/")

	streamDir, err := ioutil.TempDir(container.dir, "stream-in")
	if err != nil {
		return err
	}

	defer os.RemoveAll(streamDir)

	tarCmd := exec.Command("tar", "xf", "-", "-C", streamDir)
	tarCmd.Stdin = spec.TarStream

	err = run(tarCmd)
	if err != nil {
		return err
	}

	wshdSock := path.Join(container.dir, "run", "wshd.sock")

	conn, err := net.Dial("unix", wshdSock)
	if err != nil {
		println("dial wshd: " + err.Error())
		return err
	}

	defer conn.Close()

	enc := gob.NewEncoder(conn)

	err = enc.Encode(ginit.Request{
		CreateDir: &ginit.CreateDirRequest{
			Path: destDir,
		},
	})
	if err != nil {
		println("run request: " + err.Error())
		return err
	}

	var response ginit.Response
	err = gob.NewDecoder(conn).Decode(&response)
	if err != nil {
		println("decode response: " + err.Error())
		return err
	}

	if response.Error != nil {
		err := fmt.Errorf("remote error: %s", *response.Error)
		println(err.Error())
		return err
	}

	return run(exec.Command("machinectl", "copy-to", container.id, streamDir, destDir))
}

func (container *container) StreamOut(spec garden.StreamOutSpec) (io.ReadCloser, error) {
	if strings.HasSuffix(spec.Path, "/") {
		spec.Path += "."
	}

	streamDirBase, err := ioutil.TempDir(container.dir, "stream-out")
	if err != nil {
		return nil, err
	}

	// do NOT use path.Join; it strips out '/.'
	streamDir := streamDirBase + "/" + path.Base(spec.Path)

	err = run(exec.Command("machinectl", "copy-from", container.id, spec.Path, streamDir))
	if err != nil {
		return nil, err
	}

	workingDir := path.Dir(streamDir)
	compressArg := path.Base(streamDir)

	tarCmd := exec.Command("tar", "cf", "-", compressArg)
	tarCmd.Dir = workingDir

	out, err := tarCmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	tarCmd.Stderr = os.Stderr

	err = tarCmd.Start()
	if err != nil {
		return nil, err
	}

	return waitCloser{
		ReadCloser: out,
		proc:       tarCmd,
		tmpdir:     streamDirBase,
	}, nil
}

type waitCloser struct {
	io.ReadCloser
	proc   *exec.Cmd
	tmpdir string
}

func (c waitCloser) Close() error {
	defer os.RemoveAll(c.tmpdir)

	err := c.Close()
	if err != nil {
		return err
	}

	return c.proc.Wait()
}

func (container *container) LimitBandwidth(limits garden.BandwidthLimits) error { return nil }

func (container *container) CurrentBandwidthLimits() (garden.BandwidthLimits, error) {
	return garden.BandwidthLimits{}, nil
}

func (container *container) LimitCPU(limits garden.CPULimits) error { return nil }

func (container *container) CurrentCPULimits() (garden.CPULimits, error) {
	return garden.CPULimits{}, nil
}

func (container *container) LimitDisk(limits garden.DiskLimits) error { return nil }

func (container *container) CurrentDiskLimits() (garden.DiskLimits, error) {
	return garden.DiskLimits{}, nil
}

func (container *container) LimitMemory(limits garden.MemoryLimits) error { return nil }

func (container *container) CurrentMemoryLimits() (garden.MemoryLimits, error) {
	return garden.MemoryLimits{}, nil
}

func (container *container) NetIn(hostPort, containerPort uint32) (uint32, uint32, error) {
	return 0, 0, nil
}

func (container *container) NetOut(garden.NetOutRule) error { return nil }

func (container *container) BulkNetOut([]garden.NetOutRule) error { return nil }

func (container *container) Run(spec garden.ProcessSpec, processIO garden.ProcessIO) (garden.Process, error) {
	if spec.User == "" {
		spec.User = "root"
	}

	wshdSock := path.Join(container.dir, "run", "wshd.sock")

	conn, err := net.Dial("unix", wshdSock)
	if err != nil {
		println("dial wshd: " + err.Error())
		return nil, err
	}

	defer conn.Close()

	enc := gob.NewEncoder(conn)

	runRequest := &ginit.RunRequest{
		Path: spec.Path,
		Args: spec.Args,
		Dir:  spec.Dir,
		Env:  append(container.env, spec.Env...),
		User: spec.User,
	}

	if spec.TTY != nil {
		runRequest.TTY = &ginit.TTYSpec{}

		if spec.TTY.WindowSize != nil {
			runRequest.TTY.Columns = spec.TTY.WindowSize.Columns
			runRequest.TTY.Rows = spec.TTY.WindowSize.Rows
		} else {
			runRequest.TTY.Columns = 80
			runRequest.TTY.Rows = 24
		}
	}

	err = enc.Encode(ginit.Request{
		Run: runRequest,
	})
	if err != nil {
		println("run request: " + err.Error())
		return nil, err
	}

	var b [2048]byte
	var oob [2048]byte

	n, oobn, _, _, err := conn.(*net.UnixConn).ReadMsgUnix(b[:], oob[:])
	if err != nil {
		println("read unix msg: " + err.Error())
		return nil, err
	}

	var response ginit.Response
	err = gob.NewDecoder(bytes.NewBuffer(b[:n])).Decode(&response)
	if err != nil {
		println("decode response: " + err.Error())
		return nil, err
	}

	if response.Error != nil {
		err := fmt.Errorf("remote error: %s", *response.Error)
		println(err.Error())
		return nil, err
	}

	scms, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		println("parse socket control msg: " + err.Error())
		return nil, err
	}

	if len(scms) < 1 {
		err := fmt.Errorf("no socket control messages received")
		println(err.Error())
		return nil, err
	}

	scm := scms[0]

	fds, err := syscall.ParseUnixRights(&scm)
	if err != nil {
		println("parse unix rights: " + err.Error())
		return nil, err
	}

	return attachProcess(
		response.Run.ProcessID,
		processIO,
		response.Run.Rights,
		fds,
		wshdSock,
	), nil
}

func (container *container) Attach(processID string, processIO garden.ProcessIO) (garden.Process, error) {
	wshdSock := path.Join(container.dir, "run", "wshd.sock")

	conn, err := net.Dial("unix", wshdSock)
	if err != nil {
		println("dial wshd: " + err.Error())
		return nil, err
	}

	defer conn.Close()

	enc := gob.NewEncoder(conn)

	err = enc.Encode(ginit.Request{
		Attach: &ginit.AttachRequest{
			ProcessID: processID,
		},
	})
	if err != nil {
		println("attach request: " + err.Error())
		return nil, err
	}

	var b [2048]byte
	var oob [2048]byte

	n, oobn, _, _, err := conn.(*net.UnixConn).ReadMsgUnix(b[:], oob[:])
	if err != nil {
		println("read unix msg: " + err.Error())
		return nil, err
	}

	var response ginit.Response
	err = gob.NewDecoder(bytes.NewBuffer(b[:n])).Decode(&response)
	if err != nil {
		println("decode response: " + err.Error())
		return nil, err
	}

	if response.Error != nil {
		err := fmt.Errorf("remote error: %s", *response.Error)
		println(err.Error())
		return nil, err
	}

	scms, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		println("parse socket control msg: " + err.Error())
		return nil, err
	}

	if len(scms) < 1 {
		err := fmt.Errorf("no socket control messages received")
		println(err.Error())
		return nil, err
	}

	scm := scms[0]

	fds, err := syscall.ParseUnixRights(&scm)
	if err != nil {
		println("parse unix rights: " + err.Error())
		return nil, err
	}

	return attachProcess(
		processID,
		processIO,
		response.Attach.Rights,
		fds,
		wshdSock,
	), nil
}

func (container *container) Properties() (garden.Properties, error) {
	props := garden.Properties{}

	container.propertiesL.RLock()
	for n, v := range container.properties {
		props[n] = v
	}
	container.propertiesL.RUnlock()

	return props, nil
}

func (container *container) Property(name string) (string, error) {
	container.propertiesL.RLock()
	property, found := container.properties[name]
	container.propertiesL.RUnlock()

	if !found {
		return "", UndefinedPropertyError{name}
	}

	return property, nil
}

func (container *container) SetProperty(name string, value string) error {
	container.propertiesL.Lock()
	container.properties[name] = value
	container.propertiesL.Unlock()

	return nil
}

func (container *container) RemoveProperty(name string) error {
	container.propertiesL.Lock()
	defer container.propertiesL.Unlock()

	_, found := container.properties[name]
	if !found {
		return UndefinedPropertyError{name}
	}

	delete(container.properties, name)

	return nil
}

func (container *container) Metrics() (garden.Metrics, error) {
	return garden.Metrics{}, nil
}

func (container *container) SetGraceTime(graceTime time.Duration) error {
	container.graceTimeL.Lock()
	container.graceTime = graceTime
	container.graceTimeL.Unlock()
	return nil
}

func (container *container) currentProperties() garden.Properties {
	properties := garden.Properties{}

	container.propertiesL.RLock()

	for k, v := range container.properties {
		properties[k] = v
	}

	container.propertiesL.RUnlock()

	return properties
}

func (container *container) currentGraceTime() time.Duration {
	container.graceTimeL.RLock()
	defer container.graceTimeL.RUnlock()
	return container.graceTime
}
