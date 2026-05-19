//go:build !windows

package harness

import "github.com/neovim/go-client/nvim"

func dialNvim(addr string) (*nvim.Nvim, error) {
	return nvim.Dial(addr)
}
