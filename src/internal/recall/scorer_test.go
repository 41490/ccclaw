package recall

import (
	"testing"
	"time"
)

func TestScoreActiveSkillHighUsage(t *testing.T) {
	now := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)
	input := ScoreInput{
		UseCount: 10,
		LastUsed: time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), // 2天前
		TaskTags: []string{"git", "commit"},
		NodeTags: []string{"git", "commit", "gpg"},
		Now:      now,
	}
	score := Score(input)
	// use_count_norm = min(10/20, 1) = 0.5 → *0.4 = 0.20
	// recency = 1 - 2/90 = 0.978 → *0.4 = 0.391
	// tag_match = 2/2 = 1.0 → *0.2 = 0.20
	// total ≈ 0.791
	if score < 0.7 || score > 0.9 {
		t.Fatalf("expected score in [0.7,0.9], got %f", score)
	}
}

func TestScoreColdStartHasZeroTagMatch(t *testing.T) {
	now := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)
	withTags := ScoreInput{
		UseCount: 5,
		LastUsed: time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC),
		TaskTags: []string{"git"},
		NodeTags: []string{"git"},
		Now:      now,
	}
	cold := ScoreInput{
		UseCount: 5,
		LastUsed: time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC),
		TaskTags: nil, // cold start: no task tags
		NodeTags: []string{"git"},
		Now:      now,
	}
	scoreWithTags := Score(withTags)
	scoreCold := Score(cold)
	if scoreCold >= scoreWithTags {
		t.Fatalf("cold score (%f) should be less than tagged score (%f)", scoreCold, scoreWithTags)
	}
}

func TestClassifyActive(t *testing.T) {
	now := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)
	lastUsed := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	got := Classify(0.7, lastUsed, now)
	if got != "active" {
		t.Fatalf("expected active, got %q", got)
	}
}

func TestClassifyDormant(t *testing.T) {
	now := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)
	lastUsed := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	got := Classify(0.45, lastUsed, now)
	if got != "dormant" {
		t.Fatalf("expected dormant, got %q", got)
	}
}

func TestClassifyArchived(t *testing.T) {
	now := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)
	oldDate := time.Date(2025, 8, 1, 0, 0, 0, 0, time.UTC) // >180天前
	got := Classify(0.1, oldDate, now)
	if got != "archived" {
		t.Fatalf("expected archived, got %q", got)
	}
}

func TestClassifyDormantNotArchivedIfRecent(t *testing.T) {
	now := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC) // 44天前，score低但<180天
	got := Classify(0.1, recent, now)
	if got != "dormant" {
		t.Fatalf("expected dormant (not archived), got %q", got)
	}
}
