package storage

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"strings"
	"time"
)

const AnalyticsRetentionDays = 90
const AnalyticsMaxUniqueVisitorsPerDay = 10000
const AnalyticsMaxPagesPerDay = 256

const visitorSketchPrecision = 10
const visitorSketchSize = 1 << visitorSketchPrecision

type AnalyticsDay struct {
	Date                 string `json:"date"`
	PageViews            int64  `json:"page_views"`
	UniqueVisitors       int64  `json:"unique_visitors"`
	UniqueVisitorsCapped bool   `json:"unique_visitors_capped"`
}

type AnalyticsPage struct {
	Page      string `json:"page"`
	PageViews int64  `json:"page_views"`
}

type Analytics struct {
	Daily           []AnalyticsDay    `json:"daily"`
	Pages           []AnalyticsPage   `json:"pages"`
	VisitorSketches map[string][]byte `json:"visitor_sketches,omitempty"`
}

// RecordPageView stores only a daily page aggregate and a day-scoped visitor
// digest; the browser identifier itself is never persisted.
func (s *Store) RecordPageView(ctx context.Context, at time.Time, page, visitorHash string) error {
	if page == "" || len(page) > 128 || !strings.HasPrefix(page, "/") {
		return errors.New("storage: invalid analytics page")
	}
	if len(visitorHash) != 64 {
		return errors.New("storage: invalid analytics visitor hash")
	}
	if _, err := hex.DecodeString(visitorHash); err != nil {
		return errors.New("storage: invalid analytics visitor hash")
	}

	day := at.UTC().Format("2006-01-02")
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: start analytics transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var pageExists bool
	if err := tx.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM analytics_page_views WHERE day = ? AND page = ?)`,
		day, page).Scan(&pageExists); err != nil {
		return fmt.Errorf("storage: check analytics page: %w", err)
	}
	if !pageExists && page != "/other" {
		var pageCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM analytics_page_views WHERE day = ?`, day).Scan(&pageCount); err != nil {
			return fmt.Errorf("storage: count analytics pages: %w", err)
		}
		if pageCount >= AnalyticsMaxPagesPerDay-1 {
			page = "/other"
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO analytics_page_views(day, page, views) VALUES (?, ?, 1)
ON CONFLICT(day, page) DO UPDATE SET views = views + 1`, day, page); err != nil {
		return fmt.Errorf("storage: record analytics page view: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO analytics_visitors(day, visitor_hash)
SELECT ?, ? WHERE COALESCE((SELECT visitors FROM analytics_visitor_counts WHERE day = ?), 0) < ?`,
		day, visitorHash, day, AnalyticsMaxUniqueVisitorsPerDay)
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
	} else {
		var exists bool
		if err := tx.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM analytics_visitors WHERE day = ? AND visitor_hash = ?)`,
			day, visitorHash).Scan(&exists); err != nil {
			return fmt.Errorf("storage: check analytics visitor: %w", err)
		}
		if !exists {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO analytics_visitor_caps(day) VALUES (?)`, day); err != nil {
				return fmt.Errorf("storage: record analytics visitor cap: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit analytics transaction: %w", err)
	}
	return nil
}

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
	if _, err := tx.ExecContext(ctx, `DELETE FROM analytics_page_views WHERE day < ?`, cutoff); err != nil {
		return fmt.Errorf("storage: prune analytics page views: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM analytics_visitors WHERE day < ?`, cutoff); err != nil {
		return fmt.Errorf("storage: prune analytics visitors: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM analytics_visitor_counts WHERE day < ?`, cutoff); err != nil {
		return fmt.Errorf("storage: prune analytics visitor counts: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM analytics_visitor_caps WHERE day < ?`, cutoff); err != nil {
		return fmt.Errorf("storage: prune analytics visitor caps: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit analytics retention transaction: %w", err)
	}
	return nil
}

func (s *Store) Analytics(ctx context.Context, days int) (Analytics, error) {
	if days < 1 || days > AnalyticsRetentionDays {
		return Analytics{}, fmt.Errorf("storage: analytics days must be between 1 and %d", AnalyticsRetentionDays)
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	from := today.AddDate(0, 0, -(days - 1))
	fromDay := from.Format("2006-01-02")
	result := Analytics{
		Daily: make([]AnalyticsDay, days), Pages: []AnalyticsPage{},
		VisitorSketches: make(map[string][]byte, days),
	}
	dayIndexes := make(map[string]int, days)
	for i := range result.Daily {
		day := from.AddDate(0, 0, i).Format("2006-01-02")
		result.Daily[i] = AnalyticsDay{Date: day}
		dayIndexes[day] = i
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT day, SUM(views) FROM analytics_page_views WHERE day >= ? GROUP BY day`, fromDay)
	if err != nil {
		return Analytics{}, fmt.Errorf("storage: query analytics page views: %w", err)
	}
	for rows.Next() {
		var day string
		var views int64
		if err := rows.Scan(&day, &views); err != nil {
			_ = rows.Close()
			return Analytics{}, fmt.Errorf("storage: scan analytics page views: %w", err)
		}
		if index, ok := dayIndexes[day]; ok {
			result.Daily[index].PageViews = views
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return Analytics{}, fmt.Errorf("storage: read analytics page views: %w", err)
	}
	if err := rows.Close(); err != nil {
		return Analytics{}, fmt.Errorf("storage: close analytics page view rows: %w", err)
	}

	rows, err = s.db.QueryContext(ctx, `
SELECT day, visitor_hash FROM analytics_visitors WHERE day >= ? ORDER BY day`, fromDay)
	if err != nil {
		return Analytics{}, fmt.Errorf("storage: query analytics visitors: %w", err)
	}
	for rows.Next() {
		var day, visitorHash string
		if err := rows.Scan(&day, &visitorHash); err != nil {
			_ = rows.Close()
			return Analytics{}, fmt.Errorf("storage: scan analytics visitors: %w", err)
		}
		if index, ok := dayIndexes[day]; ok {
			result.Daily[index].UniqueVisitors++
			sketch := result.VisitorSketches[day]
			if len(sketch) == 0 {
				sketch = make([]byte, visitorSketchSize)
			}
			if err := addVisitorHash(sketch, visitorHash); err != nil {
				_ = rows.Close()
				return Analytics{}, fmt.Errorf("storage: add visitor to analytics sketch: %w", err)
			}
			result.VisitorSketches[day] = sketch
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return Analytics{}, fmt.Errorf("storage: read analytics visitors: %w", err)
	}
	if err := rows.Close(); err != nil {
		return Analytics{}, fmt.Errorf("storage: close analytics visitor rows: %w", err)
	}

	rows, err = s.db.QueryContext(ctx, `SELECT day FROM analytics_visitor_caps WHERE day >= ?`, fromDay)
	if err != nil {
		return Analytics{}, fmt.Errorf("storage: query analytics visitor caps: %w", err)
	}
	for rows.Next() {
		var day string
		if err := rows.Scan(&day); err != nil {
			_ = rows.Close()
			return Analytics{}, fmt.Errorf("storage: scan analytics visitor cap: %w", err)
		}
		if index, ok := dayIndexes[day]; ok {
			result.Daily[index].UniqueVisitorsCapped = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return Analytics{}, fmt.Errorf("storage: read analytics visitor caps: %w", err)
	}
	if err := rows.Close(); err != nil {
		return Analytics{}, fmt.Errorf("storage: close analytics visitor caps: %w", err)
	}

	rows, err = s.db.QueryContext(ctx, `
SELECT page, SUM(views) FROM analytics_page_views
WHERE day >= ? GROUP BY page ORDER BY SUM(views) DESC, page LIMIT 20`, fromDay)
	if err != nil {
		return Analytics{}, fmt.Errorf("storage: query analytics pages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var page AnalyticsPage
		if err := rows.Scan(&page.Page, &page.PageViews); err != nil {
			return Analytics{}, fmt.Errorf("storage: scan analytics pages: %w", err)
		}
		result.Pages = append(result.Pages, page)
	}
	if err := rows.Err(); err != nil {
		return Analytics{}, fmt.Errorf("storage: read analytics pages: %w", err)
	}
	return result, nil
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
	rank := bits.LeadingZeros64(value<<visitorSketchPrecision) + 1
	if rank > 64-visitorSketchPrecision+1 {
		rank = 64 - visitorSketchPrecision + 1
	}
	if byte(rank) > sketch[index] {
		sketch[index] = byte(rank)
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
