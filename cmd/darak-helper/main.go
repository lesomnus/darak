// Command darak-helper serves one user's file operations.
//
// It is not meant to be run by hand. The server spawns it already dropped to the
// target user, with a SOCK_SEQPACKET socket on file descriptor 3, and speaks the
// protocol in docs/helper-protocol.md over it.
//
// Everything this process can do is bounded by the credentials it was started
// with and by the root it is given. It makes no authorization decisions of its
// own: it attempts each operation and reports the kernel's answer.
package main

import (
	"fmt"
	"os"

	"github.com/lesomnus/darak/internal/helper"
)

// sockFD is the descriptor the server passes the socket on. It is the first
// slot after stdio, which is what exec.Cmd.ExtraFiles[0] becomes.
const sockFD = 3

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <root>\n\nSpawned by the server; not for interactive use.\n", os.Args[0])
		os.Exit(2)
	}
	root := os.Args[1]

	sock := os.NewFile(sockFD, "helper-socket")
	if sock == nil {
		fmt.Fprintf(os.Stderr, "darak-helper: no socket on fd %d\n", sockFD)
		os.Exit(2)
	}

	h, err := helper.New(root, sock)
	// helper.New dups the descriptor into a net.Conn; this copy is finished with
	// either way.
	sock.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "darak-helper: %v\n", err)
		os.Exit(1)
	}
	defer h.Close()

	if err := h.Serve(); err != nil {
		fmt.Fprintf(os.Stderr, "darak-helper: serve: %v\n", err)
		os.Exit(1)
	}
}
