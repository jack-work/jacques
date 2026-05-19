package harness

import (
	"fmt"
	"os"
	"strings"
)

type NvimHarness struct {
	addr string
}

func NewNvim() (*NvimHarness, error) {
	addr := os.Getenv("NVIM")
	if addr == "" {
		return nil, fmt.Errorf("$NVIM not set — run jacques from a neovim :terminal")
	}
	return &NvimHarness{addr: addr}, nil
}

func NewNvimWithPipe(addr string) *NvimHarness {
	return &NvimHarness{addr: addr}
}

const previewBufName = "jacques-preview.json"

func (h *NvimHarness) Preview(content string) error {
	v, err := dialNvim(h.addr)
	if err != nil {
		return fmt.Errorf("connect to nvim: %w", err)
	}
	defer v.Close()

	escaped := strings.ReplaceAll(content, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `'`, `''`)
	escaped = strings.ReplaceAll(escaped, "\n", `\n`)
	escaped = strings.ReplaceAll(escaped, "\r", "")

	// Single Lua chunk executed atomically — avoids incremental redraws
	// that corrupt the terminal buffer where the TUI is running.
	lua := fmt.Sprintf(`
local buf_name = '%s'
local lines = vim.split('%s', '\n', { plain = true })
local buf = nil
for _, b in ipairs(vim.api.nvim_list_bufs()) do
  if vim.api.nvim_buf_is_valid(b) and vim.api.nvim_buf_get_name(b):match(vim.pesc(buf_name) .. '$') then
    buf = b
    break
  end
end
if buf then
  vim.api.nvim_buf_set_lines(buf, 0, -1, false, lines)
  local found = false
  for _, w in ipairs(vim.api.nvim_list_wins()) do
    if vim.api.nvim_win_get_buf(w) == buf then
      found = true
      break
    end
  end
  if not found then
    vim.cmd('vsplit')
    vim.api.nvim_win_set_buf(0, buf)
  end
else
  vim.cmd('vsplit')
  buf = vim.api.nvim_create_buf(true, false)
  vim.api.nvim_buf_set_name(buf, buf_name)
  vim.api.nvim_buf_set_lines(buf, 0, -1, false, lines)
  vim.api.nvim_set_option_value('bufhidden', 'wipe', { buf = buf })
  vim.api.nvim_set_option_value('swapfile', false, { buf = buf })
  vim.api.nvim_win_set_buf(0, buf)
end
`, previewBufName, escaped)

	return v.ExecLua(lua, nil)
}
