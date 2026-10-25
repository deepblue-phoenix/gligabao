package model

import (
	"encoding/json"
	"time"
)

// Statuses of a TrainedModelExport.
const (
	TrainedModelExportRunning   = "running"   // the files are being fetched, or the exporter is working
	TrainedModelExportCompleted = "completed" // the exporter returned: Ref and Details
	TrainedModelExportFailed    = "failed"    // fetching the files or the exporter failed: Error says why
)

// TrainedModelExportUpdate is one progress message of an export: what the exporter (or gligabao) reported while it ran.
type TrainedModelExportUpdate struct {
	Time    time.Time `json:"time"`
	Message string    `json:"message"`
}

// TrainedModelExport is one request to export a trained model through an exporter, a hook of the embedding program
// (gb_trained_model_exports). It is stored when the export starts (status "running") and updated when it ends, so every export
// can be reviewed later, including the ones that failed or were interrupted by a restart of the server.
type TrainedModelExport struct {
	ID int64 `json:"id"`
	// TrainedModelID is the trained model (gb_trained_models) that was exported.
	TrainedModelID int64 `json:"trained_model_id"`
	// Exporter is the name of the exporter that was asked to export it.
	Exporter string `json:"exporter"`
	// Options are the free-form options the request passed to the exporter, as a JSON object.
	Options json.RawMessage `json:"options"`
	Status  string          `json:"status"`
	// Dir is the local directory the weights were fetched to and the exporter was given ("" until they are fetched).
	Dir string `json:"dir"`
	// Ref is the exporter's identifier of the result (e.g. the name of the model it created elsewhere), Details anything else
	// it reported, as a JSON object. Both are empty until the export completes.
	Ref     string          `json:"ref"`
	Details json.RawMessage `json:"details"`
	// Error is the reason when Status is "failed".
	Error string `json:"error"`
	// Updates are the progress messages the exporter (and gligabao) reported while the export ran, oldest first, for the admin UI.
	// They are not stored: the API fills them in for an export that is running, and in the answer of the request that ran it
	// (empty otherwise; never nil in the JSON).
	Updates []TrainedModelExportUpdate `json:"updates"`
	// DurationMS is how long the export took (0 while it runs); CompletedAt when it ended (nil while it runs).
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
func (e TrainedModelExport) OptionsJSON() string { return jsonObject(e.Options) }

// DetailsJSON is Details as stored: a JSON object ("{}" when there are none).
func (e TrainedModelExport) DetailsJSON() string { return jsonObject(e.Details) }

// SetStoredJSON sets Options and Details from the columns they are stored in (an unreadable column leaves the field "{}").
func (e *TrainedModelExport) SetStoredJSON(options, details string) {
	e.Options, e.Details = json.RawMessage("{}"), json.RawMessage("{}")
	if json.Valid([]byte(options)) {
		e.Options = json.RawMessage(options)
	}
	if json.Valid([]byte(details)) {
		e.Details = json.RawMessage(details)
	}
}
