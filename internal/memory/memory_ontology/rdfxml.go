package memory_ontology

// A small RDF/XML reader: it turns a document into triples, which ontology.go then interprets as OWL. It covers
// what OWL ontologies written in RDF/XML use: node elements (rdf:about, rdf:ID, rdf:nodeID, blank nodes, typed
// nodes), property elements (rdf:resource, rdf:nodeID, nested nodes, literals, rdf:parseType Resource, Collection
// and Literal), property attributes and xml:base. It does not expand rdf:li, reify statements or interpret
// datatypes and languages (literals are plain strings).

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

const (
	nsRDF   = "http://www.w3.org/1999/02/22-rdf-syntax-ns#"
	nsRDFS  = "http://www.w3.org/2000/01/rdf-schema#"
	nsOWL   = "http://www.w3.org/2002/07/owl#"
	nsXSD   = "http://www.w3.org/2001/XMLSchema#"
	nsXML   = "http://www.w3.org/XML/1998/namespace"
	nsXMLNS = "http://www.w3.org/2000/xmlns/"

	rdfType  = nsRDF + "type"
	rdfFirst = nsRDF + "first"
	rdfRest  = nsRDF + "rest"
	rdfNil   = nsRDF + "nil"
)

// term is the object of a triple: an IRI, a blank node ("_:" prefix) or a literal.
type term struct {
	v   string
	lit bool
}

func (t term) isBlank() bool { return !t.lit && strings.HasPrefix(t.v, "_:") }
func (t term) isIRI() bool   { return !t.lit && !strings.HasPrefix(t.v, "_:") }

type triple struct {
	s, p string
	o    term
}

type rdfParser struct {
	dec     *xml.Decoder
	triples []triple
	nBlank  int
}

// parseRDFXML reads an RDF/XML document into triples, in document order.
func parseRDFXML(r io.Reader) ([]triple, error) {
	p := &rdfParser{dec: xml.NewDecoder(r)}
	for {
		tok, err := p.dec.Token()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("rdf/xml: no root element")
		}
		if err != nil {
			return nil, fmt.Errorf("rdf/xml: %w", err)
		}
		root, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if root.Name.Space == nsRDF && root.Name.Local == "RDF" {
			base := baseOf(root, "")
			_, err = p.readChildren(func(c xml.StartElement) error { _, err := p.node(c, base); return err })
		} else {
			_, err = p.node(root, "") // the RDF/XML root may be a node element
		}
		if err != nil {
			return nil, fmt.Errorf("rdf/xml: %w", err)
		}
		return p.triples, nil
	}
}

func (p *rdfParser) emit(s, pred string, o term) { p.triples = append(p.triples, triple{s, pred, o}) }

func (p *rdfParser) blank() string {
	p.nBlank++
	return fmt.Sprintf("_:b%d", p.nBlank)
}

// readChildren reads the content of the element that was just opened, up to its end tag. fn is called for each
// child element and must consume it entirely. It returns the character data found directly in the element.
func (p *rdfParser) readChildren(fn func(xml.StartElement) error) (string, error) {
	var text strings.Builder
	for {
		tok, err := p.dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if err := fn(t); err != nil {
				return "", err
			}
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			return text.String(), nil
		}
	}
}

// allText reads the content of the element that was just opened as text, nested elements included.
func (p *rdfParser) allText() (string, error) {
	var text strings.Builder
	depth := 1
	for depth > 0 {
		tok, err := p.dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			text.Write(t)
		}
	}
	return text.String(), nil
}

// node parses a node element and returns its subject.
func (p *rdfParser) node(el xml.StartElement, base string) (string, error) {
	base = baseOf(el, base)
	subj := ""
	for _, a := range el.Attr {
		if a.Name.Space != nsRDF {
			continue
		}
		switch a.Name.Local {
		case "about":
			subj = resolve(base, a.Value)
		case "ID":
			subj = resolve(base, "#"+a.Value)
		case "nodeID":
			subj = "_:n_" + a.Value
		}
	}
	if subj == "" {
		subj = p.blank()
	}
	if !(el.Name.Space == nsRDF && el.Name.Local == "Description") {
		p.emit(subj, rdfType, term{v: el.Name.Space + el.Name.Local})
	}
	for _, a := range el.Attr { // property attributes
		switch {
		case a.Name.Space == nsRDF && a.Name.Local == "type":
			p.emit(subj, rdfType, term{v: resolve(base, a.Value)})
		case a.Name.Space == nsRDF, a.Name.Space == nsXML, a.Name.Space == nsXMLNS, a.Name.Space == "" && a.Name.Local == "xmlns":
		case a.Name.Space != "":
			p.emit(subj, a.Name.Space+a.Name.Local, term{v: a.Value, lit: true})
		}
	}
	_, err := p.readChildren(func(c xml.StartElement) error { return p.property(subj, c, base) })
	return subj, err
}

// property parses a property element of subject subj.
func (p *rdfParser) property(subj string, el xml.StartElement, base string) error {
	base = baseOf(el, base)
	pred := el.Name.Space + el.Name.Local
	var resource, nodeID, parseType string
	for _, a := range el.Attr {
		if a.Name.Space != nsRDF {
			continue
		}
		switch a.Name.Local {
		case "resource":
			resource = a.Value
		case "nodeID":
			nodeID = a.Value
		case "parseType":
			parseType = a.Value
		}
	}
	switch {
	case parseType == "Resource":
		obj := p.blank()
		p.emit(subj, pred, term{v: obj})
		_, err := p.readChildren(func(c xml.StartElement) error { return p.property(obj, c, base) })
		return err
	case parseType == "Collection":
		var items []string
		if _, err := p.readChildren(func(c xml.StartElement) error {
			s, err := p.node(c, base)
			items = append(items, s)
			return err
		}); err != nil {
			return err
		}
		head := rdfNil
		for i := len(items) - 1; i >= 0; i-- {
			b := p.blank()
			p.emit(b, rdfFirst, term{v: items[i]})
			p.emit(b, rdfRest, term{v: head})
			head = b
		}
		p.emit(subj, pred, term{v: head})
		return nil
	case parseType == "Literal":
		text, err := p.allText()
		p.emit(subj, pred, term{v: text, lit: true})
		return err
	case resource != "" || nodeID != "":
		obj := resolve(base, resource)
		if nodeID != "" {
			obj = "_:n_" + nodeID
		}
		p.emit(subj, pred, term{v: obj})
		_, err := p.readChildren(func(xml.StartElement) error { return p.dec.Skip() })
		return err
	}
	obj := ""
	text, err := p.readChildren(func(c xml.StartElement) error {
		s, err := p.node(c, base)
		obj = s
		return err
	})
	if err != nil {
		return err
	}
	if obj != "" {
		p.emit(subj, pred, term{v: obj})
	} else {
		p.emit(subj, pred, term{v: text, lit: true})
	}
	return nil
}

// baseOf returns the base IRI in effect inside el.
func baseOf(el xml.StartElement, base string) string {
	for _, a := range el.Attr {
		if a.Name.Space == nsXML && a.Name.Local == "base" {
			return resolve(base, a.Value)
		}
	}
	return base
}

// resolve resolves ref against base (RFC 3986). Without a base, ref is returned as is.
func resolve(base, ref string) string {
	if base == "" {
		return ref
	}
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}
