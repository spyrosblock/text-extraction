// Package ocr runs Tesseract on page images.
//
// It shells out to the tesseract CLI rather than binding libtesseract with
// cgo: the binary and language data come from the container image, and each
// page runs in its own process so a crash on one page can't take the job down.
package ocr

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Result is the text Tesseract read from one image.
type Result struct {
	Text string
	// Confidence is the mean word confidence, 0-100. 0 when no words were found.
	Confidence float64
	Words      int
}

// Tesseract runs the tesseract binary.
type Tesseract struct {
	// Binary defaults to "tesseract" on PATH.
	Binary string
	// Languages is passed to -l, e.g. "eng+ell".
	Languages string
}

// Recognize runs OCR on img (any format Leptonica reads: PGM, PNG, TIFF...).
func (t *Tesseract) Recognize(ctx context.Context, img []byte) (Result, error) {
	bin := t.Binary
	if bin == "" {
		bin = "tesseract"
	}
	// tsv output carries per-word confidences next to the text, so one run
	// gives both.
	cmd := exec.CommandContext(ctx, bin, "stdin", "stdout", "-l", t.Languages, "--psm", "3", "tsv")
	cmd.Stdin = bytes.NewReader(img)
	// Pages already run in parallel; stop each process from also spawning
	// an OpenMP thread per core.
	cmd.Env = append(os.Environ(), "OMP_THREAD_LIMIT=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return Result{}, fmt.Errorf("tesseract: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return ParseTSV(stdout.Bytes())
}

// ParseTSV rebuilds the text from Tesseract's tsv output: words on a line
// are joined by spaces, lines by newlines and paragraphs by a blank line.
func ParseTSV(tsv []byte) (Result, error) {
	const (
		colLevel = iota
		colPage
		colBlock
		colPar
		colLine
		colWord
		colLeft
		colTop
		colWidth
		colHeight
		colConf
		colText
		numCols
	)
	const wordLevel = "5"

	var (
		text     strings.Builder
		res      Result
		confSum  float64
		prevPar  string
		prevLine string
	)
	sc := bufio.NewScanner(bytes.NewReader(tsv))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for first := true; sc.Scan(); first = false {
		if first {
			continue // header
		}
		f := strings.SplitN(sc.Text(), "\t", numCols)
		if len(f) < numCols || f[colLevel] != wordLevel {
			continue
		}
		word := strings.TrimSpace(f[colText])
		if word == "" {
			continue
		}
		conf, err := strconv.ParseFloat(f[colConf], 64)
		if err != nil {
			return Result{}, fmt.Errorf("tesseract tsv: bad confidence %q", f[colConf])
		}
		if conf < 0 {
			continue
		}

		par := f[colPage] + "." + f[colBlock] + "." + f[colPar]
		line := par + "." + f[colLine]
		switch {
		case res.Words == 0:
		case par != prevPar:
			text.WriteString("\n\n")
		case line != prevLine:
			text.WriteByte('\n')
		default:
			text.WriteByte(' ')
		}
		prevPar, prevLine = par, line

		text.WriteString(word)
		confSum += conf
		res.Words++
	}
	if err := sc.Err(); err != nil {
		return Result{}, fmt.Errorf("tesseract tsv: %w", err)
	}
	res.Text = text.String()
	if res.Words > 0 {
		res.Confidence = confSum / float64(res.Words)
	}
	return res, nil
}
