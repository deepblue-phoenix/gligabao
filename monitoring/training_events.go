package monitoring

import (
	"encoding/json"
	"sync"

	"github.com/deepblue-phoenix/gligabao/gbruntime"
	log "github.com/sirupsen/logrus"
)

// Training events: the one kind of event of the training jobs. A job's events come from the ML sidecar (its status changes, progress
// steps, log lines and the system events of its container) and from gligabao itself (a job started, a stop requested, the model
// recorded, the event stream of the job failed). They have one shape (TrainingEvent) and take two paths: the SSE feed of the training
// API (GET /v1/gb/training/events) and, delivered asynchronously, the channel of every Hooks.TrainingEvents callback: a callback is
// given a channel once, when the server starts (see Start), and reads the events from it whenever it likes.

// Provider types: the ProviderType of an event says which compute provider it is about.
const (
	ProviderModal = "modal"
	// ProviderSargo runs the GCNII* graph classifier trainings (sargo, in the process of the server or beside it).
	ProviderSargo = "sargo"
)

// Types of a TrainingEvent (its Type field, also the name of the event on the SSE feed).
const (
	// From the ML sidecar's event log of the job.
	EventStatus   = "status"   // a state change: Status, and MaxSteps / FinalLoss / Error
	EventProgress = "progress" // a training step: Step, Loss, LearningRate, Epoch, ElapsedSeconds
	EventLog      = "log"      // what the job is doing: Message
	EventSystem   = "system"   // a system event of the job's container: System, Message, Data

	// From gligabao. They are not in the stored log of the job (their Seq is NoSeq).
	EventStarted       = "started"        // the provider accepted the job (MaxSteps is the number of steps asked)
	EventStopRequested = "stop_requested" // a stop was asked of the provider
	EventModelRecorded = "model_recorded" // the weights of a completed job were recorded (ModelID, Model)
	EventStreamError   = "stream_error"   // the job's event stream failed, it is retried (Error)
)

// NoSeq is the Seq of an event that is not in the stored log of its job (the events of gligabao itself): it is only live.
const NoSeq = -1

// TrainingEventsBuffer is the depth of the channel each TrainingEvents callback gets. An event that finds it full is dropped
// (with a warning): a slow consumer never slows the server.
const TrainingEventsBuffer = 1000

// TrainingEvent is one event of a training job. Fields that an event does not have are nil / empty.
type TrainingEvent struct {
	// RunID and JobID say which job the event is of (the run is the row of gb_training_job_runs). UserID is the user the job belongs
	// to: a subscription of the feed gets only the events of its user, and a hook has it as the provenance of the event. It is not
	// sent to the clients of the feed. ProviderType is the compute provider the event is about (ProviderModal, ...): empty for an
	// event that is not about a provider.
	RunID        int64  `json:"run_id,omitempty"`
	JobID        string `json:"job_id,omitempty"`
	UserID       string `json:"-"`
	ProviderType string `json:"provider_type,omitempty"`
	// Seq is the event's index in the job's event log (the SSE id), or NoSeq.
	Seq  int    `json:"seq"`
	Type string `json:"type"` // one of the Event* types
	// Status ("running", "training", "completed", "failed", "cancelled") is set on status events. A completed event has the
	// FinalLoss, a failed or cancelled one the Error; a "training" one the MaxSteps.
	Status    string   `json:"status,omitempty"`
	MaxSteps  *int     `json:"max_steps,omitempty"`
	FinalLoss *float64 `json:"final_loss,omitempty"`
	Error     string   `json:"error,omitempty"`
	// Message is the text of a log event: what the job is doing (loading the model, ...).
	Message string `json:"message,omitempty"`
	// System is the name of a system event ("container.started", "model.loaded", ...: ai_train_core/system_events.py) and Data
	// its details; Message says it in words.
	System string         `json:"event,omitempty"`
	Data   map[string]any `json:"data,omitempty"`
	// Traceback is the Python traceback of a failed job (its last part), for diagnosis.
	Traceback string `json:"traceback,omitempty"`
	// Progress events: the Trainer's step, loss (nil when the log has none, or it is not finite), learning rate and epoch,
	// and the seconds since training began.
	Step           *int     `json:"step,omitempty"`
	Loss           *float64 `json:"loss,omitempty"`
	LearningRate   *float64 `json:"learning_rate,omitempty"`
	Epoch          *float64 `json:"epoch,omitempty"`
	ElapsedSeconds *float64 `json:"elapsed_seconds,omitempty"`
	// Model is on the "completed" event (the weights the job saved; nil from an ML sidecar that does not report them) and on
	// the "model_recorded" one, which also has the ModelID of the row that records it.
	Model   *TrainedModelInfo `json:"model,omitempty"`
	ModelID int64             `json:"model_id,omitempty"`
	// Timestamp is when the event happened, in seconds since the Unix epoch.
	Timestamp float64 `json:"timestamp,omitempty"`
}

// TrainedModelInfo describes the weights a completed training job saved (ml_sidecar/ai_train/ai_train_core/trained_model.py:
// describe).
type TrainedModelInfo struct {
	BaseModel string `json:"base_model"`
	ModelSize string `json:"model_size"`
	// Path is the directory of the weights on the ML sidecar's models volume, relative to it ("trained/<job id>"): the
	// `model_path` of an inference request.
	Path      string             `json:"path"`
	Files     []TrainedModelFile `json:"files"`
	SizeBytes int64              `json:"size_bytes"`
	// NumExamples and DatasetSHA256 are of the dataset file the job read; Params are the other training settings.
	NumExamples   int             `json:"num_examples"`
	DatasetSHA256 string          `json:"dataset_sha256"`
	Params        json.RawMessage `json:"params,omitempty"`
}

// TrainedModelFile is one file of a trained model's weights.
type TrainedModelFile struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256,omitempty"`
}

// Finished reports whether the event ends the job.
func (e TrainingEvent) Finished() bool {
	return e.Type == EventStatus && (e.Status == "completed" || e.Status == "failed" || e.Status == "cancelled")
}

// TrainingEventsHook is the callback of Hooks.TrainingEvents. It is called once, when the server starts (after the OnStart hooks,
// before requests are accepted), with the runtime and the channel the events arrive on; it must return quickly (start a goroutine
// that ranges over the channel). Events emitted before it is called wait in the channel. The channel is closed when the hooks are
// removed (Serve returns), which ends that goroutine. Each callback has a channel of its own. The events are shared with the other
// callbacks and the feed: read-only (Data, Model and the pointers too).
type TrainingEventsHook func(rt *gbruntime.Runtime,
	events <-chan TrainingEvent)

// trainingSub is one TrainingEvents callback and the channel that is its own.
type trainingSub struct {
	hook TrainingEventsHook
	ch   chan TrainingEvent
}

var (
	trainingMu   sync.RWMutex
	trainingSubs []trainingSub
)

// installTrainingEvents closes the channels of the previous hooks and makes a new one for each hook of h that wants training
// events. The callbacks get them later, in startTrainingEvents.
func installTrainingEvents(h []Hooks) {
	fresh := make([]trainingSub, 0, len(h))
	for _, set := range h {
		if set.TrainingEvents != nil {
			fresh = append(fresh, trainingSub{
				hook: set.TrainingEvents,
				ch:   make(chan TrainingEvent, TrainingEventsBuffer),
			})
		}
	}
	trainingMu.Lock()
	for _, sub := range trainingSubs {
		close(sub.ch)
	}
	trainingSubs = fresh
	trainingMu.Unlock()
}

// startTrainingEvents hands every callback its channel. A panicking callback is contained.
func startTrainingEvents(rt *gbruntime.Runtime) {
	trainingMu.RLock()
	subs := append([]trainingSub(nil), trainingSubs...)
	trainingMu.RUnlock()
	for _, sub := range subs {
		callTrainingHook(sub.hook,
			rt,
			sub.ch)
	}
}

func callTrainingHook(f TrainingEventsHook,
	rt *gbruntime.Runtime,
	ch <-chan TrainingEvent) {
	defer func() { _ = recover() }()
	f(rt, ch)
}

// EmitTrainingEvent sends ev to the channel of every installed TrainingEvents callback. It never blocks: where a channel is full the
// event is dropped. A no-op when no callback is installed.
func EmitTrainingEvent(ev TrainingEvent) {
	trainingMu.RLock()
	defer trainingMu.RUnlock()
	for _, sub := range trainingSubs {
		select {
		case sub.ch <- ev:
		default:
			log.WithFields(log.Fields{
				"run_id":     ev.RunID,
				"event_type": ev.Type,
			}).Warn("training event queue full, dropping event")
		}
	}
}
