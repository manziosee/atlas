package storage

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const AnalyticsRetentionDays = 90

type AnalyticsDay struct {
	Date           string `json:"date"`
	PageViews      int64  `json:"page_views"`
	UniqueVisitors int64  `json:"unique_visitors"`
}

type AnalyticsPage struct {
	Page      string `json:"page"`
	PageViews int64  `json:"page_views"`
}

type Analytics struct {
	Daily []AnalyticsDay  `json:"daily"`
	Pages []AnalyticsPage `json:"pages"`
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
	cutoff := at.UTC().AddDate(0, 0, -(AnalyticsRetentionDays - 1)).Format("2006-01-02")
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: start analytics transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO analytics_page_views(day, page, views) VALUES (?, ?, 1)
ON CONFLICT(day, page) DO UPDATE SET views = views + 1`, day, page); err != nil {
		return fmt.Errorf("storage: record analytics page view: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO analytics_visitors(day, visitor_hash) VALUES (?, ?)`, day, visitorHash); err != nil {
		return fmt.Errorf("storage: record analytics visitor: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM analytics_page_views WHERE day < ?`, cutoff); err != nil {
		return fmt.Errorf("storage: prune analytics page views: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM analytics_visitors WHERE day < ?`, cutoff); err != nil {
		return fmt.Errorf("storage: prune analytics visitors: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit analytics transaction: %w", err)
	}
	return nil
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
	result := Analytics{Daily: make([]AnalyticsDay, days), Pages: []AnalyticsPage{}}
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
SELECT day, COUNT(*) FROM analytics_visitors WHERE day >= ? GROUP BY day`, fromDay)
	if err != nil {
		return Analytics{}, fmt.Errorf("storage: query analytics visitors: %w", err)
	}
	for rows.Next() {
		var day string
		var visitors int64
		if err := rows.Scan(&day, &visitors); err != nil {
			_ = rows.Close()
			return Analytics{}, fmt.Errorf("storage: scan analytics visitors: %w", err)
		}
		if index, ok := dayIndexes[day]; ok {
			result.Daily[index].UniqueVisitors = visitors
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return Analytics{}, fmt.Errorf("storage: read analytics visitors: %w", err)
	}
	if err := rows.Close(); err != nil {
		return Analytics{}, fmt.Errorf("storage: close analytics visitor rows: %w", err)
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

var _ interface {
	RecordPageView(context.Context, time.Time, string, string) error
	Analytics(context.Context, int) (Analytics, error)
} = (*Store)(nil)
