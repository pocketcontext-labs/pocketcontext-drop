// Inside the disposable Firecracker guest only. Start the command rendered by
// Drop and relay its HTTP port and private startup output over the VM's vsock.
package main

import (
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func serve(port uint32, handle func(*os.File)) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		panic(err)
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}); err != nil {
		panic(err)
	}
	if err := unix.Listen(fd, 16); err != nil {
		panic(err)
	}
	for {
		next, _, err := unix.Accept(fd)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			panic(err)
		}
		go func() { f := os.NewFile(uintptr(next), "vsock"); defer f.Close(); handle(f) }()
	}
}
func main() {
	var i struct {
		Config struct {
			HTTPPort int
			Command  struct {
				Program, WorkingDirectory string
				Args                      []string
				Environment               map[string]string
			}
		}
	}
	input, err := os.ReadFile("/drop-instance.json")
	if err != nil {
		panic(err)
	}
	if err = json.Unmarshal(input, &i); err != nil {
		panic(err)
	}
	cmd := exec.Command(i.Config.Command.Program, i.Config.Command.Args...)
	cmd.Dir = i.Config.Command.WorkingDirectory
	for k, v := range i.Config.Command.Environment {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	log, err := os.OpenFile("/tmp/drop-app.log", os.O_TRUNC|os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		panic(err)
	}
	cmd.Stdout = log
	cmd.Stderr = log
	if err = cmd.Start(); err != nil {
		panic(err)
	}
	go func() { _ = cmd.Wait(); fmt.Println("fixture app stopped") }()
	go serve(10001, func(f *os.File) {
		log, err := os.Open("/tmp/drop-app.log")
		if err == nil {
			defer log.Close()
			_, _ = io.CopyN(f, log, 65536)
		}
	})
	go serve(10002, func(f *os.File) {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		time.Sleep(300 * time.Millisecond)
		unix.Sync()
		_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
	})
	serve(10000, func(f *os.File) {
		conn, err := net.DialTimeout("tcp4", "127.0.0.1:"+strconv.Itoa(i.Config.HTTPPort), time.Second)
		if err != nil {
			return
		}
		defer conn.Close()
		go func() { _, _ = io.Copy(conn, f); _ = conn.(*net.TCPConn).CloseWrite() }()
		_, _ = io.Copy(f, conn)
	})
}
