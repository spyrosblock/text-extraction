package ocr

import (
	"strings"
	"testing"
)

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
