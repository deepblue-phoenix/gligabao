package geo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultNominatimURL is the public Nominatim instance of OpenStreetMap. Its usage policy
// (https://operations.osmfoundation.org/policies/nominatim/) allows at most one request per second and requires an
// identifying User-Agent; Nominatim does both. For more than light use run your own instance (NominatimOptions.BaseURL).
const DefaultNominatimURL = "https://nominatim.openstreetmap.org"

// NominatimProvider is the name of the Nominatim resolver.
const NominatimProvider = "nominatim"

// NominatimOptions configures Nominatim; the zero value is the public instance with its policy kept.
type NominatimOptions struct {
	// BaseURL of the Nominatim server, without a trailing path; "" for DefaultNominatimURL.
	BaseURL string
	// UserAgent identifies the program to the server; "" for "gligabao". Set it to something that tells the operator of a public
	// instance whom to contact.
	UserAgent string
	// Email is sent with each request, as the public instance asks of heavy users; "" for none.
	Email string
	// MinInterval is the least time between two requests; 0 for one second (the policy of the public instance), negative for none
	// (your own instance).
	MinInterval time.Duration
	// Timeout of one request; 0 for 15 seconds.
	Timeout time.Duration
	// Client makes the requests; nil for one with Timeout.
	Client *http.Client
}

type nominatim struct {
	opts NominatimOptions
	base string
	cl   *http.Client

	mu   sync.Mutex // serializes the requests, to keep the interval between them
	last time.Time
}

// NewNominatim returns a resolver that asks a Nominatim server (OpenStreetMap geocoding) for the place.
func NewNominatim(opts NominatimOptions) Resolver {
	n := &nominatim{opts: opts, base: strings.TrimRight(opts.BaseURL, "/"), cl: opts.Client}
	if n.base == "" {
		n.base = DefaultNominatimURL
	}
	if n.opts.UserAgent == "" {
		n.opts.UserAgent = "gligabao"
	}
	if n.opts.MinInterval == 0 {
		n.opts.MinInterval = time.Second
	}
	if n.cl == nil {
		t := opts.Timeout
		if t <= 0 {
			t = 15 * time.Second
		}
		n.cl = &http.Client{Timeout: t}
	}
	return n
}

// Default is the resolver the server uses unless the program registers its own: Nominatim of OpenStreetMap, cached.
func Default() Resolver { return Cached(NewNominatim(NominatimOptions{})) }

func (n *nominatim) Name() string { return NominatimProvider }

// wait blocks until a request may be made (the interval since the last one has passed), or ctx is done.
func (n *nominatim) wait(ctx context.Context) error {
	if n.opts.MinInterval < 0 {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if d := time.Until(n.last.Add(n.opts.MinInterval)); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	n.last = time.Now()
	return nil
}

// nominatimResult is one element of Nominatim's "jsonv2" search answer.
type nominatimResult struct {
	PlaceID     int64    `json:"place_id"`
	OSMType     string   `json:"osm_type"`
	OSMID       int64    `json:"osm_id"`
	Lat         string   `json:"lat"`
	Lon         string   `json:"lon"`
	Category    string   `json:"category"`
	Type        string   `json:"type"`
	AddressType string   `json:"addresstype"`
	PlaceRank   int      `json:"place_rank"`
	Importance  float64  `json:"importance"`
	DisplayName string   `json:"display_name"`
	BoundingBox []string `json:"boundingbox"` // south, north, west, east
	Address     struct {
		Country     string `json:"country"`
		CountryCode string `json:"country_code"`
		State       string `json:"state"`
		Region      string `json:"region"`
		Province    string `json:"province"`
		County      string `json:"county"`
	} `json:"address"`
}

func (n *nominatim) Resolve(ctx context.Context, q Query) (*Place, error) {
	text := strings.TrimSpace(q.Text)
	if text == "" {
		return nil, ErrNotFound
	}
	v := url.Values{}
	v.Set("q", text)
	v.Set("format", "jsonv2")
	v.Set("addressdetails", "1")
	v.Set("limit", "1")
	if cc := strings.ToLower(strings.TrimSpace(q.CountryCode)); cc != "" {
		v.Set("countrycodes", cc)
	}
	if n.opts.Email != "" {
		v.Set("email", n.opts.Email)
	}
	if err := n.wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.base+"/search?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", n.opts.UserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := n.cl.Do(req)
	if err != nil {
		return nil, fmt.Errorf("geo: nominatim: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("geo: nominatim: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("geo: nominatim: %s: %s", resp.Status, truncate(string(body), 200))
	}
	var results []nominatimResult
	if err := json.Unmarshal(body, &results); err != nil {
		return nil, fmt.Errorf("geo: nominatim: decode answer: %w", err)
	}
	if len(results) == 0 {
		return nil, ErrNotFound
	}
	return results[0].place()
}

// place converts a result to a Place.
func (r nominatimResult) place() (*Place, error) {
	lat, err1 := strconv.ParseFloat(r.Lat, 64)
	lon, err2 := strconv.ParseFloat(r.Lon, 64)
	if err1 != nil || err2 != nil {
		return nil, fmt.Errorf("geo: nominatim: bad coordinates %q, %q", r.Lat, r.Lon)
	}
	p := &Place{
		Latitude: lat, Longitude: lon, DisplayName: r.DisplayName, Type: r.Type, Importance: r.Importance,
		Country: r.Address.Country, CountryCode: strings.ToLower(r.Address.CountryCode),
		Region:   firstNonEmpty(r.Address.State, r.Address.Region, r.Address.Province, r.Address.County),
		Provider: NominatimProvider,
		Kind:     nominatimKind(r),
	}
	if r.OSMType != "" && r.OSMID != 0 {
		p.Ref = r.OSMType + "/" + strconv.FormatInt(r.OSMID, 10)
	}
	if len(r.BoundingBox) == 4 {
		var b [4]float64
		ok := true
		for i, s := range r.BoundingBox {
			f, err := strconv.ParseFloat(s, 64)
			if err != nil {
				ok = false
				break
			}
			b[i] = f
		}
		if ok { // Nominatim gives south, north, west, east; the box of a Place is south, west, north, east
			p.BBox = &[4]float64{b[0], b[2], b[1], b[3]}
		}
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return p, nil
}

// nominatimKind maps the address type of a result (or else its place rank) to a kind.
func nominatimKind(r nominatimResult) string {
	switch strings.ToLower(r.AddressType) {
	case "country":
		return KindCountry
	case "state", "region", "province", "state_district", "county", "territory", "continent":
		return KindRegion
	case "city", "town", "village", "hamlet", "municipality", "suburb", "city_district", "borough", "quarter", "neighbourhood", "locality":
		return KindCity
	case "":
	default:
		return KindPlace
	}
	switch { // no address type: the rank says how big the place is (Nominatim: 4 country, 8 state, 12 county, 16 city, 30 building)
	case r.PlaceRank == 0:
		return ""
	case r.PlaceRank <= 4:
		return KindCountry
	case r.PlaceRank <= 12:
		return KindRegion
	case r.PlaceRank <= 22:
		return KindCity
	}
	return KindPlace
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
