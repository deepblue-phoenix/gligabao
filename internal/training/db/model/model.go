// Package model holds the types shared by package training/db and its backends (sqlite, postgres): the records of the
// training jobs run on the sidecar, the models they trained, the inferences run on those and the training datasets.
// It has no dependencies on the backends so that every one of them can import it. ErrNotFound is the one of
// internal/db/relational/model, so one error means "not found" in every database package.
package model

import relmodel "github.com/deepblue-phoenix/gligabao/internal/db/relational/model"

// ErrNotFound means the requested row does not exist. It is relational's model.ErrNotFound.
var ErrNotFound = relmodel.ErrNotFound

// ErrExists means a row with that name already exists. It is relational's model.ErrExists.
var ErrExists = relmodel.ErrExists
