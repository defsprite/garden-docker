package container_daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/cloudfoundry-incubator/garden"
	"github.com/cloudfoundry-incubator/garden-linux/container_daemon/unix_socket"
	"github.com/cloudfoundry-incubator/garden-linux/containerizer/system"
)

//go:generate counterfeiter -o fake_listener/FakeListener.go . Listener
type Listener interface {
	Init() error
	Listen(ch unix_socket.ConnectionHandler) error
	Stop() error
}

//go:generate counterfeiter -o fake_runner/fake_runner.go . Runner
type Runner interface {
	Start(cmd *exec.Cmd) error
	Wait(cmd *exec.Cmd) (byte, error)
}

type ContainerDaemon struct {
	Listener Listener
	Users    system.User
	Runner   Runner
}

// This method should be called from the host namespace, to open the socket file in the right file system.
func (cd *ContainerDaemon) Init() error {
	if err := cd.Listener.Init(); err != nil {
		return fmt.Errorf("container_daemon: initializing the listener: %s", err)
	}

	return nil
}

func (cd *ContainerDaemon) Run() error {
	if err := cd.Listener.Listen(cd); err != nil {
		return fmt.Errorf("container_daemon: listening for connections: %s", err)
	}

	return nil
}

func (cd *ContainerDaemon) Handle(decoder *json.Decoder) ([]*os.File, error) {
	var spec garden.ProcessSpec
	err := decoder.Decode(&spec)
	if err != nil {
		return nil, fmt.Errorf("container_daemon: Decode failed: %s", err)
	}

	var pipes [4]struct {
		r *os.File
		w *os.File
	}

	// Create four pipes for stdin, stdout, stderr, and the exit status.
	for i := 0; i < 4; i++ {
		pipes[i].r, pipes[i].w, err = os.Pipe()
		if err != nil {
			return nil, fmt.Errorf("container_daemon: Failed to create pipe: %s", err)
		}
	}

	var uid, gid uint32
	if user, err := cd.Users.Lookup(spec.User); err == nil && user != nil {
		fmt.Sscanf(user.Uid, "%d", &uid) // todo(jz): handle errors
		fmt.Sscanf(user.Gid, "%d", &gid)
	} else if err == nil {
		return nil, fmt.Errorf("container_daemon: failed to lookup user %s", spec.User)
	} else {
		return nil, fmt.Errorf("container_daemon: lookup user %s: %s", spec.User, err)
	}

	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid: uid,
			Gid: gid,
		},
	}

	cmd.Stdin = pipes[0].r
	cmd.Stdout = pipes[1].w
	cmd.Stderr = pipes[2].w

	stdinW := pipes[0].w
	stdoutR := pipes[1].r
	stderrR := pipes[2].r
	exitStatusR := pipes[3].r

	if err := cd.Runner.Start(cmd); err != nil {
		return nil, fmt.Errorf("container_daemon: running command: %s", err)
	}

	go reportExitStatus(cd.Runner, cmd, pipes[3].w, pipes[2].w, func() {
		pipes[0].r.Close() // Ignore error
		for i := 1; i <= 3; i++ {
			pipes[i].w.Close() // Ignore error
		}
	})

	return []*os.File{stdinW, stdoutR, stderrR, exitStatusR}, nil
}

func reportExitStatus(runner Runner, cmd *exec.Cmd, exitWriter, errWriter *os.File, tidyUp func()) {
	defer tidyUp()
	exitStatus, err := runner.Wait(cmd)
	if err != nil {
		exitStatus = UnknownExitStatus
		tryToReportErrorf(errWriter, "container_daemon: Wait failed: %s", err)
	}

	_, err = exitWriter.Write([]byte{exitStatus})
	if err != nil {
		tryToReportErrorf(errWriter, "container_daemon: failed to Write exit status: %s", err)
	}
}

func tryToReportErrorf(errWriter *os.File, format string, inserts ...interface{}) {
	message := fmt.Sprintf(format, inserts)
	errWriter.Write([]byte(message)) // Ignore error - nothing to do.
}

func (cd *ContainerDaemon) Stop() error {
	if err := cd.Listener.Stop(); err != nil {
		return fmt.Errorf("container_daemon: stopping the listener: %s", err)
	}

	return nil
}
