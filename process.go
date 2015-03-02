package gardensystemd

import (
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/cloudfoundry-incubator/garden"
	"github.com/vito/garden-systemd/ginit"
)

type initProcess struct {
	processID uint32

	copying *sync.WaitGroup
	statusR *os.File
}

func attachProcess(
	processID uint32,
	processIO garden.ProcessIO,
	rights ginit.FDRights,
	fds []int,
) *initProcess {
	offsets := rights.Offsets()

	var status, stdin, stdout, stderr *os.File

	if offsets.Status != nil {
		status = os.NewFile(uintptr(fds[*offsets.Status]), "status")
	}

	if offsets.Stdin != nil {
		stdin = os.NewFile(uintptr(fds[*offsets.Stdin]), "stdin")
	}

	if offsets.Stdout != nil {
		stdout = os.NewFile(uintptr(fds[*offsets.Stdout]), "stdout")
	}

	if offsets.Stderr != nil {
		stderr = os.NewFile(uintptr(fds[*offsets.Stderr]), "stderr")
	}

	copying := new(sync.WaitGroup)

	if stdin != nil {
		// does not count towards copying; there may never be anything on
		// processIO.Stdin, which would block forever.
		go func() {
			io.Copy(stdin, processIO.Stdin)
			stdin.Close()
		}()
	}

	if stdout != nil {
		copying.Add(1)
		go func() {
			io.Copy(processIO.Stdout, stdout)
			stdout.Close()
			copying.Done()
		}()
	}

	if stderr != nil {
		copying.Add(1)
		go func() {
			io.Copy(processIO.Stderr, stderr)
			stderr.Close()
			copying.Done()
		}()
	}

	return &initProcess{
		processID: processID,

		copying: copying,
		statusR: status,
	}
}

func (p *initProcess) ID() uint32 {
	return p.processID
}

func (p *initProcess) Wait() (int, error) {
	p.copying.Wait()

	var status int
	_, err := fmt.Fscanf(p.statusR, "%d", &status)
	if err != nil {
		return 0, err
	}

	return status, nil
}

func (p *initProcess) SetTTY(spec garden.TTYSpec) error {
	return fmt.Errorf("SetTTY not implemented")
}

func (p *initProcess) Signal(sig garden.Signal) error {
	return fmt.Errorf("Signal not implemented")
}
