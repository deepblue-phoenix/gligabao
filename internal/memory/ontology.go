package memory

// Ontologies describe the concepts (classes) of a domain and the relationships between them. Loading one parses
// the document, records its metadata in the relational database (gb_ontologies: name, owner, hash, counts) and
// stores the ontology itself, classes as nodes and relationships as edges, in the graph database
// (gb_onto_classes, gb_onto_edges). A loaded ontology can later be updated by name: merged with a new document, or replaced by it (see UpdateOntology). Ontologies are owned by a user and named; names are unique per user. They are
// separate from datasets and do not (yet) take part in remember or recall.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/ontology"

	log "github.com/sirupsen/logrus"
)

var (
	ErrInvalidOntologyName = errors.New("invalid ontology name")
	ErrInvalidOntology     = errors.New("invalid ontology")
	ErrOntologyExists      = errors.New("ontology already exists")
	ErrOntologyNotFound    = errors.New("ontology not found")
)

// OntologyFormatRDFXML is the only supported serialization: OWL in RDF/XML.
const OntologyFormatRDFXML = "rdfxml"

// MaxOntologyBytes is the maximum size of an ontology document.
const MaxOntologyBytes = 8 << 20

// Ontology is the metadata of a loaded ontology.
type Ontology = relational.Ontology

var ontologyNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ResolveOntologyName trims name and checks it: required, 1-64 characters of letters, digits, '.', '_' or '-',
// starting with a letter or digit.
func ResolveOntologyName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("%w: name is required", ErrInvalidOntologyName)
	}
	if !ontologyNameRe.MatchString(name) {
		return "", fmt.Errorf("%w: name must be 1-64 characters of letters, digits, '.', '_' or '-', starting with a letter or digit", ErrInvalidOntologyName)
	}
	return name, nil
}

//---------------------------------------------------------------------------------------------

// preparedOntology is a parsed ontology ready to be stored.
type preparedOntology struct {
	meta    Ontology // everything but the ID, the owner, the name and the timestamps
	classes []graph.OntologyClass
	edges   []graph.OntologyEdge
}

// prepareOntology validates the inputs and parses the document. If description is empty, the ontology's own
// rdfs:comment is used.
func prepareOntology(userID, name, description string, content []byte) (userIDOut, nameOut string, p *preparedOntology, err error) {
	if userID, err = memory_core.ResolveUserID(userID); err != nil {
		return
	}
	if name, err = ResolveOntologyName(name); err != nil {
		return
	}
	if len(content) > MaxOntologyBytes {
		err = fmt.Errorf("%w: larger than %d bytes", ErrInvalidOntology, MaxOntologyBytes)
		return
	}
	o, perr := ontology.Parse(bytes.NewReader(content))
	if perr != nil {
		err = fmt.Errorf("%w: %v", ErrInvalidOntology, perr)
		return
	}
	if description = strings.TrimSpace(description); description == "" {
		description = o.Comment
	}
	sum := sha256.Sum256(content)
	p = &preparedOntology{
		meta: Ontology{
			Description: description, IRI: o.IRI, Title: o.Title,
			Format: OntologyFormatRDFXML, SizeBytes: len(content), SHA256: hex.EncodeToString(sum[:]),
			Classes: len(o.Classes), Properties: o.Properties(), Edges: len(o.Edges),
		},
		classes: make([]graph.OntologyClass, len(o.Classes)),
		edges:   make([]graph.OntologyEdge, len(o.Edges)),
	}
	for i, c := range o.Classes {
		p.classes[i] = graph.OntologyClass{IRI: c.IRI, Name: c.Name, Label: c.Label, Comment: c.Comment, Declared: c.Declared}
	}
	for i, e := range o.Edges {
		p.edges[i] = graph.OntologyEdge{SourceIRI: e.SourceIRI, TargetIRI: e.TargetIRI, Relation: e.Relation, Kind: e.Kind,
			IRI: e.IRI, Label: e.Label, Comment: e.Comment, InverseOf: e.InverseOf}
	}
	return userID, name, p, nil
}

// LoadOntology parses an OWL ontology in RDF/XML and stores it for userID under name. The metadata goes to the
// relational database and the classes and relationships to the graph database. If description is empty, the
// ontology's own rdfs:comment is used. It returns ErrOntologyExists if the user already has an ontology with that
// name, and ErrInvalidOntology (wrapped) if the document cannot be parsed or defines no class. Nothing is kept
// when loading fails.
func LoadOntology(ctx context.Context, d *sql.DB, userID, name, description string, content []byte) (*Ontology, error) {
	userID, name, p, err := prepareOntology(userID, name, description, content)
	if err != nil {
		return nil, err
	}
	p.meta.UserID, p.meta.Name = userID, name
	meta, err := relational.CreateOntology(ctx, d, p.meta)
	if errors.Is(err, relational.ErrExists) {
		return nil, ErrOntologyExists
	}
	if err != nil {
		return nil, err
	}
	if err := graph.LoadOntology(ctx, d, meta.ID, p.classes, p.edges); err != nil {
		// The graph load is atomic; undo the metadata so that the name is free again.
		if derr := relational.DeleteOntology(context.WithoutCancel(ctx), d, meta.ID); derr != nil {
			log.WithError(derr).WithField("ontology", name).Error("load ontology: cleanup after failure")
		}
		return nil, err
	}
	return meta, nil
}

// UpdateOntology changes the ontology stored for userID under name with the one in content. It returns
// ErrOntologyNotFound if there is no such ontology (an update never creates one), and ErrInvalidOntology
// (wrapped) if the document cannot be parsed or defines no class. The ontology keeps its ID, owner, name and
// creation time. There are two modes.
//
// REPLACE (replace = true): the stored ontology is thrown away and the document becomes the ontology: every
// stored class and edge not in the document is deleted, and the metadata is recomputed from the document.
//
// MERGE (replace = false, the default): the document is merged into the stored ontology, and nothing is deleted.
//
//   - Classes are identified by IRI. A class that is not stored is added.
//   - Edges are identified by (source class IRI, target class IRI, kind, property/predicate IRI), so
//     "Trade -hasBuyer-> Buyer" is the same edge whatever its label says. An edge that is not stored is added.
//   - An edge that is already stored is OVERWRITTEN by the one in the document: relation, label, comment and
//     inverse_of all take the new values, even when the new ones are empty (an overwrite does not combine old
//     and new metadata). The same holds for a class the document declares (types as owl:Class or rdfs:Class):
//     its name, label and comment are overwritten.
//   - A class the document only mentions, as an endpoint of an edge, is added if missing but never changes a
//     stored class. Otherwise a fragment such as "Asset -tradedOn-> Market" would blank the label and comment
//     of the stored Asset and Market classes.
//   - Classes and edges the document does not contain are kept, so a merge can add and correct things but not
//     remove them; use replace for that.
//   - Overwriting keeps the stored row (its ID), so whatever stays in the ontology stays connected.
//   - The whole merge is one transaction: it is applied completely or not at all.
//
// The metadata is updated in both modes. The counts (classes, properties, edges) always describe the stored
// ontology after the update, so after a merge they are totals, not the size of the document. size_bytes and
// sha256 always describe the document of the latest update (not the merged whole), and updated_at is set. The
// description, IRI and title are those of the document; after a merge, ones the document does not provide are
// kept from the stored ontology, whereas a replace drops them. An explicit description in the request always wins.
//
// If the update fails, the stored ontology is left as it was.
func UpdateOntology(ctx context.Context, d *sql.DB, userID, name, description string, content []byte, replace bool) (*Ontology, error) {
	userID, name, p, err := prepareOntology(userID, name, description, content)
	if err != nil {
		return nil, err
	}
	old, err := relational.GetOntology(ctx, d, userID, name)
	if errors.Is(err, relational.ErrNotFound) {
		return nil, ErrOntologyNotFound
	}
	if err != nil {
		return nil, err
	}

	// The metadata is written first, and put back if the graph change fails. The graph change is atomic, so in
	// that case the old graph is intact and restoring the old metadata leaves the ontology exactly as it was.
	p.meta.ID, p.meta.UpdatedAt = old.ID, time.Now()
	if !replace {
		// Merge: what the document does not say stays as it is. The counts are provisional until the merge is done.
		if p.meta.Description == "" {
			p.meta.Description = old.Description
		}
		if p.meta.IRI == "" {
			p.meta.IRI = old.IRI
		}
		if p.meta.Title == "" {
			p.meta.Title = old.Title
		}
		p.meta.Classes, p.meta.Properties, p.meta.Edges = old.Classes, old.Properties, old.Edges
	}
	meta, err := relational.UpdateOntology(ctx, d, p.meta)
	if errors.Is(err, relational.ErrNotFound) {
		return nil, ErrOntologyNotFound
	}
	if err != nil {
		return nil, err
	}

	var counts graph.OntologyCounts
	if replace {
		err = graph.LoadOntology(ctx, d, old.ID, p.classes, p.edges)
		counts = graph.OntologyCounts{Classes: len(p.classes), Properties: p.meta.Properties, Edges: len(p.edges)}
	} else {
		counts, err = graph.MergeOntology(ctx, d, old.ID, p.classes, p.edges)
	}
	if err != nil {
		if _, rerr := relational.UpdateOntology(context.WithoutCancel(ctx), d, *old); rerr != nil {
			log.WithError(rerr).WithField("ontology", name).Error("update ontology: restoring metadata after failure")
		}
		return nil, err
	}

	// After a merge the counts are those of the merged ontology, known only now.
	if meta.Classes != counts.Classes || meta.Properties != counts.Properties || meta.Edges != counts.Edges {
		p.meta.Classes, p.meta.Properties, p.meta.Edges = counts.Classes, counts.Properties, counts.Edges
		if meta, err = relational.UpdateOntology(ctx, d, p.meta); err != nil {
			// The graph change is committed already; only the counts in the metadata are stale.
			log.WithError(err).WithField("ontology", name).Error("update ontology: recording counts")
			return nil, err
		}
	}
	return meta, nil
}

// ListOntologies returns userID's ontologies, oldest first.
func ListOntologies(ctx context.Context, d *sql.DB, userID string) ([]Ontology, error) {
	userID, err := memory_core.ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	return relational.ListOntologies(ctx, d, userID)
}
