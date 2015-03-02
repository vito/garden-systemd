package process_tracker

import (
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"syscall"

	"github.com/cloudfoundry-incubator/garden"
)

type Process struct {
	id uint32

	containerPath string

	exited     chan struct{}
	exitStatus int
	exitErr    error

	stdin  *faninWriter
	stdout *fanoutWriter
	stderr *fanoutWriter
}

func NewProcess(id uint32, containerPath string) *Process {
	return &Process{
		id: id,

		containerPath: containerPath,

		exited: make(chan struct{}),

		stdin:  &faninWriter{hasSink: make(chan struct{})},
		stdout: &fanoutWriter{},
		stderr: &fanoutWriter{},
	}
}

func (p *Process) ID() uint32 {
	return p.id
}

func (p *Process) Wait() (int, error) {
	<-p.exited
	return p.exitStatus, p.exitErr
}

func (p *Process) SetTTY(tty garden.TTYSpec) error {
	//if tty.WindowSize != nil {
	//return p.link.SetWindowSize(tty.WindowSize.Columns, tty.WindowSize.Rows)
	//}

	return nil
}

func (p *Process) Spawn(spec garden.ProcessSpec, processIO garden.ProcessIO) error {
	wshPath := filepath.Join(p.containerPath, "bin", "wsh")
	wshdSock := path.Join(p.containerPath, "run", "wshd.sock")

	wshArgs := []string{
		"-socket", wshdSock,
	}

	if spec.User != "" {
		spec.Env = append(spec.Env, "USER="+spec.User)
	}

	// set up a basic $PATH
	if spec.Privileged {
		spec.Env = append(
			spec.Env,
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		)
	} else {
		spec.Env = append(
			spec.Env,
			"PATH=/usr/local/bin:/usr/bin:/bin",
		)
	}

	if spec.Dir != "" {
		wshArgs = append(wshArgs, "--dir", spec.Dir)
	}

	if spec.TTY != nil {
		wshArgs = append(wshArgs, "-tty")

		if spec.TTY.WindowSize != nil {
			wshArgs = append(
				wshArgs,
				fmt.Sprintf("-windowColumns=%d", spec.TTY.WindowSize.Columns),
				fmt.Sprintf("-windowRows=%d", spec.TTY.WindowSize.Rows),
			)
		}
	}

	wshArgs = append(wshArgs, "--", spec.Path)
	wshArgs = append(wshArgs, spec.Args...)

	spawn := exec.Command(wshPath, wshArgs...)

	spawn.Env = spec.Env
	spawn.Stdin = processIO.Stdin
	spawn.Stdout = processIO.Stdout
	spawn.Stderr = processIO.Stderr
	spawn.SysProcAttr = &syscall.SysProcAttr{
		Pdeathsig: syscall.SIGKILL,
	}

	err := spawn.Start()
	if err != nil {
		return err
	}

	go func() {
		state, err := spawn.Process.Wait()
		if err != nil {
			p.completed(-1, err)
		} else {
			p.completed(state.Sys().(syscall.WaitStatus).ExitStatus(), nil)
		}
	}()

	return nil
}

func (p *Process) Link() {
	//p.runningLink.Do(p.runLinker)
}

func (p *Process) Attach(processIO garden.ProcessIO) {
	if processIO.Stdin != nil {
		p.stdin.AddSource(processIO.Stdin)
	}

	if processIO.Stdout != nil {
		p.stdout.AddSink(processIO.Stdout)
	}

	if processIO.Stderr != nil {
		p.stderr.AddSink(processIO.Stderr)
	}
}

func (p *Process) Signal(signal garden.Signal) error {
	//select {
	//case <-p.linked:
	//return p.link.SendSignal(signal)
	//default:
	//return nil
	//}
	return nil
}

func (p *Process) completed(exitStatus int, err error) {
	p.exitStatus = exitStatus
	p.exitErr = err
	close(p.exited)
}
