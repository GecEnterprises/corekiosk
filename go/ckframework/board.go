package ckframework

import "gopkg.in/guregu/null.v4"

// BoardSource indicates whether the server or the game engine populates a board.
type BoardSource string

const (
	BoardSourceServer BoardSource = "server"
	BoardSourceGame   BoardSource = "game"
)

// SortDirection specifies ascending or descending sort order for a board.
type SortDirection string

const (
	SortAsc  SortDirection = "asc"
	SortDesc SortDirection = "desc"
)

// EvictionPolicy determines how a capped board evicts excess rows.
type EvictionPolicy string

const (
	EvictionNone    EvictionPolicy = "none"
	EvictionFIFO    EvictionPolicy = "fifo"
	EvictionRankCap EvictionPolicy = "rank_cap"
)

// BoardColumnKind describes how a column renders in tablet & launcher UIs.
type BoardColumnKind string

const (
	ColRank      BoardColumnKind = "rank"
	ColText      BoardColumnKind = "text"
	ColNumber    BoardColumnKind = "number"
	ColBadge     BoardColumnKind = "badge"
	ColTimestamp BoardColumnKind = "timestamp"
)

// BoardColumn describes one display column of a BoardSpec.
type BoardColumn struct {
	Key   string          `json:"key"`
	Label string          `json:"label"`
	Kind  BoardColumnKind `json:"kind"`
}

// BoardSpec declares a server- or game-defined curated leaderboard / live wall.
// Operators can delete rows (soft-tombstone) without modifying row contents or
// deleting underlying immutable form submissions.
type BoardSpec struct {
	ID              string         `json:"id"`
	Title           string         `json:"title"`
	Source          BoardSource    `json:"source"`
	Columns         []BoardColumn  `json:"columns"`
	SortField       string         `json:"sort_field,omitempty"`
	SortDir         SortDirection  `json:"sort_dir"`
	MinScore        null.Float     `json:"min_score"`
	Limit           int            `json:"limit,omitempty"`
	LimitSettingKey string         `json:"limit_setting_key,omitempty"`
	MaxRows         int            `json:"max_rows,omitempty"`
	Eviction        EvictionPolicy `json:"eviction"`
	DedupeField     string         `json:"dedupe_field,omitempty"`
	AllowRowDelete  bool           `json:"allow_row_delete"`
	AllowBoardClear bool           `json:"allow_board_clear"`
}

func NewBoard(id, title string) BoardSpec {
	return BoardSpec{
		ID:             id,
		Title:          title,
		Source:         BoardSourceServer,
		SortDir:        SortDesc,
		Limit:          10,
		Eviction:       EvictionNone,
		AllowRowDelete: true,
	}
}

func NewGameBoard(id, title string) BoardSpec {
	b := NewBoard(id, title)
	b.Source = BoardSourceGame
	b.Limit = 30
	return b
}

func RankColumn(label string) BoardColumn {
	return BoardColumn{Key: "_rank", Label: label, Kind: ColRank}
}
func FieldColumn(key, label string) BoardColumn {
	return BoardColumn{Key: key, Label: label, Kind: ColText}
}
func NumberColumn(key, label string) BoardColumn {
	return BoardColumn{Key: key, Label: label, Kind: ColNumber}
}
func BadgeColumn(key, label string) BoardColumn {
	return BoardColumn{Key: key, Label: label, Kind: ColBadge}
}
func TimestampColumn(key, label string) BoardColumn {
	return BoardColumn{Key: key, Label: label, Kind: ColTimestamp}
}

func (b BoardSpec) WithColumns(cols ...BoardColumn) BoardSpec { b.Columns = cols; return b }
func (b BoardSpec) SortBy(field string, dir SortDirection) BoardSpec {
	b.SortField = field
	b.SortDir = dir
	return b
}
func (b BoardSpec) WithMinScore(min float64) BoardSpec { b.MinScore = null.FloatFrom(min); return b }
func (b BoardSpec) FixedLimit(n int) BoardSpec         { b.Limit = n; b.LimitSettingKey = ""; return b }
func (b BoardSpec) LimitFromSetting(key string, fallback int) BoardSpec {
	b.LimitSettingKey = key
	b.Limit = fallback
	return b
}
func (b BoardSpec) FIFO(maxRows int) BoardSpec {
	b.MaxRows = maxRows
	b.Eviction = EvictionFIFO
	return b
}
func (b BoardSpec) DedupeBy(field string) BoardSpec { b.DedupeField = field; return b }
func (b BoardSpec) WithRowDelete(v bool) BoardSpec  { b.AllowRowDelete = v; return b }
func (b BoardSpec) WithBoardClear(v bool) BoardSpec { b.AllowBoardClear = v; return b }
