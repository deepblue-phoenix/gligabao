package training

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	trainingeval "github.com/deepblue-phoenix/gligabao/internal/evals/training"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	log "github.com/sirupsen/logrus"
)

// LoadModelDataset reads the training dataset of tm from the training datasets directory dir: the dataset's name, the digest of
// the file and its examples (what an audit asks). The errors are safe to show to the user.
func LoadModelDataset(dir string, tm *trainingdb.TrainedModel) (name, sum string, examples []trainingeval.Example, err error) {
	name = strings.TrimSuffix(tm.Dataset, ".jsonl")
	if name == "" {
		return "", "", nil, errors.New("this model was trained on the sidecar's default dataset, which gligabao does not have: there are no questions to ask")
	}
	ds, err := FindDataset(dir, name)
	if err != nil {
		return "", "", nil, fmt.Errorf("the training dataset %s of this model is not in the training datasets directory", name)
	}
	content, err := os.ReadFile(ds.Path)
	if err != nil {
		log.WithError(err).WithField("dataset", name).Error("training: read training dataset file failed")
		return "", "", nil, fmt.Errorf("the data file of dataset %s cannot be read", name)
	}
	if len(content) > MaxDatasetBytes {
		return "", "", nil, fmt.Errorf("the data file of dataset %s is over %d MB", name, MaxDatasetBytes>>20)
	}
	if examples, err = trainingeval.ParseExamples(content); err != nil {
		return "", "", nil, fmt.Errorf("dataset %s: %w", name, err)
	}
	h := sha256.Sum256(content)
	return name, hex.EncodeToString(h[:]), examples, nil
}
