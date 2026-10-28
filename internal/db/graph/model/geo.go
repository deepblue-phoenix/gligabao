package model

import "time"

// The geo metadata of entities (see package internal/memory/memory_geo): an entity that is a place (a city, a country, a region, a
// landmark, ...) has the coordinates the geo resolver found for it. They are kept apart from gb_entities, in
// gb_entity_geo: one row per geo entity, keyed by the entity's ID.

// NewEntityGeo is the geo metadata to store for an entity (inserted, or replacing the stored one of the entity).
type NewEntityGeo struct {
	EntityID int64
	// Kind is what the place is: "country", "region", "city" or "place" (anything smaller, e.g. a landmark or an address).
	Kind string
	// Query is what the resolver was asked for (the entity's name with the context that disambiguates it, "Paris, France").
	Query string
	// Latitude and Longitude are in degrees (WGS 84): -90..90 and -180..180.
	Latitude, Longitude float64
	// DisplayName is the full name of the place as the resolver gives it.
	DisplayName string
	// Country (the name), CountryCode (ISO 3166-1 alpha-2, lowercase) and Region (state, province, ...) the place lies in, if known.
	Country, CountryCode, Region string
	// PlaceType is the type of the place as the resolver names it ("city", "administrative", "peak", ...).
	PlaceType string
	// BBox is the bounding box of the place, south, west, north, east in degrees; nil if unknown.
	BBox *[4]float64
	// Importance is how prominent the place is according to the resolver, 0..1; 0 if it does not say.
	Importance float64
	// Provider is the resolver that found the place; ProviderRef is the place's ID there (e.g. "node/12345"), if it has one.
	Provider, ProviderRef string
	// Now is the time of the write (created_at on insert, updated_at always).
	Now time.Time
}

// EntityGeo is the stored geo metadata of an entity.
type EntityGeo struct {
	EntityID    int64
	Kind        string
	Query       string
	Latitude    float64
	Longitude   float64
	DisplayName string
	Country     string
	CountryCode string
	Region      string
	PlaceType   string
	BBox        *[4]float64
	Importance  float64
	Provider    string
	ProviderRef string
	CreatedAt   time.Time
	UpdatedAt   time.Time

	// EntityName and EntityType are those of the entity; only set by the listing (graph.ListEntityGeo).
	EntityName string
	EntityType string
}

// ConnectedGeo is a geo entity with the dataset it is in and how connected it is.
type ConnectedGeo struct {
	EntityGeo
	// UserID and Dataset name the dataset of the entity.
	UserID  string
	Dataset string
	// Connections is the number of live (not expired) edges that have the entity as source or target.
	Connections int
}

// GeoStats summarizes the geo entities of a database (those of live datasets).
type GeoStats struct {
	// Entities is the number of entities with geo metadata (a row in gb_entity_geo); AllEntities the number of entities.
	Entities    int
	AllEntities int
	// Top are the most connected geo entities, most connected first.
	Top []ConnectedGeo
}

// NearbyGeo is a geo entity found by a proximity query (graph.NearbyEntityGeo), with its distance from the point asked about.
type NearbyGeo struct {
	EntityGeo
	// DistanceKm is the great-circle distance from the point, in kilometers.
	DistanceKm float64
}
