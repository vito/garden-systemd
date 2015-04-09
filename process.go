package gardensystemd

import (
	"encoding/gob"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"

	"github.com/cloudfoundry-incubator/garden"
	"github.com/vito/garden-systemd/ginit"
)

type initProcess struct {
	processID uint32

	copying *sync.WaitGroup
	statusR *os.File

	wshdSock string
}

func attachProcess(
	processID uint32,
	processIO garden.ProcessIO,
	rights ginit.FDRights,
	fds []int,
	wshdSock string,
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

		wshdSock: wshdSock,
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
	conn, err := net.Dial("unix", p.wshdSock)
	if err != nil {
		println("dial wshd: " + err.Error())
		return err
	}

	defer conn.Close()

	enc := gob.NewEncoder(conn)

	var columns, rows int
	if spec.WindowSize != nil {
		columns = spec.WindowSize.Columns
		rows = spec.WindowSize.Rows
	}

	err = enc.Encode(ginit.Request{
		SetWindowSize: &ginit.SetWindowSizeRequest{
			ProcessID: p.processID,
			Columns:   columns,
			Rows:      rows,
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

	return nil
}

func (p *initProcess) Signal(sig garden.Signal) error {
	var signal os.Signal
	switch sig {
	case garden.SignalTerminate:
		signal = syscall.SIGTERM
	default:
		signal = syscall.SIGKILL
	}

	conn, err := net.Dial("unix", p.wshdSock)
	if err != nil {
		println("dial wshd: " + err.Error())
		return err
	}

	defer conn.Close()

	enc := gob.NewEncoder(conn)

	err = enc.Encode(ginit.Request{
		Signal: &ginit.SignalRequest{
			ProcessID: p.processID,
			Signal:    signal,
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

	return nil
}
