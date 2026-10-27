package memory_core

import (
	"errors"
	"fmt"
	"math"

	"github.com/deepblue-phoenix/gligabao/internal/geo"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
)

const (
	// DefaultGeoRadiusKm is the radius, in kilometers, within which Query looks for the geo entities near the places of a
	// question when nothing else is set (config geo_radius_km, the geo_radius_km of the request).
	DefaultGeoRadiusKm = config.DefaultGeoRadiusKm
	// MaxGeoRadiusKm is the largest radius that can be set: half the circumference of the Earth (which finds every place).
	MaxGeoRadiusKm = geo.MaxDistanceKm
)

// ErrInvalidGeoRadius means a geo radius is not a number between 0 (exclusive) and MaxGeoRadiusKm.
var ErrInvalidGeoRadius = errors.New("invalid geo radius")

// ValidateGeoRadius returns ErrInvalidGeoRadius unless km is a number greater than 0 and at most MaxGeoRadiusKm.
func ValidateGeoRadius(km float64) error {
	if math.IsNaN(km) || km <= 0 || km > MaxGeoRadiusKm {
		return fmt.Errorf("%w: %v km (it must be greater than 0 and at most %v)", ErrInvalidGeoRadius, km, MaxGeoRadiusKm)
	}
	return nil
}

// WithGeoNearby turns on the proximity search of Query, which is off by default: Query then looks up the geo entities near the
// places of the question and gives them to the LLM that answers as context (see memory_query's query_geo_nearby.go). The places of
// the question that the geo resolver finds and that are not stored yet are stored as entities with coordinates, so that the next
// question with the same places finds them in the database. It only matters when the call has a geo resolver (WithGeoResolver).
func WithGeoNearby(on bool) Option { return func(o *CallOptions) { o.GeoNearby = on } }

// WithGeoRadius sets the radius, in kilometers, of the proximity search of Query (see WithGeoNearby). Without it (or with 0) the
// radius is DefaultGeoRadiusKm; an invalid one makes Query fail (ValidateGeoRadius). It does not turn the search on. Of two options
// the later wins, so a caller's radius overrides a default one.
func WithGeoRadius(km float64) Option { return func(o *CallOptions) { o.GeoRadiusKm = km } }
