//go:build windows

package harness

import (
	"context"
	"net"
	"strings"

	winio "github.com/Microsoft/go-winio"
	"github.com/neovim/go-client/nvim"
)

func dialNvim(addr string) (*nvim.Nvim, error) {
	pipe := normalizePipe(addr)
	return nvim.Dial(pipe, nvim.DialNetDial(
		func(ctx context.Context, network, address string) (net.Conn, error) {
			return winio.DialPipeContext(ctx, pipe)
		},
	))
}

// normalizePipe ensures the pipe path starts with \\.\pipe\
// to handle variations from different shells.
func normalizePipe(addr string) string {
	addr = strings.ReplaceAll(addr, "/", "\\")
	if strings.HasPrefix(addr, "\\\\.\\pipe\\") {
		return addr
	}
	if strings.HasPrefix(addr, "\\.\\pipe\\") {
		return "\\" + addr
	}
	return addr
}
