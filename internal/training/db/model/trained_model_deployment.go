package model

import (
	"encoding/json"
	"time"
)

// Statuses of a TrainedModelDeployment.
const (
	TrainedModelDeploymentRunning   = "running"   // the files are being fetched, or the deployer is working
	TrainedModelDeploymentCompleted = "completed" // the deployer returned: Ref and Details
	TrainedModelDeploymentFailed    = "failed"    // fetching the files or the deployer failed: Error says why
)

// TrainedModelDeploymentUpdate is one progress message of a deployment: what the deployer (or gligabao) reported while it ran.
type TrainedModelDeploymentUpdate struct {
	Time    time.Time `json:"time"`
	Message string    `json:"message"`
}

// TrainedModelDeployment is one request to deploy a trained model through a deployer, a hook of the embedding program
// (gb_trained_model_deployments). It is stored when the deployment starts (status "running") and updated when it ends, so every deployment
// can be reviewed later, including the ones that failed or were interrupted by a restart of the server.
type TrainedModelDeployment struct {
	ID int64 `json:"id"`
	// TrainedModelID is the trained model (gb_trained_models) that was deployed.
	TrainedModelID int64 `json:"trained_model_id"`
	// Deployer is the name of the deployer that was asked to deploy it.
	Deployer string `json:"deployer"`
	// Options are the free-form options the request passed to the deployer, as a JSON object.
	Options json.RawMessage `json:"options"`
	Status  string          `json:"status"`
	// Dir is the local directory the weights were fetched to and the deployer was given ("" until they are fetched).
	Dir string `json:"dir"`
	// Ref is the deployer's identifier of the result (e.g. the name of the model it created elsewhere), Details anything else
	// it reported, as a JSON object. Both are empty until the deployment completes.
	Ref     string          `json:"ref"`
	Details json.RawMessage `json:"details"`
	// Error is the reason when Status is "failed".
	Error string `json:"error"`
	// Updates are the progress messages the deployer (and gligabao) reported while the deployment ran, oldest first, for the admin UI.
	// They are not stored: the API fills them in for a deployment that is running, and in the answer of the request that ran it
	// (empty otherwise; never nil in the JSON).
	Updates []TrainedModelDeploymentUpdate `json:"updates"`
	// DurationMS is how long the deployment took (0 while it runs); CompletedAt when it ended (nil while it runs).
	DurationMS  int64      `json:"duration_ms"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

// jsonObject is raw as stored: a JSON object ("{}" when there is none).
func jsonObject(raw json.RawMessage) string {
	if len(raw) == 0 || !json.Valid(raw) {
		return "{}"
	}
	return string(raw)
}

// OptionsJSON is Options as stored: a JSON object ("{}" when there are none).
func (e TrainedModelDeployment) OptionsJSON() string { return jsonObject(e.Options) }

// DetailsJSON is Details as stored: a JSON object ("{}" when there are none).
func (e TrainedModelDeployment) DetailsJSON() string { return jsonObject(e.Details) }

// SetStoredJSON sets Options and Details from the columns they are stored in (an unreadable column leaves the field "{}").
func (e *TrainedModelDeployment) SetStoredJSON(options, details string) {
	e.Options, e.Details = json.RawMessage("{}"), json.RawMessage("{}")
	if json.Valid([]byte(options)) {
		e.Options = json.RawMessage(options)
	}
	if json.Valid([]byte(details)) {
		e.Details = json.RawMessage(details)
	}
}
