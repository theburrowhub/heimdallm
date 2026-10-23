package tui

import (
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

// maxInputRunes bounds what a single-line input holds.
const maxInputRunes = 512

// textInput is a minimal single-line text field: printable runes append,
// backspace deletes the last rune and ctrl+u clears the line. Enter and esc
// are left to the caller, which decides whether to commit or cancel.
type textInput struct {
	value []rune
}

func newTextInput(initial string) textInput {
	return textInput{value: []rune(initial)}
}

func (t textInput) String() string { return string(t.value) }

// update applies a key and reports whether it changed the text.
func (t *textInput) update(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyBackspace:
		if len(t.value) > 0 {
			t.value = t.value[:len(t.value)-1]
			return true
		}
	case tea.KeyCtrlU:
		if len(t.value) > 0 {
			t.value = nil
			return true
		}
	case tea.KeySpace:
		return t.insert([]rune{' '})
	case tea.KeyRunes:
		return t.insert(msg.Runes)
	}
	return false
}

func (t *textInput) insert(runes []rune) bool {
	changed := false
	for _, r := range runes {
		if !unicode.IsPrint(r) || len(t.value) >= maxInputRunes {
			continue
		}
		t.value = append(t.value, r)
		changed = true
	}
	return changed
}

// view renders the text with a cursor block at its end.
func (t textInput) view() string { return string(t.value) + "█" }
