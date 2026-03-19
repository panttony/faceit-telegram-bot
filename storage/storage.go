package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"faceit-telegram-bot/faceit"
)

type TrackedPlayer struct {
	ScopeID             string    `json:"scope_id,omitempty"`
	UserID              string    `json:"user_id,omitempty"`
	ChatID              int64     `json:"chat_id"`
	PlayerID            string    `json:"player_id"`
	PlayerNickname      string    `json:"player_nickname"`
	ProfileURL          string    `json:"profile_url"`
	Game                string    `json:"game"`
	Avatar              string    `json:"avatar"`
	PhotoPath           string    `json:"photo_path"`
	CurrentElo          int       `json:"current_elo"`
	LastSeenMatchID     string    `json:"last_seen_match_id"`
	LastNotifiedMatchID string    `json:"last_notified_match_id"`
	LastMatchTime       int64     `json:"last_match_time"`
	TrackedSince        time.Time `json:"tracked_since"`
}

type Storage struct {
	mu      sync.RWMutex
	rootDir string
	scopes  map[string]map[string]*TrackedPlayer
}

func ScopeIDForChat(chatID int64) string {
	return strconv.FormatInt(chatID, 10)
}

func NewStorage(rootDir string) *Storage {
	s := &Storage{rootDir: rootDir, scopes: make(map[string]map[string]*TrackedPlayer)}
	_ = os.MkdirAll(filepath.Join(rootDir, "chats"), 0o755)
	_ = s.load()
	_ = s.cleanupAllRenders()
	return s
}

func (s *Storage) RootDir() string { return s.rootDir }

func (s *Storage) scopeDir(scopeID string) string {
	return filepath.Join(s.rootDir, "chats", scopeID)
}

func (s *Storage) scopeFilePath(scopeID string) string {
	return filepath.Join(s.scopeDir(scopeID), "tracked.json")
}

func (s *Storage) load() error {
	for _, rootName := range []string{"chats", "users"} {
		rootPath := filepath.Join(s.rootDir, rootName)
		entries, err := os.ReadDir(rootPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			folderID := entry.Name()
			path := filepath.Join(rootPath, folderID, "tracked.json")
			data, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			players := make(map[string]*TrackedPlayer)
			if err := json.Unmarshal(data, &players); err != nil {
				return fmt.Errorf("unmarshal %s: %w", path, err)
			}
			for playerID, player := range players {
				if player == nil {
					continue
				}
				scopeID := player.ScopeID
				if scopeID == "" {
					if player.ChatID != 0 {
						scopeID = ScopeIDForChat(player.ChatID)
					} else {
						scopeID = folderID
					}
				}
				player.ScopeID = scopeID
				if player.ProfileURL == "" {
					player.ProfileURL = faceit.BuildProfileURL(player.PlayerNickname)
				}
				if player.ChatID == 0 {
					if chatID, err := strconv.ParseInt(scopeID, 10, 64); err == nil {
						player.ChatID = chatID
					}
				}
				if s.scopes[scopeID] == nil {
					s.scopes[scopeID] = make(map[string]*TrackedPlayer)
				}
				if existing, ok := s.scopes[scopeID][playerID]; !ok || shouldReplace(existing, player) {
					s.scopes[scopeID][playerID] = clonePlayer(player)
				}
			}
		}
	}
	return nil
}

func shouldReplace(existing, incoming *TrackedPlayer) bool {
	if existing == nil {
		return true
	}
	if incoming == nil {
		return false
	}
	if existing.LastMatchTime != incoming.LastMatchTime {
		return incoming.LastMatchTime > existing.LastMatchTime
	}
	if existing.LastSeenMatchID == "" && incoming.LastSeenMatchID != "" {
		return true
	}
	if existing.LastNotifiedMatchID == "" && incoming.LastNotifiedMatchID != "" {
		return true
	}
	if existing.PhotoPath == "" && incoming.PhotoPath != "" {
		return true
	}
	return false
}

func (s *Storage) cleanupAllRenders() error {
	for _, rootName := range []string{"chats", "users"} {
		rootPath := filepath.Join(s.rootDir, rootName)
		entries, err := os.ReadDir(rootPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if err := s.cleanupScopeRenders(entry.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Storage) cleanupScopeRenders(scopeID string) error {
	rendersDir := filepath.Join(s.scopeDir(scopeID), "renders")
	entries, err := os.ReadDir(rendersDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(rendersDir, entry.Name())); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	_ = os.Remove(rendersDir)
	return nil
}

func (s *Storage) saveScopeLocked(scopeID string) error {
	if err := os.MkdirAll(s.scopeDir(scopeID), 0o755); err != nil {
		return err
	}
	players := s.scopes[scopeID]
	if players == nil {
		players = map[string]*TrackedPlayer{}
	}
	payload, err := json.MarshalIndent(players, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.scopeFilePath(scopeID), payload, 0o644)
}

func clonePlayer(p *TrackedPlayer) *TrackedPlayer {
	if p == nil {
		return nil
	}
	cp := *p
	return &cp
}

func (s *Storage) AddOrUpdatePlayer(player *TrackedPlayer) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if player == nil {
		return fmt.Errorf("player is nil")
	}
	if player.PlayerID == "" {
		return fmt.Errorf("player id is required")
	}
	if player.ScopeID == "" {
		if player.ChatID == 0 {
			return fmt.Errorf("scope id or chat id is required")
		}
		player.ScopeID = ScopeIDForChat(player.ChatID)
	}
	if player.TrackedSince.IsZero() {
		player.TrackedSince = time.Now()
	}
	if player.ProfileURL == "" {
		player.ProfileURL = faceit.BuildProfileURL(player.PlayerNickname)
	}

	if s.scopes[player.ScopeID] == nil {
		s.scopes[player.ScopeID] = make(map[string]*TrackedPlayer)
	}
	if existing, ok := s.scopes[player.ScopeID][player.PlayerID]; ok {
		if player.PhotoPath == "" {
			player.PhotoPath = existing.PhotoPath
		}
		if player.LastSeenMatchID == "" {
			player.LastSeenMatchID = existing.LastSeenMatchID
		}
		if player.LastNotifiedMatchID == "" {
			player.LastNotifiedMatchID = existing.LastNotifiedMatchID
		}
		if player.LastMatchTime == 0 {
			player.LastMatchTime = existing.LastMatchTime
		}
		if player.CurrentElo == 0 {
			player.CurrentElo = existing.CurrentElo
		}
		if player.TrackedSince.IsZero() {
			player.TrackedSince = existing.TrackedSince
		}
	}
	s.scopes[player.ScopeID][player.PlayerID] = clonePlayer(player)
	return s.saveScopeLocked(player.ScopeID)
}

func (s *Storage) RemovePlayer(scopeID, playerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	players := s.scopes[scopeID]
	if players == nil {
		return fmt.Errorf("scope has no tracked players")
	}
	if _, ok := players[playerID]; !ok {
		return fmt.Errorf("player not found")
	}
	if err := s.removePlayerPhotoFiles(scopeID, playerID); err != nil {
		return err
	}
	if err := s.removePlayerRenderFiles(scopeID, playerID); err != nil {
		return err
	}
	delete(players, playerID)
	if len(players) == 0 {
		s.scopes[scopeID] = map[string]*TrackedPlayer{}
	}
	return s.saveScopeLocked(scopeID)
}

func (s *Storage) UpdatePlayer(player *TrackedPlayer) error {
	return s.AddOrUpdatePlayer(player)
}

func (s *Storage) CountAllTrackedPlayers() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := 0
	for _, players := range s.scopes {
		total += len(players)
	}
	return total
}

func (s *Storage) CountTrackedPlayersByChat(chatID int64) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	scopeID := ScopeIDForChat(chatID)
	return len(s.scopes[scopeID])
}

func (s *Storage) GetPlayer(scopeID, playerID string) (*TrackedPlayer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	players := s.scopes[scopeID]
	if players == nil {
		return nil, fmt.Errorf("scope has no tracked players")
	}
	player, ok := players[playerID]
	if !ok {
		return nil, fmt.Errorf("player not found")
	}
	return clonePlayer(player), nil
}

func (s *Storage) GetAllTrackedPlayers() []*TrackedPlayer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*TrackedPlayer
	for _, players := range s.scopes {
		for _, p := range players {
			result = append(result, clonePlayer(p))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ScopeID != result[j].ScopeID {
			return result[i].ScopeID < result[j].ScopeID
		}
		return strings.ToLower(result[i].PlayerNickname) < strings.ToLower(result[j].PlayerNickname)
	})
	return result
}

func (s *Storage) GetChatPlayers(scopeID string) []*TrackedPlayer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	players := s.scopes[scopeID]
	result := make([]*TrackedPlayer, 0, len(players))
	for _, p := range players {
		result = append(result, clonePlayer(p))
	}
	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i].PlayerNickname) < strings.ToLower(result[j].PlayerNickname)
	})
	return result
}

func (s *Storage) FindChatPlayerByReference(scopeID, reference string) (*TrackedPlayer, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return nil, fmt.Errorf("empty reference")
	}

	players := s.GetChatPlayers(scopeID)
	if index, err := strconv.Atoi(reference); err == nil {
		if index < 1 || index > len(players) {
			return nil, fmt.Errorf("tracked player not found")
		}
		return players[index-1], nil
	}

	nicknameFromURL, isURL := faceit.ExtractNicknameFromProfileURL(reference)
	for _, p := range players {
		if strings.EqualFold(p.PlayerID, reference) {
			return p, nil
		}
		if strings.EqualFold(p.PlayerNickname, reference) {
			return p, nil
		}
		if strings.EqualFold(p.ProfileURL, reference) {
			return p, nil
		}
		if isURL && strings.EqualFold(p.PlayerNickname, nicknameFromURL) {
			return p, nil
		}
	}
	return nil, fmt.Errorf("tracked player not found")
}

func (s *Storage) SavePlayerPhoto(scopeID, playerID string, data []byte, ext string) (string, error) {
	if ext == "" {
		ext = ".jpg"
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}

	photosDir := filepath.Join(s.scopeDir(scopeID), "photos")
	if err := os.MkdirAll(photosDir, 0o755); err != nil {
		return "", err
	}

	tmpFile, err := os.CreateTemp(photosDir, playerID+"_upload_*")
	if err != nil {
		return "", err
	}
	tmpPath := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}()
	if _, err := tmpFile.Write(data); err != nil {
		return "", err
	}
	if err := tmpFile.Close(); err != nil {
		return "", err
	}

	if err := s.removePlayerPhotoFiles(scopeID, playerID); err != nil {
		return "", err
	}

	rel := filepath.ToSlash(filepath.Join("chats", scopeID, "photos", playerID+ext))
	abs := filepath.Join(s.rootDir, filepath.FromSlash(rel))
	if err := os.Rename(tmpPath, abs); err != nil {
		return "", err
	}
	return rel, nil
}

func (s *Storage) RemovePlayerPhoto(scopeID, playerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	players := s.scopes[scopeID]
	if players == nil {
		return fmt.Errorf("scope has no tracked players")
	}
	player, ok := players[playerID]
	if !ok {
		return fmt.Errorf("player not found")
	}
	if err := s.removePlayerPhotoFiles(scopeID, playerID); err != nil {
		return err
	}
	player.PhotoPath = ""
	return s.saveScopeLocked(scopeID)
}

func (s *Storage) removePlayerPhotoFiles(scopeID, playerID string) error {
	photosDir := filepath.Join(s.scopeDir(scopeID), "photos")
	if err := removeGlob(filepath.Join(photosDir, playerID+".*")); err != nil {
		return err
	}
	_ = os.Remove(photosDir)
	return nil
}

func (s *Storage) removePlayerRenderFiles(scopeID, playerID string) error {
	rendersDir := filepath.Join(s.scopeDir(scopeID), "renders")
	if err := removeGlob(filepath.Join(rendersDir, playerID+"_*.jpg")); err != nil {
		return err
	}
	_ = os.Remove(rendersDir)
	return nil
}

func removeGlob(pattern string) error {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	for _, match := range matches {
		if err := os.Remove(match); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *Storage) BuildRenderedImagePath(scopeID, playerID, matchID string) (string, string, error) {
	rendersDir := filepath.Join(s.scopeDir(scopeID), "renders")
	if err := os.MkdirAll(rendersDir, 0o755); err != nil {
		return "", "", err
	}
	fileName := fmt.Sprintf("%s_%s.jpg", playerID, matchID)
	rel := filepath.ToSlash(filepath.Join("chats", scopeID, "renders", fileName))
	abs := filepath.Join(s.rootDir, filepath.FromSlash(rel))
	return rel, abs, nil
}

func (s *Storage) AbsolutePath(rel string) string {
	if rel == "" {
		return ""
	}
	return filepath.Join(s.rootDir, filepath.FromSlash(rel))
}
