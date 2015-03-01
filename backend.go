package gardensystemd

import (
	"bytes"
	"errors"
	"fmt"
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

func (backend *Backend) GraceTime(garden.Container) time.Duration {
	return 5 * time.Minute
}

func (backend *Backend) Ping() error {
	return nil
}

func (backend *Backend) Capacity() (garden.Capacity, error) {
	println("NOT IMPLEMENTED: Capacity")
	return garden.Capacity{}, nil
}

func (backend *Backend) Create(spec garden.ContainerSpec) (garden.Container, error) {
	id := backend.generateContainerID()

	if spec.Handle == "" {
		spec.Handle = id
	}

	rootfs, err := url.Parse(spec.RootFSPath)
	if err != nil {
		return nil, err
	}

	var image string
	switch rootfs.Scheme {
	case "docker":
		dockerIndex := "https://index.docker.io"
		if rootfs.Host != "" {
			dockerIndex = "https://" + rootfs.Host
		}

		tag := "latest"
		if rootfs.Fragment != "" {
			tag = rootfs.Fragment
		}

		var repo string
		pathSegs := strings.Split(rootfs.Path, "/")
		if len(pathSegs) == 0 {
			return nil, fmt.Errorf("invalid docker uri")
		}

		// drop leading /
		pathSegs = pathSegs[1:]
		if len(pathSegs) == 1 {
			repo = "library/" + pathSegs[0]
		} else {
			repo = strings.Join(pathSegs, "/")
		}

		err := run(exec.Command(
			"machinectl",
			"pull-dkr",
			repo+":"+tag,
			"--force",
			"--verify", "no",
			"--dkr-index-url", dockerIndex,
		))
		if err != nil {
			return nil, err
		}

		image = filepath.Base(rootfs.Path)
	default:
		return nil, fmt.Errorf("unsupported rootfs scheme: %s", rootfs.Scheme)
	}

	dir := filepath.Join(backend.containersDir, "container-"+id)

	container := newContainer(spec, dir, id)

	err = os.MkdirAll(dir, 0755)
	if err != nil {
		return nil, err
	}

	runDir := filepath.Join(dir, "run")
	binDir := filepath.Join(dir, "bin")

	if err := os.MkdirAll(runDir, 0755); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(binDir, 0755); err != nil {
		return nil, err
	}

	err = run(exec.Command("machinectl", "clone", image, id))
	if err != nil {
		return nil, err
	}

	// clear out any existing resolv.conf
	err = os.RemoveAll(filepath.Join("/var/lib/machines", id, "etc", "resolv.conf"))
	if err != nil {
		return nil, err
	}

	err = run(exec.Command("cp", "-a", filepath.Join(backend.skeletonDir, "bin", "wsh"), filepath.Join(binDir, "wsh")))
	if err != nil {
		if err := run(exec.Command("machinectl", "remove", id)); err != nil {
			log.Println("failed to cleanup image:", err)
		}

		return nil, err
	}

	err = run(exec.Command("cp", "-a", filepath.Join(backend.skeletonDir, "bin", "iodaemon"), filepath.Join(binDir, "iodaemon")))
	if err != nil {
		if err := run(exec.Command("machinectl", "remove", id)); err != nil {
			log.Println("failed to cleanup image:", err)
		}

		return nil, err
	}

	err = run(exec.Command("cp", "-a", filepath.Join(backend.skeletonDir, "bin", "wshd"), filepath.Join("/var/lib/machines", id, "sbin", "wshd")))
	if err != nil {
		if err := run(exec.Command("machinectl", "remove", id)); err != nil {
			log.Println("failed to cleanup image:", err)
		}

		return nil, err
	}

	err = run(exec.Command("systemctl", "start", "garden-container@"+id))
	if err != nil {
		if err := run(exec.Command("machinectl", "remove", id)); err != nil {
			log.Println("failed to cleanup image:", err)
		}

		return nil, err
	}

	backend.containersL.Lock()
	backend.containers[spec.Handle] = container
	backend.containersL.Unlock()

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

	for i := 0; i < 10; i++ {
		err = run(exec.Command("machinectl", "remove", container.id))
		if err == nil {
			break
		}

		time.Sleep(time.Second)
	}

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

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Printf("command failed: %v\n\nstdout: %s\n\nstderr: %s\n", cmd.Args, outBuf.String(), errBuf.String())
		return err
	}

	return nil
}
