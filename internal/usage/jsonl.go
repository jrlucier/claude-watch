// Package usage parses Claude Code's local JSONL transcripts to derive
// token/cost data the OAuth usage API doesn't expose: burn rate, per-model
// cost, and time-to-exhaustion forecasts.
package usage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Record is one assistant-message usage event extracted from a JSONL file.
type Record struct {
	Timestamp       time.Time
	Model           string
	InputTok        int64
	OutputTok       int64
	CacheReadTok    int64
	CacheWrite5mTok int64
	CacheWrite1hTok int64
	// MessageID disambiguates duplicate replays (Claude Code occasionally writes
	// the same message id twice across files when a session is forked). The
	// aggregator dedupes on this.
	MessageID string
}

// CostUSD is convenience for callers that want a single number.
func (r Record) CostUSD() float64 {
	return PricingFor(r.Model).CostUSD(r.InputTok, r.OutputTok, r.CacheReadTok, r.CacheWrite5mTok, r.CacheWrite1hTok)
}

// rawLine is the subset of fields we read off each JSONL line. Defensive: every
// field is optional; lines we don't care about (user prompts, tool results,
// summaries) silently miss the assistant+usage guard.
type rawLine struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Message   struct {
		ID    string `json:"id"`
		Role  string `json:"role"`
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheCreation            struct {
				Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
				Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

// ProjectsDir returns the canonical ~/.claude/projects (or
// $CLAUDE_CONFIG_DIR/projects if set).
func ProjectsDir() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "projects"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// Reader walks ~/.claude/projects/**/*.jsonl, returning records incrementally.
// It tracks per-file (mtime, offset) so successive Scan() calls only read new
// bytes — important because a multi-month-old projects tree easily reaches
// hundreds of MB.
type Reader struct {
	dir string

	mu     sync.Mutex
	cursor map[string]fileCursor // path → cursor
}

type fileCursor struct {
	Size    int64
	ModTime time.Time
	// Offset is where to resume the next read. We keep it equal to Size as long
	// as the file grows append-only. If a file shrinks or its mtime moves
	// backwards (truncation / replacement), we reset to zero and reread.
	Offset int64
}

// NewReader creates an empty-cursor reader rooted at dir. Pass an empty dir to
// use ProjectsDir().
func NewReader(dir string) (*Reader, error) {
	if dir == "" {
		d, err := ProjectsDir()
		if err != nil {
			return nil, err
		}
		dir = d
	}
	return &Reader{dir: dir, cursor: map[string]fileCursor{}}, nil
}

// Dir returns the root directory this reader walks.
func (r *Reader) Dir() string { return r.dir }

// Scan returns every assistant-with-usage record appended to any JSONL file
// under r.dir since the previous call. The first call after construction
// returns every record (full backfill) so the daemon's first snapshot already
// has a populated 5h block.
func (r *Reader) Scan() ([]Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []Record
	// MissingOK: a fresh user may have no projects dir yet.
	if _, err := os.Stat(r.dir); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	err := filepath.WalkDir(r.dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable; continue the walk
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".jsonl" {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		cur := r.cursor[path]
		// Resume rules:
		//   - same size as before → nothing new, skip
		//   - grew: read from old offset
		//   - shrunk or rewritten (size<old or mtime<=old but size changed): reset
		if info.Size() == cur.Size && !info.ModTime().After(cur.ModTime) {
			return nil
		}
		offset := cur.Offset
		if info.Size() < cur.Offset {
			offset = 0
		}
		recs, err := readFromOffset(path, offset)
		if err != nil {
			// Skip this file but keep walking — one corrupted JSONL must not
			// take the whole daemon down.
			return nil
		}
		out = append(out, recs...)
		r.cursor[path] = fileCursor{
			Size:    info.Size(),
			ModTime: info.ModTime(),
			Offset:  info.Size(),
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	return out, nil
}

func readFromOffset(path string, offset int64) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return nil, err
		}
	}
	var out []Record
	sc := bufio.NewScanner(f)
	// Some JSONL lines (large tool outputs) push past the default 64KB cap.
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var rl rawLine
		if err := json.Unmarshal(line, &rl); err != nil {
			continue
		}
		if rl.Message.Role != "assistant" {
			continue
		}
		if rl.Message.Usage.InputTokens == 0 && rl.Message.Usage.OutputTokens == 0 &&
			rl.Message.Usage.CacheCreationInputTokens == 0 && rl.Message.Usage.CacheReadInputTokens == 0 {
			continue
		}
		w5 := rl.Message.Usage.CacheCreation.Ephemeral5m
		w1h := rl.Message.Usage.CacheCreation.Ephemeral1h
		// If neither bucket is set but the totals say there was cache creation,
		// fall back to charging it all at the 5-min rate (cheaper, conservative).
		if w5 == 0 && w1h == 0 && rl.Message.Usage.CacheCreationInputTokens > 0 {
			w5 = rl.Message.Usage.CacheCreationInputTokens
		}
		out = append(out, Record{
			Timestamp:       rl.Timestamp,
			Model:           rl.Message.Model,
			InputTok:        rl.Message.Usage.InputTokens,
			OutputTok:       rl.Message.Usage.OutputTokens,
			CacheReadTok:    rl.Message.Usage.CacheReadInputTokens,
			CacheWrite5mTok: w5,
			CacheWrite1hTok: w1h,
			MessageID:       rl.Message.ID,
		})
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("scan %s: %w", path, err)
	}
	return out, nil
}
