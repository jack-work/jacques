package render

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/jokellih/jacques/data"
	"github.com/jokellih/jacques/logging"
	"golang.org/x/term"
)

type TUIOptions struct {
	Height     int
	TimeFormat string
	Columns    []string
}

func DefaultTUIOptions() TUIOptions {
	return TUIOptions{
		Height:     20,
		TimeFormat: "2006-01-02 15:04:05",
	}
}

func TUI(store data.RowStore, opts TUIOptions) {
	ctx := context.Background()

	cols := store.Columns()
	if len(cols) == 0 {
		logging.Warn(ctx, "TUI received empty result")
		fmt.Fprintln(os.Stderr, "(no results)")
		return
	}

	filtered := filterColumns(store, opts.Columns)
	termWidth, termHeight := getTerminalSize()
	logging.Info(ctx, "TUI starting",
		logging.Int("columns", len(filtered.Columns())),
		logging.Int("rows", filtered.RowCount()),
		logging.Int("term_width", termWidth),
		logging.Int("term_height", termHeight),
	)

	cells := buildCells(filtered, opts)

	m := &model{
		store:      store,
		filtered:   filtered,
		cells:      cells,
		opts:       opts,
		termWidth:  termWidth,
		termHeight: termHeight,
		colWidths:  computeNaturalWidths(filtered, cells),
	}

	if _, err := tea.NewProgram(m).Run(); err != nil {
		logging.Error(ctx, "TUI program error", logging.String("error", err.Error()))
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		os.Exit(1)
	}
	logging.Info(ctx, "TUI exited cleanly")
}

// ---------------------------------------------------------------------------
// model
// ---------------------------------------------------------------------------

type viewMode int

const (
	modeTable viewMode = iota
	modeDetail
	modeSearch
)

type searchMatch struct {
	row, col int
}

type model struct {
	store    data.RowStore // original (all columns) for detail view
	filtered data.RowStore // column-filtered for table
	cells    [][]string    // pre-formatted cell strings

	opts       TUIOptions
	termWidth  int
	termHeight int
	colWidths  []int // natural (unclamped) widths

	cursorRow int
	cursorCol int
	scrollRow int
	scrollCol int

	expandedCol int // -1 = none
	mode        viewMode

	searchQuery   string
	searchMatches []searchMatch
	searchIdx     int

	yankText string
}

func (m *model) Init() tea.Cmd { return nil }

func (m *model) viewRows() int {
	return m.termHeight - 4 // header + separator + status + border
}

func (m *model) expandedLines() []string {
	if m.expandedCol < 0 {
		return nil
	}
	w := m.displayWidth(m.expandedCol)
	maxLines := m.viewRows() / 2
	if maxLines < 3 {
		maxLines = 3
	}

	raw := m.expandedRawValue()
	if isJSONValue(raw) {
		pretty := formatJSONPretty(raw)
		lines := strings.SplitN(pretty, "\n", maxLines+1)
		if len(lines) > maxLines {
			lines = lines[:maxLines]
			lines[maxLines-1] = padOrTruncate(lines[maxLines-1]+"…", w)
		}
		for i, line := range lines {
			lines[i] = padOrTruncate(line, w)
		}
		return lines
	}

	cellText := ""
	if m.cursorRow < len(m.cells) && m.expandedCol < len(m.cells[m.cursorRow]) {
		cellText = m.cells[m.cursorRow][m.expandedCol]
	}
	return wrapText(cellText, w, maxLines)
}

func (m *model) expandedRowHeight() int {
	lines := m.expandedLines()
	if len(lines) == 0 {
		return 1
	}
	return len(lines)
}

func (m *model) expandedRawValue() interface{} {
	if m.expandedCol < 0 || m.cursorRow < 0 {
		return nil
	}
	row, err := m.filtered.Row(m.cursorRow)
	if err != nil || m.expandedCol >= len(row) {
		return nil
	}
	return row[m.expandedCol]
}

func isJSONValue(v interface{}) bool {
	switch v.(type) {
	case map[string]interface{}, []interface{}:
		return true
	}
	if s, ok := v.(string); ok {
		s = strings.TrimSpace(s)
		return (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) ||
			(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]"))
	}
	return false
}

func prettyJSON(v interface{}) string {
	switch t := v.(type) {
	case map[string]interface{}, []interface{}:
		b, err := json.MarshalIndent(t, "", "  ")
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	case string:
		var parsed interface{}
		if err := json.Unmarshal([]byte(t), &parsed); err == nil {
			b, err := json.MarshalIndent(parsed, "", "  ")
			if err == nil {
				return string(b)
			}
		}
		return t
	default:
		return fmt.Sprintf("%v", v)
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if m.mode == modeSearch {
		return m.handleSearchInput(key, msg)
	}

	if m.mode == modeDetail {
		return m.handleDetailKey(key)
	}

	return m.handleTableKey(key)
}

func (m *model) handleTableKey(key string) (tea.Model, tea.Cmd) {
	numRows := len(m.cells)
	numCols := len(m.filtered.Columns())
	vr := m.viewRows()

	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		if m.expandedCol >= 0 {
			m.expandedCol = -1
		} else if m.searchQuery != "" {
			m.searchQuery = ""
			m.searchMatches = nil
		} else {
			return m, tea.Quit
		}

	// vertical movement
	case "j", "down":
		m.yankText = ""
		if m.cursorRow < numRows-1 {
			m.cursorRow++
		}
	case "k", "up":
		if m.cursorRow > 0 {
			m.cursorRow--
		}
	case "g":
		m.cursorRow = 0
	case "G":
		m.cursorRow = numRows - 1
	case "ctrl+d":
		m.cursorRow += vr / 2
		if m.cursorRow >= numRows {
			m.cursorRow = numRows - 1
		}
	case "ctrl+u":
		m.cursorRow -= vr / 2
		if m.cursorRow < 0 {
			m.cursorRow = 0
		}
	case "pgdown":
		m.cursorRow += vr
		if m.cursorRow >= numRows {
			m.cursorRow = numRows - 1
		}
	case "pgup":
		m.cursorRow -= vr
		if m.cursorRow < 0 {
			m.cursorRow = 0
		}

	// horizontal movement
	case "h", "left":
		if m.cursorCol > 0 {
			m.cursorCol--
			m.expandedCol = -1
		}
	case "l", "right":
		if m.cursorCol < numCols-1 {
			m.cursorCol++
			m.expandedCol = -1
		}
	case "0":
		m.cursorCol = 0
		m.expandedCol = -1
	case "$":
		m.cursorCol = numCols - 1
		m.expandedCol = -1

	// actions
	case "enter":
		m.mode = modeDetail
	case " ", "space":
		if m.expandedCol == m.cursorCol {
			m.expandedCol = -1
		} else {
			m.expandedCol = m.cursorCol
		}

	// yank
	case "y":
		var cellText string
		if m.expandedCol >= 0 && m.expandedCol == m.cursorCol {
			raw := m.expandedRawValue()
			if isJSONValue(raw) {
				cellText = prettyJSON(raw)
			}
		}
		if cellText == "" && m.cursorRow < len(m.cells) && m.cursorCol < len(m.cells[m.cursorRow]) {
			cellText = m.cells[m.cursorRow][m.cursorCol]
		}
		m.yankText = cellText
		copyToClipboard(cellText)
	case "Y":
		if m.cursorRow < len(m.cells) {
			parts := make([]string, len(m.cells[m.cursorRow]))
			copy(parts, m.cells[m.cursorRow])
			text := strings.Join(parts, "\t")
			m.yankText = text
			copyToClipboard(text)
		}

	// search
	case "/":
		m.mode = modeSearch
		m.searchQuery = ""
	case "n":
		m.nextMatch(1)
	case "N":
		m.nextMatch(-1)
	}

	m.clampScroll()
	return m, nil
}

func (m *model) handleDetailKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc", "enter":
		m.mode = modeTable
	case "j", "down":
		if m.cursorRow < len(m.cells)-1 {
			m.cursorRow++
		}
	case "k", "up":
		if m.cursorRow > 0 {
			m.cursorRow--
		}
	case "n":
		m.nextMatch(1)
	case "N":
		m.nextMatch(-1)
	}
	return m, nil
}

func (m *model) handleSearchInput(key string, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key {
	case "enter":
		m.runSearch()
		m.mode = modeTable
		if len(m.searchMatches) > 0 {
			m.searchIdx = 0
			m.jumpToMatch()
		}
	case "esc":
		m.mode = modeTable
	case "backspace":
		if len(m.searchQuery) > 0 {
			m.searchQuery = m.searchQuery[:len(m.searchQuery)-1]
		}
	default:
		if len(key) == 1 && key[0] >= 32 && key[0] < 127 {
			m.searchQuery += key
		} else if len(msg.Text) > 0 {
			m.searchQuery += msg.Text
		}
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// search
// ---------------------------------------------------------------------------

func (m *model) runSearch() {
	m.searchMatches = nil
	if m.searchQuery == "" {
		return
	}
	q := strings.ToLower(m.searchQuery)

	if m.expandedCol >= 0 {
		// Search only within the expanded cell's full content (including pretty JSON)
		raw := m.expandedRawValue()
		var searchText string
		if isJSONValue(raw) {
			searchText = prettyJSON(raw)
		} else if m.cursorRow < len(m.cells) && m.expandedCol < len(m.cells[m.cursorRow]) {
			searchText = m.cells[m.cursorRow][m.expandedCol]
		}
		if strings.Contains(strings.ToLower(searchText), q) {
			m.searchMatches = append(m.searchMatches, searchMatch{m.cursorRow, m.expandedCol})
		}
		return
	}

	for r, row := range m.cells {
		for c, cell := range row {
			if strings.Contains(strings.ToLower(cell), q) {
				m.searchMatches = append(m.searchMatches, searchMatch{r, c})
			}
		}
	}
}

func (m *model) nextMatch(dir int) {
	if len(m.searchMatches) == 0 {
		return
	}
	m.searchIdx += dir
	if m.searchIdx >= len(m.searchMatches) {
		m.searchIdx = 0
	}
	if m.searchIdx < 0 {
		m.searchIdx = len(m.searchMatches) - 1
	}
	m.jumpToMatch()
}

func (m *model) jumpToMatch() {
	if m.searchIdx < 0 || m.searchIdx >= len(m.searchMatches) {
		return
	}
	match := m.searchMatches[m.searchIdx]
	m.cursorRow = match.row
	m.cursorCol = match.col
	m.clampScroll()
}

// ---------------------------------------------------------------------------
// scrolling
// ---------------------------------------------------------------------------

func (m *model) clampScroll() {
	vr := m.viewRows()
	if m.cursorRow < m.scrollRow {
		m.scrollRow = m.cursorRow
	}

	// Ensure the cursor row (including its expanded height) fits in view
	for {
		linesUsed := 0
		fits := false
		for r := m.scrollRow; r < len(m.cells); r++ {
			h := 1
			if r == m.cursorRow && m.expandedCol >= 0 {
				h = m.expandedRowHeight()
			}
			if r == m.cursorRow && linesUsed+h <= vr {
				fits = true
				break
			}
			linesUsed += h
			if linesUsed >= vr {
				break
			}
		}
		if fits || m.scrollRow >= m.cursorRow {
			break
		}
		m.scrollRow++
	}

	visibleCols := m.visibleColRange()
	if m.cursorCol < m.scrollCol {
		m.scrollCol = m.cursorCol
	}
	if m.cursorCol >= m.scrollCol+visibleCols {
		m.scrollCol = m.cursorCol - visibleCols + 1
	}
	if m.scrollCol < 0 {
		m.scrollCol = 0
	}
}

func (m *model) visibleColRange() int {
	w := m.termWidth - 2
	count := 0
	used := 0
	for c := m.scrollCol; c < len(m.filtered.Columns()); c++ {
		cw := m.displayWidth(c)
		if count > 0 {
			cw += 3 // separator
		}
		if used+cw > w {
			break
		}
		used += cw
		count++
	}
	if count == 0 {
		count = 1
	}
	return count
}

func (m *model) displayWidth(col int) int {
	if col == m.expandedCol {
		maxW := m.termWidth / 2
		if m.colWidths[col] < maxW {
			return m.colWidths[col]
		}
		return maxW
	}
	w := m.colWidths[col]
	maxNormal := 30
	if w > maxNormal {
		w = maxNormal
	}
	if w < 4 {
		w = 4
	}
	return w
}

// ---------------------------------------------------------------------------
// view
// ---------------------------------------------------------------------------

// ANSI escape sequences for fast cell rendering (avoid lipgloss.Render per cell)
const (
	ansiReset     = "\x1b[0m"
	ansiHeader    = "\x1b[1;38;5;39m"   // bold + fg 39
	ansiSep       = "\x1b[38;5;238m"    // fg 238
	ansiNormal    = "\x1b[38;5;252m"    // fg 252
	ansiCursor    = "\x1b[38;5;229;48;5;57m" // fg 229 bg 57
	ansiRowHL     = "\x1b[38;5;229;48;5;236m" // fg 229 bg 236
	ansiSearchHL  = "\x1b[1;38;5;0;48;5;220m" // bold fg 0 bg 220
	ansiMatchCell = "\x1b[38;5;220m"    // fg 220
	ansiHelp      = "\x1b[38;5;241m"    // fg 241
)

// lipgloss styles only for complex rendering (detail view, title bar)
var (
	stDetailKey = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	stDetailVal = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	stTitle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57")).Padding(0, 1)
	stHelp      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)

func ansiWrap(code, text string) string {
	return code + text + ansiReset
}

func (m *model) View() tea.View {
	var v tea.View
	v.AltScreen = true
	switch m.mode {
	case modeDetail:
		v.SetContent(m.viewDetail())
	case modeSearch:
		v.SetContent(m.viewTable() + m.viewSearchPrompt())
	default:
		v.SetContent(m.viewTable())
	}
	return v
}

func (m *model) viewTable() string {
	var b strings.Builder

	colStart := m.scrollCol
	colEnd := colStart + m.visibleColRange()
	if colEnd > len(m.filtered.Columns()) {
		colEnd = len(m.filtered.Columns())
	}

	// header
	m.writeHeaderRow(&b, colStart, colEnd)
	b.WriteByte('\n')
	m.writeSeparator(&b, colStart, colEnd)
	b.WriteByte('\n')

	// data rows
	vr := m.viewRows()
	linesUsed := 0
	for r := m.scrollRow; r < len(m.cells) && linesUsed < vr; r++ {
		rowLines := m.renderDataRow(r, colStart, colEnd)
		for _, line := range rowLines {
			if linesUsed >= vr {
				break
			}
			b.WriteString(line)
			b.WriteByte('\n')
			linesUsed++
		}
	}

	// status bar
	b.WriteString(m.statusBar())
	b.WriteByte('\n')

	return b.String()
}

func (m *model) writeHeaderRow(b *strings.Builder, colStart, colEnd int) {
	for c := colStart; c < colEnd; c++ {
		if c > colStart {
			b.WriteString(ansiWrap(ansiSep, " │ "))
		}
		w := m.displayWidth(c)
		title := m.filtered.Columns()[c].Name
		b.WriteString(ansiWrap(ansiHeader, padOrTruncate(title, w)))
	}
}

func (m *model) writeSeparator(b *strings.Builder, colStart, colEnd int) {
	for c := colStart; c < colEnd; c++ {
		if c > colStart {
			b.WriteString(ansiWrap(ansiSep, "─┼─"))
		}
		w := m.displayWidth(c)
		b.WriteString(ansiWrap(ansiSep, strings.Repeat("─", w)))
	}
}

func (m *model) renderDataRow(row, colStart, colEnd int) []string {
	isCurrentRow := row == m.cursorRow
	isExpanded := isCurrentRow && m.expandedCol >= 0

	numLines := 1
	if isExpanded {
		numLines = m.expandedRowHeight()
	}

	// For each column, compute wrapped lines
	type colLines struct {
		lines []string
		width int
	}
	colData := make([]colLines, colEnd-colStart)

	for ci, c := range rangeSlice(colStart, colEnd) {
		w := m.displayWidth(c)
		cellText := ""
		if c < len(m.cells[row]) {
			cellText = m.cells[row][c]
		}

		if isExpanded && c == m.expandedCol {
			colData[ci] = colLines{m.expandedLines(), w}
		} else {
			colData[ci] = colLines{[]string{padOrTruncate(cellText, w)}, w}
		}
	}

	output := make([]string, numLines)
	for lineIdx := 0; lineIdx < numLines; lineIdx++ {
		var b strings.Builder
		for ci, c := range rangeSlice(colStart, colEnd) {
			if ci > 0 {
				b.WriteString(ansiWrap(ansiSep, " │ "))
			}

			display := strings.Repeat(" ", colData[ci].width)
			if lineIdx < len(colData[ci].lines) {
				display = colData[ci].lines[lineIdx]
			}

			isCurrentCell := isCurrentRow && c == m.cursorCol
			isCellMatch := m.isCellSearchMatch(row, c)

			if isCurrentCell {
				display = m.highlightSearchANSI(display)
				b.WriteString(ansiWrap(ansiCursor, display))
			} else if isCellMatch {
				display = m.highlightSearchANSI(display)
				if isCurrentRow {
					b.WriteString(ansiWrap(ansiRowHL, display))
				} else {
					b.WriteString(ansiWrap(ansiMatchCell, display))
				}
			} else if isCurrentRow {
				b.WriteString(ansiWrap(ansiRowHL, display))
			} else {
				b.WriteString(ansiWrap(ansiNormal, display))
			}
		}
		output[lineIdx] = b.String()
	}

	return output
}

func rangeSlice(start, end int) []int {
	s := make([]int, end-start)
	for i := range s {
		s[i] = start + i
	}
	return s
}

func wrapText(text string, width, maxLines int) []string {
	if width <= 0 {
		return []string{""}
	}
	var lines []string
	for len(text) > 0 && len(lines) < maxLines {
		end := width
		if end > len(text) {
			end = len(text)
		}
		line := text[:end]
		text = text[end:]

		if len(lines) == maxLines-1 && len(text) > 0 {
			// last allowed line and there's more text — add ellipsis
			if len(line) > 1 {
				line = line[:len(line)-1] + "…"
			}
		}

		line = padOrTruncate(line, width)
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		lines = []string{strings.Repeat(" ", width)}
	}
	return lines
}

func (m *model) statusBar() string {
	var parts []string
	parts = append(parts, fmt.Sprintf("row %d/%d", m.cursorRow+1, len(m.cells)))
	parts = append(parts, fmt.Sprintf("col %d/%d [%s]",
		m.cursorCol+1, len(m.filtered.Columns()),
		m.filtered.Columns()[m.cursorCol].Name))

	if len(m.searchMatches) > 0 {
		parts = append(parts, fmt.Sprintf("/%s [%d/%d]", m.searchQuery, m.searchIdx+1, len(m.searchMatches)))
	} else if m.searchQuery != "" {
		parts = append(parts, fmt.Sprintf("/%s [no matches]", m.searchQuery))
	}

	if m.yankText != "" {
		yanked := m.yankText
		if len(yanked) > 40 {
			yanked = yanked[:39] + "…"
		}
		parts = append(parts, fmt.Sprintf("yanked: %q", yanked))
	}

	left := stHelp.Render(strings.Join(parts, "  "))
	right := stHelp.Render("hjkl:move  space:expand  y:yank  enter:detail  /:search  q:quit")

	gap := m.termWidth - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 2 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *model) viewSearchPrompt() string {
	return "\n" + stHelp.Render("/") + m.searchQuery + "█"
}

// ---------------------------------------------------------------------------
// detail view
// ---------------------------------------------------------------------------

func (m *model) viewDetail() string {
	idx := m.cursorRow
	if idx < 0 || idx >= m.store.RowCount() {
		return "No row selected"
	}

	row, _ := m.store.Row(idx)
	allCols := m.store.Columns()
	var b strings.Builder

	b.WriteString(stTitle.Render(fmt.Sprintf(" Row %d/%d ", idx+1, m.store.RowCount())))
	b.WriteString("\n\n")

	for i, col := range allCols {
		val := ""
		if i < len(row) {
			val = formatValue(row[i], col.Type, DefaultOptions())
		}
		if val == "" {
			continue
		}

		key := stDetailKey.Render(fmt.Sprintf("%s:", col.Name))
		if m.searchQuery != "" {
			val = m.highlightSearchInText(val)
		}
		value := stDetailVal.Render(val)
		b.WriteString(fmt.Sprintf("  %s %s\n", key, value))
	}

	b.WriteString("\n")

	var help []string
	help = append(help, "esc/enter: back")
	help = append(help, "j/k: prev/next row")
	if len(m.searchMatches) > 0 {
		help = append(help, fmt.Sprintf("n/N: search [%d/%d]", m.searchIdx+1, len(m.searchMatches)))
	}
	help = append(help, "q: quit")
	b.WriteString(stHelp.Render("  " + strings.Join(help, "  ")))
	b.WriteByte('\n')

	return b.String()
}

// ---------------------------------------------------------------------------
// search highlighting
// ---------------------------------------------------------------------------

func (m *model) isCellSearchMatch(row, col int) bool {
	if m.searchQuery == "" {
		return false
	}
	for _, sm := range m.searchMatches {
		if sm.row == row && sm.col == col {
			return true
		}
	}
	return false
}

func (m *model) highlightSearchInText(text string) string {
	return m.highlightSearchANSI(text)
}

func (m *model) highlightSearchANSI(text string) string {
	if m.searchQuery == "" {
		return text
	}
	q := strings.ToLower(m.searchQuery)
	lower := strings.ToLower(text)
	idx := strings.Index(lower, q)
	if idx < 0 {
		return text
	}

	var b strings.Builder
	for idx >= 0 {
		b.WriteString(text[:idx])
		matchEnd := idx + len(m.searchQuery)
		b.WriteString(ansiSearchHL)
		b.WriteString(text[idx:matchEnd])
		b.WriteString(ansiReset)
		text = text[matchEnd:]
		lower = lower[matchEnd:]
		idx = strings.Index(lower, q)
	}
	b.WriteString(text)
	return b.String()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func getTerminalSize() (width, height int) {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return 120, 30
	}
	return w, h
}

func filterColumns(store data.RowStore, wanted []string) data.RowStore {
	if len(wanted) == 0 {
		return store
	}

	srcCols := store.Columns()
	wantSet := make(map[string]int, len(wanted))
	for i, name := range wanted {
		wantSet[name] = i
	}

	type colMapping struct {
		srcIdx, dstIdx int
		col            data.Column
	}
	var mappings []colMapping
	for i, col := range srcCols {
		if dstIdx, ok := wantSet[col.Name]; ok {
			mappings = append(mappings, colMapping{srcIdx: i, dstIdx: dstIdx, col: col})
		}
	}

	cols := make([]data.Column, len(mappings))
	for _, mp := range mappings {
		cols[mp.dstIdx] = mp.col
	}

	rows := make([][]interface{}, store.RowCount())
	for r := 0; r < store.RowCount(); r++ {
		srcRow, _ := store.Row(r)
		row := make([]interface{}, len(mappings))
		for _, mp := range mappings {
			if mp.srcIdx < len(srcRow) {
				row[mp.dstIdx] = srcRow[mp.srcIdx]
			}
		}
		rows[r] = row
	}

	return data.NewMemoryStore(cols, rows)
}

func buildCells(store data.RowStore, opts TUIOptions) [][]string {
	tableOpts := DefaultOptions()
	tableOpts.TimeFormat = opts.TimeFormat
	cols := store.Columns()

	cells := make([][]string, store.RowCount())
	for r := 0; r < store.RowCount(); r++ {
		row, _ := store.Row(r)
		cells[r] = make([]string, len(cols))
		for c, val := range row {
			colType := ""
			if c < len(cols) {
				colType = cols[c].Type
			}
			cells[r][c] = formatValue(val, colType, tableOpts)
		}
	}
	return cells
}

func computeNaturalWidths(store data.RowStore, cells [][]string) []int {
	cols := store.Columns()
	widths := make([]int, len(cols))
	for i, col := range cols {
		widths[i] = len(col.Name)
	}
	for _, row := range cells {
		for c, cell := range row {
			if len(cell) > widths[c] {
				widths[c] = len(cell)
			}
		}
	}
	return widths
}

func padOrTruncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if len(s) > w {
		if w > 1 {
			return s[:w-1] + "…"
		}
		return s[:w]
	}
	if len(s) < w {
		return s + strings.Repeat(" ", w-len(s))
	}
	return s
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func copyToClipboard(text string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("clip.exe")
	case "darwin":
		cmd = exec.Command("pbcopy")
	default:
		cmd = exec.Command("xclip", "-selection", "clipboard")
	}
	cmd.Stdin = strings.NewReader(text)
	_ = cmd.Run()
}
