package db

import "fmt"

// DefaultTrainedModelName is the name a trained model gets when none is given: its size, dataset and steps, and the start
// of the job's id, which tells models trained with the same settings apart ("4b-mine.jsonl-100steps-1a2b3c4d").
func DefaultTrainedModelName(m TrainedModel) string {
	dataset := m.Dataset
	if dataset == "" {
		dataset = "default"
	}
	id := m.JobID
	if len(id) > 8 {
		id = id[:8]
	}
	return fmt.Sprintf("%s-%s-%dsteps-%s", m.ModelSize, dataset, m.NumTrainSteps, id)
}
