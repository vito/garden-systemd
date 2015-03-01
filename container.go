package gardensystemd

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cloudfoundry-incubator/garden"
	"github.com/vito/garden-systemd/process_tracker"
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

	processTracker process_tracker.ProcessTracker
}

func newContainer(spec garden.ContainerSpec, dir string, id string) *container {
	if spec.Properties == nil {
		spec.Properties = garden.Properties{}
	}

	return &container{
		id: id,

		dir: dir,

		handle: spec.Handle,

		properties: spec.Properties,

		env: spec.Env,

		processTracker: process_tracker.New(dir),
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

func (container *container) StreamIn(dstPath string, tarStream io.Reader) error {
	streamDir, err := ioutil.TempDir(container.dir, "stream-in")
	if err != nil {
		return err
	}

	tarCmd := exec.Command("tar", "xf", "-", "-C", streamDir)
	tarCmd.Stdin = tarStream

	err = run(tarCmd)
	if err != nil {
		return err
	}

	// TODO: can't use copy-to because it doesn't make the dest dir.
	// use 'bind' even though it permits the container writing to the host...
	// for now.

	return run(exec.Command("machinectl", "bind", "--mkdir", container.id, streamDir, dstPath))
}

func (container *container) StreamOut(srcPath string) (io.ReadCloser, error) {
	streamDirBase, err := ioutil.TempDir(container.dir, "stream-out")
	if err != nil {
		return nil, err
	}

	// do NOT use path.Join; it strips out '/.'
	streamDir := streamDirBase + "/" + path.Base(srcPath)

	err = run(exec.Command("machinectl", "copy-from", container.id, srcPath, streamDir))
	if err != nil {
		return nil, err
	}

	workingDir := path.Dir(streamDir)
	compressArg := path.Base(streamDir)
	if strings.HasSuffix(srcPath, "/") {
		workingDir = streamDir
		compressArg = "."
	}

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
	}, nil
}

type waitCloser struct {
	io.ReadCloser
	proc *exec.Cmd
}

func (c waitCloser) Close() error {
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

func (container *container) Run(spec garden.ProcessSpec, processIO garden.ProcessIO) (garden.Process, error) {
	wshPath := filepath.Join(container.dir, "bin", "wsh")
	sockPath := filepath.Join(container.dir, "run", "wshd.sock")

	user := "root"

	args := []string{
		"--socket", sockPath,
		"--user", user,
	}

	for _, e := range spec.Env {
		args = append(args, "--env", e)
	}

	if spec.Dir != "" {
		args = append(args, "--dir", spec.Dir)
	}

	args = append(args, spec.Path)
	args = append(args, spec.Args...)

	cmd := exec.Command(wshPath, args...)

	return container.processTracker.Run(cmd, processIO, spec.TTY)
}

func (container *container) Attach(processID uint32, processIO garden.ProcessIO) (garden.Process, error) {
	return container.processTracker.Attach(processID, processIO)
}

func (container *container) GetProperty(name string) (string, error) {
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

func (container *container) currentProperties() garden.Properties {
	properties := garden.Properties{}

	container.propertiesL.RLock()

	for k, v := range container.properties {
		properties[k] = v
	}

	container.propertiesL.RUnlock()

	return properties
}
