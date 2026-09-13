package servers

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/atomicfile"
	"github.com/KilimcininKorOglu/M365Bridge/pkg/toolcalling"
	"github.com/gofrs/flock"
)

const (
	continuityTTL        = 24 * time.Hour
	continuityRecordMax  = 8 << 20
	continuityDiskMax    = 64 << 20
	continuityRecordsMax = 256
	continuityChainMax   = 128
	checkpointEventsMax  = 4096
	compactionPrefix     = "m365cp1."
)

var (
	errStateNotFound        = errors.New("response state is missing, expired, or unavailable for this caller")
	errStateCorrupt         = errors.New("stored continuity state could not be authenticated or decoded")
	errStateTooLarge        = errors.New("continuity state exceeds the supported retention budget; compact the history or use store=false")
	errCompactionInvalid    = errors.New("compaction state is invalid, expired, or belongs to another caller/session")
	errContinuityInput      = errors.New("invalid continuity input; provide correctly paired tool history and matching session IDs")
	errCompactionIncomplete = errors.New("compaction summary was truncated; request a larger output budget")
)

type storedResponse struct {
	ID        string         `json:"id"`
	ParentID  string         `json:"parent_id,omitempty"`
	SessionID string         `json:"session_id"`
	Input     []any          `json:"input"`
	Output    []any          `json:"output"`
	Response  map[string]any `json:"response,omitempty"`
}

type taskCheckpoint struct {
	Tasks toolcalling.TaskState `json:"tasks"`
	Seen  map[string]bool       `json:"seen"`
}

type compactState struct {
	SessionID    string                `json:"session_id,omitempty"`
	Summary      string                `json:"summary"`
	Tasks        toolcalling.TaskState `json:"tasks"`
	PendingCalls []any                 `json:"pending_calls,omitempty"`
	LoadedTools  []toolcalling.ToolDef `json:"loaded_tools,omitempty"`
	GoalOpen     bool                  `json:"goal_open,omitempty"`
	UserMessages []any                 `json:"user_messages,omitempty"`
}

type stateEnvelope struct {
	Expires int64           `json:"expires"`
	Payload json.RawMessage `json:"payload"`
}

// Records are encrypted and credential-scoped. The OS file lock protects key
// creation/checkpoint transactions across both text and image bridge processes.
type continuityStore struct {
	dir    string
	now    func() time.Time
	retain bool
}

func newContinuityStore(dir string) *continuityStore {
	return &continuityStore{dir: dir, now: time.Now, retain: true}
}

func configuredContinuityStore() *continuityStore {
	dir := os.Getenv("M365_CONTINUITY_DIR")
	if dir == "" {
		dir = "data/continuity"
	}
	store := newContinuityStore(dir)
	store.retain = !strings.EqualFold(os.Getenv("M365_STORE_CONTINUITY"), "false")
	return store
}

func (s *continuityStore) locked(ctx context.Context, run func(cipher.AEAD) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(s.dir, ".lock"))
	defer lock.Close()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ok, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("continuity store is busy")
	}
	aead, err := s.cipher()
	if err != nil {
		return err
	}
	return run(aead)
}

func (s *continuityStore) cipher() (cipher.AEAD, error) {
	path := filepath.Join(s.dir, "state.key")
	key, err := readBoundedState(path, 32)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		err = atomicfile.Write(path, key, 0600)
	}
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("invalid continuity encryption key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func readBoundedState(path string, limit int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if len(data) > limit {
		return nil, errStateTooLarge
	}
	return data, err
}

func statePathKey(kind, owner, id string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + owner + "\x00" + id))
	return kind + "-" + hex.EncodeToString(sum[:]) + ".bin"
}

func (s *continuityStore) seal(aead cipher.AEAD, owner, identity string, value any) ([]byte, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(payload) > continuityRecordMax {
		return nil, errStateTooLarge
	}
	body, err := json.Marshal(stateEnvelope{Expires: s.now().Add(continuityTTL).Unix(), Payload: payload})
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, body, []byte(owner+"\x00"+identity)), nil
}

func (s *continuityStore) open(aead cipher.AEAD, owner, identity string, data []byte, target any) error {
	if len(data) < aead.NonceSize() {
		return errStateCorrupt
	}
	body, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte(owner+"\x00"+identity))
	if err != nil {
		return errStateCorrupt
	}
	var envelope stateEnvelope
	if json.Unmarshal(body, &envelope) != nil {
		return errStateCorrupt
	}
	if envelope.Expires <= s.now().Unix() {
		return errStateNotFound
	}
	if json.Unmarshal(envelope.Payload, target) != nil {
		return errStateCorrupt
	}
	return nil
}

func (s *continuityStore) read(aead cipher.AEAD, kind, owner, id string, target any) error {
	name := statePathKey(kind, owner, id)
	data, err := readBoundedState(filepath.Join(s.dir, name), continuityRecordMax+1024)
	if errors.Is(err, os.ErrNotExist) {
		return errStateNotFound
	}
	if err != nil {
		return err
	}
	return s.open(aead, owner, name, data, target)
}

func (s *continuityStore) write(aead cipher.AEAD, kind, owner, id string, value any) error {
	name := statePathKey(kind, owner, id)
	data, err := s.seal(aead, owner, name, value)
	if err != nil {
		return err
	}
	if err := s.prune(name, int64(len(data))); err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(s.dir, name), data, 0600)
}

type retentionFiles struct {
	evictable []os.FileInfo
	bytes     int64
	count     int
}

func (s *continuityStore) retainedFiles(replacing string) (retentionFiles, error) {
	var retained retentionFiles
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return retained, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".bin") || entry.Name() == replacing {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return retained, err
		}
		if s.now().Sub(info.ModTime()) > continuityTTL {
			if err := os.Remove(filepath.Join(s.dir, info.Name())); err != nil {
				return retained, err
			}
			continue
		}
		retained.bytes += info.Size()
		retained.count++
		if !strings.HasPrefix(info.Name(), "checkpoint-") {
			retained.evictable = append(retained.evictable, info)
		}
	}
	return retained, nil
}

func (s *continuityStore) prune(replacing string, incoming int64) error {
	retained, err := s.retainedFiles(replacing)
	if err != nil {
		return err
	}
	retained.bytes += incoming
	retained.count++
	slices.SortFunc(retained.evictable, func(a, b os.FileInfo) int { return a.ModTime().Compare(b.ModTime()) })
	for retained.count > continuityRecordsMax || retained.bytes > continuityDiskMax {
		if len(retained.evictable) == 0 {
			return errStateTooLarge
		}
		oldest := retained.evictable[0]
		if err := os.Remove(filepath.Join(s.dir, oldest.Name())); err != nil {
			return err
		}
		retained.bytes -= oldest.Size()
		retained.count--
		retained.evictable = retained.evictable[1:]
	}
	return nil
}

func (s *continuityStore) saveResponse(ctx context.Context, owner string, record storedResponse) error {
	return s.locked(ctx, func(aead cipher.AEAD) error { return s.write(aead, "response", owner, record.ID, record) })
}

func (s *continuityStore) history(ctx context.Context, owner, id string) ([]any, string, error) {
	var history []any
	var session string
	err := s.locked(ctx, func(aead cipher.AEAD) error {
		var chain []storedResponse
		seen := map[string]bool{}
		for id != "" {
			if seen[id] || len(chain) >= continuityChainMax {
				return errStateTooLarge
			}
			seen[id] = true
			var record storedResponse
			if err := s.read(aead, "response", owner, id, &record); err != nil {
				return err
			}
			if session == "" {
				session = record.SessionID
			}
			chain = append(chain, record)
			id = record.ParentID
		}
		for _, record := range slices.Backward(chain) {
			history = append(history, record.Input...)
			history = append(history, storedOutput(record)...)
		}
		encoded, err := json.Marshal(history)
		if err != nil {
			return err
		}
		if len(encoded) > continuityRecordMax {
			return errStateTooLarge
		}
		return nil
	})
	return history, session, err
}

func storedOutput(record storedResponse) []any {
	if record.Response != nil {
		if output, ok := record.Response["output"].([]any); ok {
			return output
		}
	}
	return record.Output
}

func (s *continuityStore) response(ctx context.Context, owner, id string) (storedResponse, error) {
	var record storedResponse
	err := s.locked(ctx, func(aead cipher.AEAD) error { return s.read(aead, "response", owner, id, &record) })
	return record, err
}

func (s *continuityStore) deleteResponse(ctx context.Context, owner, id string) error {
	return s.locked(ctx, func(aead cipher.AEAD) error {
		var record storedResponse
		if err := s.read(aead, "response", owner, id, &record); err != nil {
			return err
		}
		return os.Remove(filepath.Join(s.dir, statePathKey("response", owner, id)))
	})
}

func (s *continuityStore) encodeCompaction(ctx context.Context, owner string, value compactState) (string, error) {
	var token string
	err := s.locked(ctx, func(aead cipher.AEAD) error {
		data, err := s.seal(aead, owner, "compaction-v1", value)
		if err != nil {
			return err
		}
		token = compactionPrefix + base64.RawURLEncoding.EncodeToString(data)
		return nil
	})
	return token, err
}

func (s *continuityStore) decodeCompaction(ctx context.Context, owner, token string) (compactState, error) {
	var value compactState
	if len(token) > 2*continuityRecordMax {
		return value, errCompactionInvalid
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, compactionPrefix))
	if err != nil {
		return value, errCompactionInvalid
	}
	err = s.locked(ctx, func(aead cipher.AEAD) error { return s.open(aead, owner, "compaction-v1", data, &value) })
	if err != nil {
		return value, errCompactionInvalid
	}
	return value, nil
}
