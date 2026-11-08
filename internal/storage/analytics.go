package storage

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Privacy-limited usage analytics for the hosted console. Raw browser IDs are
// never stored: callers pass a hash of the ID and the UTC day, so a browser
// cannot be followed from one day to the next. Everything is bounded and
// expires after AnalyticsRetentionDays.

const (
	AnalyticsRetentionDays           = 90
	AnalyticsMaxUniqueVisitorsPerDay = 10000
	AnalyticsMaxPagesPerDay          = 256
	AnalyticsMaxVersionsPerDay       = 64
)

const (
	visitorSketchPrecision = 10
	visitorSketchSize      = 1 << visitorSketchPrecision
)

// Telemetry event kinds.
const (
	EventPageView = "pageview"
	EventConnect  = "connect"
)

// Connection kinds for EventConnect.
const (
	ConnectNode = "node"
	ConnectDemo = "demo"
)

var (
	countryPattern = regexp.MustCompile(`^[A-Z]{2}$`)
	versionPattern = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,32}$`)
)

// TelemetryEvent is one anonymous event from the hosted console.
type TelemetryEvent struct {
	At time.Time
	// VisitorHash is the hex SHA-256 of the UTC day and the browser's random ID.
	VisitorHash string
	Kind        string // EventPageView or EventConnect
	Page        string // normalized path, for page views
	Country     string // ISO 3166-1 alpha-2, or empty when unknown
	Connect     string // ConnectNode or ConnectDemo, for connections
	Version     string // the connected node's version, for connections
}

type AnalyticsDay struct {
	Date                 string `json:"date"`
	PageViews            int64  `json:"page_views"`
	UniqueVisitors       int64  `json:"unique_visitors"`
	UniqueVisitorsCapped bool   `json:"unique_visitors_capped"`
	// Systems counts browsers that connected the console to their own node.
	Systems         int64 `json:"systems"`
	NodeConnections int64 `json:"node_connections"`
	DemoConnections int64 `json:"demo_connections"`
}

type AnalyticsPage struct {
	Page      string `json:"page"`
	PageViews int64  `json:"page_views"`
}

// AnalyticsCountry totals one country over the requested range. Visitors are
// browser-days: a browser seen on three days counts three times.
type AnalyticsCountry struct {
	Country   string `json:"country"`
	PageViews int64  `json:"page_views"`
	Visitors  int64  `json:"visitors"`
}

// AnalyticsVersion counts console connections to nodes running a version.
type AnalyticsVersion struct {
	Version     string `json:"version"`
	Connections int64  `json:"connections"`
}

// Analytics is one node's report. The sketches let a caller merge reports
// from several nodes without counting a browser twice.
type Analytics struct {
	Daily     []AnalyticsDay     `json:"daily"`
	Pages     []AnalyticsPage    `json:"pages"`
	Countries []AnalyticsCountry `json:"countries"`
	Versions  []AnalyticsVersion `json:"versions"`
	Sketches  *AnalyticsSketches `json:"sketches,omitempty"`
}

// AnalyticsSketches are HyperLogLog sketches keyed by day or country.
type AnalyticsSketches struct {
	Visitors  map[string][]byte `json:"visitors,omitempty"`
	Systems   map[string][]byte `json:"systems,omitempty"`
	Countries map[string][]byte `json:"countries,omitempty"`
}

// ValidateTelemetry checks an event before it is stored.
func ValidateTelemetry(e TelemetryEvent) error {
	if len(e.VisitorHash) != 64 {
		return errors.New("storage: invalid analytics visitor hash")
	}
	if _, err := hex.DecodeString(e.VisitorHash); err != nil {
		return errors.New("storage: invalid analytics visitor hash")
	}
	if e.Country != "" && !countryPattern.MatchString(e.Country) {
		return errors.New("storage: country must be an ISO 3166-1 alpha-2 code")
	}
	switch e.Kind {
	case EventPageView:
		if e.Page == "" || len(e.Page) > 128 || !strings.HasPrefix(e.Page, "/") {
			return errors.New("storage: invalid analytics page")
		}
	case EventConnect:
		if e.Connect != ConnectNode && e.Connect != ConnectDemo {
			return errors.New("storage: connection kind must be node or demo")
		}
		if e.Version != "" && !versionPattern.MatchString(e.Version) {
			return errors.New("storage: invalid node version")
		}
	default:
		return errors.New("storage: unknown analytics event")
	}
	return nil
}

// RecordTelemetry stores one event as bounded daily aggregates.
func (s *Store) RecordTelemetry(ctx context.Context, e TelemetryEvent) error {
	if err := ValidateTelemetry(e); err != nil {
		return err
	}
	day := e.At.UTC().Format("2006-01-02")
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: start analytics transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	switch e.Kind {
	case EventPageView:
		page, err := boundedKey(ctx, tx, `SELECT COUNT(*) FROM analytics_page_views WHERE day = ?`,
			`SELECT EXISTS(SELECT 1 FROM analytics_page_views WHERE day = ? AND page = ?)`, day, e.Page, "/other", AnalyticsMaxPagesPerDay)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO analytics_page_views(day, page, views) VALUES (?, ?, 1)
ON CONFLICT(day, page) DO UPDATE SET views = views + 1`, day, page); err != nil {
			return fmt.Errorf("storage: record analytics page view: %w", err)
		}
		if e.Country != "" {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO analytics_countries(day, country, views) VALUES (?, ?, 1)
ON CONFLICT(day, country) DO UPDATE SET views = views + 1`, day, e.Country); err != nil {
				return fmt.Errorf("storage: record analytics country: %w", err)
			}
		}
	case EventConnect:
		version := e.Version
		if version == "" {
			version = "unknown"
		}
		version, err := boundedKey(ctx, tx, `SELECT COUNT(DISTINCT version) FROM analytics_connections WHERE day = ?`,
			`SELECT EXISTS(SELECT 1 FROM analytics_connections WHERE day = ? AND version = ?)`, day, version, "other", AnalyticsMaxVersionsPerDay)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO analytics_connections(day, kind, version, connections) VALUES (?, ?, ?, 1)
ON CONFLICT(day, kind, version) DO UPDATE SET connections = connections + 1`, day, e.Connect, version); err != nil {
			return fmt.Errorf("storage: record analytics connection: %w", err)
		}
	}

	if err := recordVisitor(ctx, tx, day, e); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit analytics transaction: %w", err)
	}
	return nil
}

// boundedKey returns key, or overflow once a day already holds limit-1
// distinct keys, so each day stores at most limit of them.
func boundedKey(ctx context.Context, tx *sql.Tx, countQuery, existsQuery, day, key, overflow string, limit int) (string, error) {
	if key == overflow {
		return key, nil
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, existsQuery, day, key).Scan(&exists); err != nil {
		return "", fmt.Errorf("storage: check analytics key: %w", err)
	}
	if exists {
		return key, nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, countQuery, day).Scan(&count); err != nil {
		return "", fmt.Errorf("storage: count analytics keys: %w", err)
	}
	if count >= limit-1 {
		return overflow, nil
	}
	return key, nil
}

// recordVisitor adds the day's visitor row (up to the daily cap), keeping the
// first known country and marking browsers that connected their own node.
func recordVisitor(ctx context.Context, tx *sql.Tx, day string, e TelemetryEvent) error {
	connected := 0
	if e.Kind == EventConnect && e.Connect == ConnectNode {
		connected = 1
	}
	result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO analytics_visitors(day, visitor_hash, country, connected)
SELECT ?, ?, ?, ? WHERE COALESCE((SELECT visitors FROM analytics_visitor_counts WHERE day = ?), 0) < ?`,
		day, e.VisitorHash, e.Country, connected, day, AnalyticsMaxUniqueVisitorsPerDay)
	if err != nil {
		return fmt.Errorf("storage: record analytics visitor: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("storage: read analytics visitor insert result: %w", err)
	}
	if inserted == 1 {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO analytics_visitor_counts(day, visitors) VALUES (?, 1)
ON CONFLICT(day) DO UPDATE SET visitors = visitors + 1`, day); err != nil {
			return fmt.Errorf("storage: count analytics visitor: %w", err)
		}
		return nil
	}
	updated, err := tx.ExecContext(ctx, `
UPDATE analytics_visitors SET
  country = CASE WHEN country = '' THEN ? ELSE country END,
  connected = MAX(connected, ?)
WHERE day = ? AND visitor_hash = ?`, e.Country, connected, day, e.VisitorHash)
	if err != nil {
		return fmt.Errorf("storage: update analytics visitor: %w", err)
	}
	if n, err := updated.RowsAffected(); err == nil && n == 0 {
		// Not stored because the day is full: remember that counts are capped.
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO analytics_visitor_caps(day) VALUES (?)`, day); err != nil {
			return fmt.Errorf("storage: record analytics visitor cap: %w", err)
		}
	}
	return nil
}

// PruneAnalytics deletes data older than the retention window.
func (s *Store) PruneAnalytics(ctx context.Context, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pruneAnalytics(ctx, at)
}

func (s *Store) pruneAnalytics(ctx context.Context, at time.Time) error {
	cutoff := at.UTC().AddDate(0, 0, -(AnalyticsRetentionDays - 1)).Format("2006-01-02")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: start analytics retention transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, table := range []string{"analytics_page_views", "analytics_visitors", "analytics_visitor_counts", "analytics_visitor_caps", "analytics_countries", "analytics_connections"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE day < ?`, cutoff); err != nil { // #nosec G202 -- table names are constants above.
			return fmt.Errorf("storage: prune %s: %w", table, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit analytics retention transaction: %w", err)
	}
	return nil
}

// Analytics reports the last days days (1..AnalyticsRetentionDays) on this
// node, with exact counts and sketches for merging with other nodes.
func (s *Store) Analytics(ctx context.Context, days int) (Analytics, error) {
	if days < 1 || days > AnalyticsRetentionDays {
		return Analytics{}, fmt.Errorf("storage: analytics days must be between 1 and %d", AnalyticsRetentionDays)
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	from := today.AddDate(0, 0, -(days - 1))
	fromDay := from.Format("2006-01-02")
	result := Analytics{
		Daily: make([]AnalyticsDay, days), Pages: []AnalyticsPage{}, Countries: []AnalyticsCountry{}, Versions: []AnalyticsVersion{},
		Sketches: &AnalyticsSketches{Visitors: map[string][]byte{}, Systems: map[string][]byte{}, Countries: map[string][]byte{}},
	}
	index := make(map[string]int, days)
	for i := range result.Daily {
		day := from.AddDate(0, 0, i).Format("2006-01-02")
		result.Daily[i] = AnalyticsDay{Date: day}
		index[day] = i
	}

	err := s.each(ctx, `SELECT day, SUM(views) FROM analytics_page_views WHERE day >= ? GROUP BY day`, []any{fromDay}, func(rows *sql.Rows) error {
		var day string
		var views int64
		if err := rows.Scan(&day, &views); err != nil {
			return err
		}
		if i, ok := index[day]; ok {
			result.Daily[i].PageViews = views
		}
		return nil
	})
	if err != nil {
		return Analytics{}, fmt.Errorf("storage: read analytics page views: %w", err)
	}

	countries := map[string]*AnalyticsCountry{}
	country := func(code string) *AnalyticsCountry {
		if countries[code] == nil {
			countries[code] = &AnalyticsCountry{Country: code}
		}
		return countries[code]
	}
	err = s.each(ctx, `SELECT day, visitor_hash, country, connected FROM analytics_visitors WHERE day >= ?`, []any{fromDay}, func(rows *sql.Rows) error {
		var day, hash, code string
		var connected bool
		if err := rows.Scan(&day, &hash, &code, &connected); err != nil {
			return err
		}
		i, ok := index[day]
		if !ok {
			return nil
		}
		result.Daily[i].UniqueVisitors++
		if err := addToSketch(result.Sketches.Visitors, day, hash); err != nil {
			return err
		}
		if connected {
			result.Daily[i].Systems++
			if err := addToSketch(result.Sketches.Systems, day, hash); err != nil {
				return err
			}
		}
		if code != "" {
			country(code).Visitors++
			return addToSketch(result.Sketches.Countries, code, hash)
		}
		return nil
	})
	if err != nil {
		return Analytics{}, fmt.Errorf("storage: read analytics visitors: %w", err)
	}

	err = s.each(ctx, `SELECT day FROM analytics_visitor_caps WHERE day >= ?`, []any{fromDay}, func(rows *sql.Rows) error {
		var day string
		if err := rows.Scan(&day); err != nil {
			return err
		}
		if i, ok := index[day]; ok {
			result.Daily[i].UniqueVisitorsCapped = true
		}
		return nil
	})
	if err != nil {
		return Analytics{}, fmt.Errorf("storage: read analytics visitor caps: %w", err)
	}

	err = s.each(ctx, `SELECT country, SUM(views) FROM analytics_countries WHERE day >= ? GROUP BY country`, []any{fromDay}, func(rows *sql.Rows) error {
		var code string
		var views int64
		if err := rows.Scan(&code, &views); err != nil {
			return err
		}
		country(code).PageViews = views
		return nil
	})
	if err != nil {
		return Analytics{}, fmt.Errorf("storage: read analytics countries: %w", err)
	}
	for _, c := range countries {
		result.Countries = append(result.Countries, *c)
	}
	SortCountries(result.Countries)

	versions := map[string]int64{}
	err = s.each(ctx, `SELECT day, kind, version, SUM(connections) FROM analytics_connections WHERE day >= ? GROUP BY day, kind, version`, []any{fromDay}, func(rows *sql.Rows) error {
		var day, kind, version string
		var n int64
		if err := rows.Scan(&day, &kind, &version, &n); err != nil {
			return err
		}
		if i, ok := index[day]; ok {
			if kind == ConnectNode {
				result.Daily[i].NodeConnections += n
				versions[version] += n
			} else {
				result.Daily[i].DemoConnections += n
			}
		}
		return nil
	})
	if err != nil {
		return Analytics{}, fmt.Errorf("storage: read analytics connections: %w", err)
	}
	for v, n := range versions {
		result.Versions = append(result.Versions, AnalyticsVersion{Version: v, Connections: n})
	}
	SortVersions(result.Versions)

	err = s.each(ctx, `
SELECT page, SUM(views) FROM analytics_page_views
WHERE day >= ? GROUP BY page ORDER BY SUM(views) DESC, page LIMIT 20`, []any{fromDay}, func(rows *sql.Rows) error {
		var page AnalyticsPage
		if err := rows.Scan(&page.Page, &page.PageViews); err != nil {
			return err
		}
		result.Pages = append(result.Pages, page)
		return nil
	})
	if err != nil {
		return Analytics{}, fmt.Errorf("storage: read analytics pages: %w", err)
	}
	return result, nil
}

// each runs query and calls fn for every row.
func (s *Store) each(ctx context.Context, query string, args []any, fn func(*sql.Rows) error) error {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// SortCountries orders countries by page views, then visitors, then code.
func SortCountries(list []AnalyticsCountry) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].PageViews != list[j].PageViews {
			return list[i].PageViews > list[j].PageViews
		}
		if list[i].Visitors != list[j].Visitors {
			return list[i].Visitors > list[j].Visitors
		}
		return list[i].Country < list[j].Country
	})
}

// SortVersions orders versions by connections, then name.
func SortVersions(list []AnalyticsVersion) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].Connections != list[j].Connections {
			return list[i].Connections > list[j].Connections
		}
		return list[i].Version < list[j].Version
	})
}

func addToSketch(sketches map[string][]byte, key, visitorHash string) error {
	sketch := sketches[key]
	if sketch == nil {
		sketch = make([]byte, visitorSketchSize)
		sketches[key] = sketch
	}
	return addVisitorHash(sketch, visitorHash)
}

func addVisitorHash(sketch []byte, visitorHash string) error {
	if len(sketch) != visitorSketchSize {
		return fmt.Errorf("storage: invalid analytics visitor sketch size %d", len(sketch))
	}
	hash, err := hex.DecodeString(visitorHash)
	if err != nil || len(hash) < 8 {
		return errors.New("storage: invalid analytics visitor hash")
	}
	value := binary.BigEndian.Uint64(hash[:8])
	index := value >> (64 - visitorSketchPrecision)
	rank := min(bits.LeadingZeros64(value<<visitorSketchPrecision)+1, 64-visitorSketchPrecision+1)
	// rank is at most 64-visitorSketchPrecision+1 = 55, so it fits a byte.
	if r := byte(rank); r > sketch[index] { // #nosec G115 -- bounded above.
		sketch[index] = r
	}
	return nil
}

// MergeSketchMaps merges src into dst key by key.
func MergeSketchMaps(dst, src map[string][]byte) error {
	for key, sketch := range src {
		if len(sketch) != visitorSketchSize {
			return fmt.Errorf("storage: visitor sketches must each be %d bytes", visitorSketchSize)
		}
		current, ok := dst[key]
		if !ok {
			dst[key] = append([]byte(nil), sketch...)
			continue
		}
		merged, err := MergeVisitorSketches(current, sketch)
		if err != nil {
			return err
		}
		dst[key] = merged
	}
	return nil
}

func MergeVisitorSketches(left, right []byte) ([]byte, error) {
	if len(left) != visitorSketchSize || len(right) != visitorSketchSize {
		return nil, fmt.Errorf("storage: visitor sketches must each be %d bytes", visitorSketchSize)
	}
	merged := make([]byte, visitorSketchSize)
	for i := range merged {
		merged[i] = max(left[i], right[i])
	}
	return merged, nil
}

func EstimateUniqueVisitors(sketch []byte) (int64, error) {
	if len(sketch) != visitorSketchSize {
		return 0, fmt.Errorf("storage: visitor sketch must be %d bytes", visitorSketchSize)
	}
	var sum float64
	zeros := 0
	for _, rank := range sketch {
		sum += math.Ldexp(1, -int(rank))
		if rank == 0 {
			zeros++
		}
	}
	if zeros == visitorSketchSize {
		return 0, nil
	}
	m := float64(visitorSketchSize)
	alpha := 0.7213 / (1 + 1.079/m)
	estimate := alpha * m * m / sum
	if estimate <= 2.5*m && zeros > 0 {
		estimate = m * math.Log(m/float64(zeros))
	}
	return int64(math.Round(estimate)), nil
}
