package ocr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTesseract writes a script that prints out and exits with code.
func fakeTesseract(t *testing.T, out string, code int) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "tesseract")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' '%s'\nexit %d\n", out, code)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestCheck(t *testing.T) {
	const langs = "List of available languages in \"/usr/share/tesseract-ocr/5/tessdata/\" (3):\neng\nell\nosd\n"
	tests := map[string]struct {
		bin       string
		languages string
		wantErr   bool
	}{
		"installed":        {fakeTesseract(t, langs, 0), "eng+ell", false},
		"missing language": {fakeTesseract(t, langs, 0), "eng+deu", true},
		"binary fails":     {fakeTesseract(t, "", 1), "eng", true},
		"no binary":        {filepath.Join(t.TempDir(), "missing"), "eng", true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			err := (&Tesseract{Binary: tt.bin, Languages: tt.languages}).Check(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, want error %v", err, tt.wantErr)
			}
		})
	}
}

func TestRecognizeUnavailable(t *testing.T) {
	tess := &Tesseract{Binary: filepath.Join(t.TempDir(), "missing"), Languages: "eng"}
	if _, err := tess.Recognize(context.Background(), []byte("img")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing binary: err = %v, want ErrUnavailable", err)
	}
	// A binary that starts but fails is about the image, not Tesseract.
	tess.Binary = fakeTesseract(t, "", 1)
	if _, err := tess.Recognize(context.Background(), []byte("img")); err == nil || errors.Is(err, ErrUnavailable) {
		t.Fatalf("failing binary: err = %v, want an image error", err)
	}
}

func TestParseTSV(t *testing.T) {
	rows := []string{
		"level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext",
		"1\t1\t0\t0\t0\t0\t0\t0\t100\t100\t-1\t",
		"4\t1\t1\t1\t1\t0\t0\t0\t10\t10\t-1\t",
		"5\t1\t1\t1\t1\t1\t0\t0\t10\t10\t90\tHello",
		"5\t1\t1\t1\t1\t2\t0\t0\t10\t10\t80\tworld",
		"5\t1\t1\t1\t2\t1\t0\t0\t10\t10\t70\tΚαλημέρα",
		"5\t1\t1\t1\t2\t2\t0\t0\t10\t10\t95\t ",
		"5\t1\t2\t1\t1\t1\t0\t0\t10\t10\t60\tNext",
	}
	res, err := ParseTSV([]byte(strings.Join(rows, "\n") + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "Hello world\nΚαλημέρα\n\nNext"; res.Text != want {
		t.Errorf("text = %q, want %q", res.Text, want)
	}
	if res.Words != 4 || res.Confidence != 75 {
		t.Errorf("words = %d confidence = %v, want 4, 75", res.Words, res.Confidence)
	}
}

func TestParseTSVEmpty(t *testing.T) {
	res, err := ParseTSV([]byte("level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n1\t1\t0\t0\t0\t0\t0\t0\t100\t100\t-1\t\n"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "" || res.Words != 0 || res.Confidence != 0 {
		t.Errorf("got %+v, want empty", res)
	}
}
