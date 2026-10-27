// Package geo is what gligabao knows about places: the kinds of place, and the geo resolvers that turn the name of a
// place into its coordinates. The remember pipeline uses it (package memory_remember, remember_geo.go): an entity that is
// a place gets its coordinates stored in gb_entity_geo.
//
// A Resolver is the one thing a geo source has to implement; Nominatim (OpenStreetMap) is the default one, Cached wraps
// any resolver with a cache, and an embedding program can register its own (gligabao.ServeOptions.GeoResolver). The package
// depends on nothing else of gligabao.
package geo

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
)

// The kinds of place. A kind says what the place is, not how it is mapped: a country is a country whatever the source calls it.
const (
	KindCountry = "country"
	KindRegion  = "region" // a state, province, county, a named area such as "Silicon Valley" or "the Alps"
	KindCity    = "city"   // a city, town, village, district
	KindPlace   = "place"  // anything smaller or specific: a landmark, a building, an address, a mountain, a lake, ...
)

// Kinds lists the kinds of place, the biggest first.
var Kinds = []string{KindCountry, KindRegion, KindCity, KindPlace}

// NormKind returns the kind that kind names (trimmed, lowercased; a few synonyms accepted), or "" if it is none.
func NormKind(kind string) string {
	switch k := strings.ToLower(strings.TrimSpace(kind)); k {
	case KindCountry, "nation", "state_country":
		return KindCountry
	case KindRegion, "state", "province", "county", "area", "territory", "continent":
		return KindRegion
	case KindCity, "town", "village", "municipality", "settlement", "capital":
		return KindCity
	case KindPlace, "location", "landmark", "address", "building", "poi", "venue", "natural_feature":
		return KindPlace
	}
	return ""
}

// NotPlaceTypes are the entity types that are never a place ("person", "concept", "product", "event"): remember does not ask the
// LLM about them, and query does not attach coordinates to an entity of one of them.
var NotPlaceTypes = map[string]bool{"person": true, "concept": true, "product": true, "event": true}

// ErrNotFound means the resolver knows no place for the query. It is an answer, not a failure of the resolver.
var ErrNotFound = errors.New("geo: place not found")

// Query is what a Resolver is asked for.
type Query struct {
	// Text is what to look for: the name of the place, with whatever disambiguates it ("Paris, France", "Springfield, Illinois").
	Text string
	// Kind is what the place is (see the Kind constants); "" if not known. A resolver may use it to prefer a result of that kind.
	Kind string
	// CountryCode (ISO 3166-1 alpha-2) restricts the search to a country; "" for anywhere.
	CountryCode string
}

// Place is a place a Resolver found.
type Place struct {
	// Latitude and Longitude are in degrees (WGS 84).
	Latitude, Longitude float64
	// Kind is what the place is (see the Kind constants), as far as the resolver can tell; "" if it cannot.
	Kind string
	// DisplayName is the full name of the place ("Paris, Île-de-France, Metropolitan France, France").
	DisplayName string
	// Country (the name), CountryCode (ISO 3166-1 alpha-2, lowercase) and Region (state, province, ...) the place lies in; "" if unknown.
	Country, CountryCode, Region string
	// Type is the resolver's own type of the place ("city", "administrative", "peak", ...); "" if it has none.
	Type string
	// BBox is the bounding box of the place, south, west, north, east in degrees; nil if the resolver gives none.
	BBox *[4]float64
	// Importance is how prominent the place is, 0..1; 0 if the resolver does not say.
	Importance float64
	// Provider is the name of the resolver; Ref is the ID of the place there (e.g. "node/12345"), "" if it has none.
	Provider, Ref string
}

// Validate says whether the place has usable coordinates.
func (p Place) Validate() error {
	switch {
	case math.IsNaN(p.Latitude) || math.IsNaN(p.Longitude):
		return errors.New("geo: coordinates are not numbers")
	case p.Latitude < -90 || p.Latitude > 90:
		return fmt.Errorf("geo: latitude %v is out of range", p.Latitude)
	case p.Longitude < -180 || p.Longitude > 180:
		return fmt.Errorf("geo: longitude %v is out of range", p.Longitude)
	}
	return nil
}

// Resolver turns the name of a place into its coordinates.
type Resolver interface {
	// Name identifies the resolver ("nominatim"); it is stored with what it resolved.
	Name() string
	// Resolve returns the best place for the query, or ErrNotFound if there is none. Any other error means the resolver failed
	// (network, quota, ...): the query may succeed later. It must be safe for concurrent use.
	Resolve(ctx context.Context, q Query) (*Place, error)
}

// Func adapts a function to a Resolver.
type Func struct {
	ResolverName string
	Fn           func(ctx context.Context, q Query) (*Place, error)
}

func (f Func) Name() string { return f.ResolverName }
func (f Func) Resolve(ctx context.Context, q Query) (*Place, error) {
	return f.Fn(ctx, q)
}
