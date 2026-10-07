package bot

import (
	"strings"
	"testing"
)

// Telegram rejects a message over 4096 characters; the help text is sent in one.
func TestHelpFitsInOneMessage(t *testing.T) {
	if n := len([]rune(helpText)); n > 4096 {
		t.Errorf("help text is %d characters, Telegram's limit is 4096", n)
	}
}

// Every tag opened in the help text must be closed, or Telegram refuses to send it.
func TestHelpHTMLIsBalanced(t *testing.T) {
	for _, tag := range []string{"b", "code", "i"} {
		open, closed := strings.Count(helpText, "<"+tag+">"), strings.Count(helpText, "</"+tag+">")
		if open != closed {
			t.Errorf("<%s>: %d opened, %d closed", tag, open, closed)
		}
	}
}

// The bot speaks English: no Cyrillic may leak into user-facing text. Russian
// belongs only in the parser's input vocabulary, never in the help text.
func TestHelpIsEnglish(t *testing.T) {
	for _, r := range helpText {
		if r >= 'а' && r <= 'я' || r >= 'А' && r <= 'Я' || r == 'ё' || r == 'Ё' {
			t.Fatalf("help text contains Cyrillic: %q", string(r))
		}
	}
}
