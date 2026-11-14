package contract

import (
	"encoding/json"
	"testing"
)

func TestConfidenceJSON(t *testing.T) {
	tests := []struct {
		conf Confidence
		json string
		back Confidence
	}{
		{TextLayer, `"-"`, TextLayer},
		{0, `0`, 0},
		{87.456, `87.46`, 87.46},
		{100, `100`, 100},
	}
	for _, tt := range tests {
		b, err := json.Marshal(Page{Number: 1, Text: "x", Confidence: tt.conf})
		if err != nil {
			t.Fatal(err)
		}
		if want := `{"page":1,"text":"x","confidence":` + tt.json + `}`; string(b) != want {
			t.Errorf("marshal %v = %s, want %s", tt.conf, b, want)
		}
		var p Page
		if err := json.Unmarshal(b, &p); err != nil || p.Confidence != tt.back {
			t.Errorf("unmarshal %s = %v, %v; want %v", b, p.Confidence, err, tt.back)
		}
	}
	var p Page
	if err := json.Unmarshal([]byte(`{"confidence":"high"}`), &p); err == nil {
		t.Error(`unmarshal "high": want error`)
	}
}
