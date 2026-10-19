package training_test

import (
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/training"
)

const goodDataset = "{\"instruction\":\"a\",\"input\":\"\",\"output\":\"b\"}\n\n{\"instruction\":\"c\",\"output\":\"d\"}\r\n"

func TestValidateTrainingDataset(t *testing.T) {
	if n, err := training.ValidateDataset([]byte(goodDataset)); err != nil || n != 2 {
		t.Fatalf("good: %d, %v", n, err)
	}
	for in, want := range map[string]string{
		"":           "no records",
		"\n \n":      "no records",
		"not json\n": "line 1 is not a JSON object",
		"[1]\n":      "line 1 is not a JSON object",
		"{\"instruction\":\"a\",\"output\":\"b\"}\n{\"instruction\":\"a\"}\n": "line 2 has no \"output\"",
		"{\"instruction\":1,\"output\":\"b\"}\n":                              "\"instruction\" must be a string",
		"{\"instruction\":\"a\",\"output\":\"b\",\"input\":null}\n":           "\"input\" must be a string",
		"\xff\xfe\n": "UTF-8",
	} {
		if _, err := training.ValidateDataset([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", in, err, want)
		}
	}
}
