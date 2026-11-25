package logging

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	old := logger
	logger = log.New(&buf, "", 0)
	defer func() { logger = old }()

	lvl, err := ParseLevel("DEBUG")
	if err != nil || lvl != LevelDebug {
		t.Fatalf("ParseLevel(DEBUG) = %v, %v", lvl, err)
	}
	if lvl, _ := ParseLevel("warn"); lvl != LevelWarn {
		t.Fatalf("ParseLevel(warn) = %v", lvl)
	}
	if lvl, err := ParseLevel("bogus"); err != nil || lvl != LevelInfo {
		t.Fatalf("ParseLevel(bogus) = %v, %v", lvl, err)
	}

	SetLevel(LevelInfo)
	Debugf("hidden %d", 1)
	Infof("shown %d", 2)
	if !strings.Contains(buf.String(), "shown 2") || strings.Contains(buf.String(), "hidden") {
		t.Fatalf("info-level filtering wrong: %q", buf.String())
	}

	buf.Reset()
	SetLevel(LevelDebug)
	Debugf("now %d", 3)
	if !strings.Contains(buf.String(), "[DEBUG] now 3") {
		t.Fatalf("debug line missing: %q", buf.String())
	}
	SetLevel(LevelInfo)
}
