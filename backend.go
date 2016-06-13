package gardensystemd

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudfoundry-incubator/garden"
)

var (
	ErrContainerNotFound = errors.New("container not found")
)

type Backend struct {
	containersDir string
	skeletonDir   string

	containers  map[string]*container
	containersL sync.RWMutex

	containerNum uint64
}

func NewBackend(containersDir string, skeletonDir string) *Backend {
	return &Backend{
		containersDir: containersDir,
		skeletonDir:   skeletonDir,

		containers: make(map[string]*container),

		containerNum: uint64(time.Now().UnixNano()),
	}
}

func (backend *Backend) Start() error {
	err := os.MkdirAll(backend.containersDir, 0755)
	if err != nil {
		return err
	}

	return run(exec.Command(
		"systemctl",
		"link",
		filepath.Join(backend.skeletonDir, "garden-container@.service"),
	))
}

func (backend *Backend) Stop() {
	containers, _ := backend.Containers(nil)

	for _, container := range containers {
		backend.Destroy(container.Handle())
	}
}

func (backend *Backend) GraceTime(c garden.Container) time.Duration {
	return c.(*container).currentGraceTime()
}

func (backend *Backend) Ping() error {
	return nil
}

func (backend *Backend) Capacity() (garden.Capacity, error) {
	println("NOT IMPLEMENTED: Capacity")
	return garden.Capacity{}, nil
}

var ErrNoRootFS = errors.New("no rootfs path specified")

func (backend *Backend) Create(spec garden.ContainerSpec) (garden.Container, error) {
	if spec.RootFSPath == "" {
		return nil, ErrNoRootFS
	}

	id := backend.generateContainerID()

	if spec.Handle == "" {
		spec.Handle = id
	}

	dir := filepath.Join(backend.containersDir, "container-"+id)

	container := newContainer(spec, dir, id)

	err := os.MkdirAll(dir, 0755)
	if err != nil {
		return nil, err
	}

	nspawnFlags := []string{}

	for _, mount := range spec.BindMounts {
		switch mount.Mode {
		case garden.BindMountModeRO:
			nspawnFlags = append(nspawnFlags, "--bind-ro", mount.SrcPath+":"+mount.DstPath)
		case garden.BindMountModeRW:
			nspawnFlags = append(nspawnFlags, "--bind", mount.SrcPath+":"+mount.DstPath)
		}
	}

	rootfsURL, err := url.Parse(spec.RootFSPath)
	if err != nil {
		return nil, fmt.Errorf("invalid rootfs URI: %s", spec.RootFSPath)
	}

	if rootfsURL.Scheme != "raw" {
		return nil, fmt.Errorf("unsupported rootfs URI (only raw:// supported): %s", spec.RootFSPath)
	}

	start := fmt.Sprintf(
		`#!/bin/sh

exec /usr/bin/systemd-nspawn \
	--capability all \
	--machine %[1]s \
	--directory '%[2]s' \
	--ephemeral \
	--quiet \
	--keep-unit \
	--bind /var/lib/garden/container-%[1]s/tmp:/tmp \
	--bind /var/lib/garden/container-%[1]s/run:/tmp/garden-init \
	--bind /var/lib/garden/container-%[1]s/bin/wshd:/sbin/wshd \
	%[3]s \
	-- /sbin/wshd --run /tmp/garden-init`,
		id,
		rootfsURL.Path,
		strings.Join(nspawnFlags, " "),
	)

	err = ioutil.WriteFile(filepath.Join(dir, "start"), []byte(start), 0755)
	if err != nil {
		return nil, err
	}

	runDir := filepath.Join(dir, "run")
	binDir := filepath.Join(dir, "bin")
	tmpDir := filepath.Join(dir, "tmp")

	if err := os.MkdirAll(runDir, 0755); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(binDir, 0755); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(tmpDir, 0777); err != nil {
		return nil, err
	}

	err = run(exec.Command("cp", "-a", filepath.Join(backend.skeletonDir, "bin", "wshd"), filepath.Join(binDir, "wshd")))
	if err != nil {
		return nil, err
	}

	// clear out any existing resolv.conf
	err = os.RemoveAll(filepath.Join(rootfsURL.Path, "etc", "resolv.conf"))
	if err != nil {
		return nil, err
	}

	// create sbin/wshd in rootfs to fool nspawn validation
	sbinDir := filepath.Join(rootfsURL.Path, "sbin")
	err = os.MkdirAll(sbinDir, 0755)
	if err != nil {
		return nil, err
	}

	wshdIsh, err := os.Create(filepath.Join(sbinDir, "wshd"))
	if err != nil {
		return nil, err
	}

	wshdIsh.Close()

	err = run(exec.Command("systemctl", "start", "garden-container@"+id))
	if err != nil {
		return nil, err
	}

	created := false

	defer func() {
		if !created {
			if err := run(exec.Command("systemctl", "stop", "garden-container@"+id)); err != nil {
				log.Println("failed to cleanup container:", err)
			}
		}
	}()

	for i := 0; i < 10; i++ {
		err = run(exec.Command("machinectl", "status", id))
		if err == nil {
			break
		}

		time.Sleep(time.Second)
	}

	if err != nil {
		return nil, fmt.Errorf("container did not come up")
	}

	backend.containersL.Lock()
	backend.containers[spec.Handle] = container
	backend.containersL.Unlock()

	created = true

	return container, nil
}

func (backend *Backend) Destroy(handle string) error {
	backend.containersL.RLock()
	container, found := backend.containers[handle]
	backend.containersL.RUnlock()

	if !found {
		return ErrContainerNotFound
	}

	err := run(exec.Command("systemctl", "stop", "garden-container@"+container.id))
	if err != nil {
		return err
	}

	err = os.RemoveAll(container.dir)
	if err != nil {
		return err
	}

	backend.containersL.Lock()
	delete(backend.containers, handle)
	backend.containersL.Unlock()

	return nil
}

func (backend *Backend) Containers(filter garden.Properties) ([]garden.Container, error) {
	matchingContainers := []garden.Container{}

	backend.containersL.RLock()

	for _, container := range backend.containers {
		if containerHasProperties(container, filter) {
			matchingContainers = append(matchingContainers, container)
		}
	}

	backend.containersL.RUnlock()

	return matchingContainers, nil
}

func (backend *Backend) Lookup(handle string) (garden.Container, error) {
	backend.containersL.RLock()
	container, found := backend.containers[handle]
	backend.containersL.RUnlock()

	if !found {
		return nil, ErrContainerNotFound
	}

	return container, nil
}

func (backend *Backend) BulkInfo(handles []string) (map[string]garden.ContainerInfoEntry, error) {
	return map[string]garden.ContainerInfoEntry{}, nil
}

func (backend *Backend) BulkMetrics(handles []string) (map[string]garden.ContainerMetricsEntry, error) {
	return map[string]garden.ContainerMetricsEntry{}, nil
}

func (backend *Backend) generateContainerID() string {
	containerNum := atomic.AddUint64(&backend.containerNum, 1)

	containerID := []byte{}

	var i uint64
	for i = 0; i < 11; i++ {
		containerID = strconv.AppendUint(
			containerID,
			(containerNum>>(55-(i+1)*5))&31,
			32,
		)
	}

	return string(containerID)
}

func containerHasProperties(container *container, properties garden.Properties) bool {
	containerProps := container.currentProperties()

	for key, val := range properties {
		cval, ok := containerProps[key]
		if !ok {
			return false
		}

		if cval != val {
			return false
		}
	}

	return true
}

func run(cmd *exec.Cmd) error {
	outBuf := new(bytes.Buffer)
	errBuf := new(bytes.Buffer)

	cmd.Stdout = outBuf
	cmd.Stderr = errBuf
	if err := cmd.Run(); err != nil {
		log.Printf("command failed: %v\n\nstdout: %s\n\nstderr: %s\n", cmd.Args, outBuf.String(), errBuf.String())
		return err
	}

	return nil
}
