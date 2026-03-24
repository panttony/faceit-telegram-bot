package faceit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	apiKey           string
	downloadsToken   string
	downloadsBaseURL string
	baseURL          string
	httpClient       *http.Client
}

type SearchPlayerResult struct {
	PlayerID   string
	Nickname   string
	Avatar     string
	Game       string
	SkillLevel int
	Country    string
	Verified   bool
	ProfileURL string
}

type PlayerStats struct {
	PlayerID     string
	Nickname     string
	Avatar       string
	ProfileURL   string
	Level        int
	Elo          int
	KDRatio      float64
	WinRate      float64
	MatchesCount int
	TotalMatches int
	AvgKills     float64
	ADR          float64
	HeadshotsPct float64
	Kills        int
	Deaths       int
}

type MatchTeamPlayer struct {
	ID       string
	Nickname string
}

type MatchTeam struct {
	Faction string
	ID      string
	Name    string
	Players []MatchTeamPlayer
}

type MatchInfo struct {
	MatchID         string
	Game            string
	CompetitionName string
	Status          string
	FaceitURL       string
	StartedAt       int64
	FinishedAt      int64
	Winner          string
	Score           map[string]int
	Teams           []MatchTeam
}

type MatchStats struct {
	MatchID       string
	PlayerID      string
	PlayerName    string
	Map           string
	Kills         int
	Deaths        int
	Assists       int
	KD            float64
	Headshots     int
	HeadshotsPct  float64
	ADR           float64
	Damage        int
	UtilityDamage int
	Result        string
}

type MatchDetails struct {
	MatchID         string
	Game            string
	CompetitionName string
	FaceitURL       string
	Status          string
	StartedAt       int64
	FinishedAt      int64
	DemoURLs        []string
}

type demoDownloadResponse struct {
	Payload struct {
		DownloadURL string `json:"download_url"`
	} `json:"payload"`
}

func NewClient(apiKey, downloadsToken, downloadsBaseURL string) *Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
	}

	return &Client{
		apiKey:           apiKey,
		downloadsToken:   downloadsToken,
		downloadsBaseURL: strings.TrimRight(downloadsBaseURL, "/"),
		baseURL:          "https://open.faceit.com/data/v4",
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   60 * time.Second,
		},
	}
}

func (c *Client) doRequest(ctx context.Context, method, rawURL string, body io.Reader, authToken string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	if authToken == "" {
		authToken = c.apiKey
	}
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("FACEIT API %s %s failed with status %d: %s", method, rawURL, resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	return payload, nil
}

func (c *Client) SearchPlayer(ctx context.Context, nickname string) ([]SearchPlayerResult, error) {
	q := url.QueryEscape(strings.TrimSpace(nickname))
	rawURL := fmt.Sprintf("%s/search/players?nickname=%s&limit=20", c.baseURL, q)
	body, err := c.doRequest(ctx, http.MethodGet, rawURL, nil, "")
	if err != nil {
		return nil, err
	}

	var response struct {
		Items []struct {
			PlayerID string `json:"player_id"`
			Nickname string `json:"nickname"`
			Avatar   string `json:"avatar"`
			Country  string `json:"country"`
			Verified bool   `json:"verified"`
			Games    []struct {
				Name       string      `json:"name"`
				SkillLevel interface{} `json:"skill_level"`
			} `json:"games"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}

	results := make([]SearchPlayerResult, 0, len(response.Items))
	for _, item := range response.Items {
		gameName, skill := pickPreferredGame(item.Games)
		if gameName == "" {
			continue
		}
		results = append(results, SearchPlayerResult{
			PlayerID:   item.PlayerID,
			Nickname:   item.Nickname,
			Avatar:     item.Avatar,
			Game:       gameName,
			SkillLevel: skill,
			Country:    item.Country,
			Verified:   item.Verified,
			ProfileURL: BuildProfileURL(item.Nickname),
		})
	}

	needle := strings.ToLower(strings.TrimSpace(nickname))
	sort.SliceStable(results, func(i, j int) bool {
		aExact := strings.EqualFold(results[i].Nickname, needle)
		bExact := strings.EqualFold(results[j].Nickname, needle)
		if aExact != bExact {
			return aExact
		}
		if results[i].Game != results[j].Game {
			return results[i].Game == "cs2"
		}
		return strings.ToLower(results[i].Nickname) < strings.ToLower(results[j].Nickname)
	})

	return results, nil
}

func pickPreferredGame(games []struct {
	Name       string      `json:"name"`
	SkillLevel interface{} `json:"skill_level"`
}) (string, int) {
	for _, game := range games {
		if game.Name == "cs2" {
			return "cs2", toInt(game.SkillLevel)
		}
	}
	for _, game := range games {
		if game.Name == "csgo" {
			return "csgo", toInt(game.SkillLevel)
		}
	}
	return "", 0
}

func (c *Client) GetPlayerDetails(ctx context.Context, playerID string) (*PlayerStats, error) {
	rawURL := fmt.Sprintf("%s/players/%s", c.baseURL, url.PathEscape(playerID))
	body, err := c.doRequest(ctx, http.MethodGet, rawURL, nil, "")
	if err != nil {
		return nil, err
	}

	var response struct {
		PlayerID  string `json:"player_id"`
		Nickname  string `json:"nickname"`
		Avatar    string `json:"avatar"`
		FaceitURL string `json:"faceit_url"`
		Games     map[string]struct {
			SkillLevel interface{} `json:"skill_level"`
			FaceitElo  int         `json:"faceit_elo"`
		} `json:"games"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}

	stats := &PlayerStats{
		PlayerID:   response.PlayerID,
		Nickname:   response.Nickname,
		Avatar:     response.Avatar,
		ProfileURL: replaceLangPlaceholder(response.FaceitURL, response.Nickname),
	}
	if stats.ProfileURL == "" {
		stats.ProfileURL = BuildProfileURL(response.Nickname)
	}

	if game, ok := response.Games["cs2"]; ok {
		stats.Level = toInt(game.SkillLevel)
		stats.Elo = game.FaceitElo
	} else if game, ok := response.Games["csgo"]; ok {
		stats.Level = toInt(game.SkillLevel)
		stats.Elo = game.FaceitElo
	}

	return stats, nil
}

func (c *Client) GetPlayerStats(ctx context.Context, playerID, gameID string) (*PlayerStats, error) {
	details, err := c.GetPlayerDetails(ctx, playerID)
	if err != nil {
		return nil, err
	}

	lifetimeURL := fmt.Sprintf("%s/players/%s/stats/%s", c.baseURL, playerID, gameID)
	lifetimeBody, err := c.doRequest(ctx, "GET", lifetimeURL, nil, "")
	if err == nil {
		var lifetimeResp struct {
			Lifetime map[string]any `json:"lifetime"`
		}

		if json.Unmarshal(lifetimeBody, &lifetimeResp) == nil {
			details.TotalMatches = toInt(lifetimeResp.Lifetime["Total Matches"])
			if details.TotalMatches == 0 {
				details.TotalMatches = toInt(lifetimeResp.Lifetime["Matches"])
			}
		}
	}

	rawURL := fmt.Sprintf("%s/players/%s/games/%s/stats?limit=30", c.baseURL, url.PathEscape(playerID), url.PathEscape(gameID))
	body, err := c.doRequest(ctx, http.MethodGet, rawURL, nil, "")
	if err != nil {
		return nil, err
	}

	var response struct {
		Items []struct {
			Stats map[string]interface{} `json:"stats"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}

	var totalKills, totalDeaths, totalDamage, totalRounds, totalHeadshots, wins int
	matches := 0
	for _, item := range response.Items {
		stats := item.Stats
		matches++
		totalKills += toInt(stats["Kills"])
		totalDeaths += toInt(stats["Deaths"])
		totalDamage += toInt(stats["Damage"])
		totalHeadshots += toInt(stats["Headshots"])
		totalRounds += toInt(stats["Rounds"])
		if toInt(stats["Result"]) == 1 {
			wins++
		}
	}
	if matches > 0 {
		details.MatchesCount = matches
		details.Kills = totalKills
		details.Deaths = totalDeaths
		details.WinRate = float64(wins) / float64(matches) * 100
		details.AvgKills = float64(totalKills) / float64(matches)
		if totalDeaths > 0 {
			details.KDRatio = float64(totalKills) / float64(totalDeaths)
		}
		if totalRounds > 0 {
			details.ADR = float64(totalDamage) / float64(totalRounds)
		}
		if totalKills > 0 {
			details.HeadshotsPct = float64(totalHeadshots) / float64(totalKills) * 100
		}
	}

	return details, nil
}

func (c *Client) GetPlayerMatches(ctx context.Context, playerID, gameID string, limit int) ([]MatchInfo, error) {
	rawURL := fmt.Sprintf("%s/players/%s/history?game=%s&limit=%d", c.baseURL, url.PathEscape(playerID), url.QueryEscape(gameID), limit)
	body, err := c.doRequest(ctx, http.MethodGet, rawURL, nil, "")
	if err != nil {
		return nil, err
	}

	var response struct {
		Items []struct {
			MatchID         string `json:"match_id"`
			GameID          string `json:"game_id"`
			CompetitionName string `json:"competition_name"`
			FaceitURL       string `json:"faceit_url"`
			Status          string `json:"status"`
			StartedAt       int64  `json:"started_at"`
			FinishedAt      int64  `json:"finished_at"`
			Results         struct {
				Winner string         `json:"winner"`
				Score  map[string]int `json:"score"`
			} `json:"results"`
			Teams map[string]struct {
				TeamID   string `json:"team_id"`
				Nickname string `json:"nickname"`
				Players  []struct {
					PlayerID string `json:"player_id"`
					Nickname string `json:"nickname"`
				} `json:"players"`
			} `json:"teams"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}

	matches := make([]MatchInfo, 0, len(response.Items))
	for _, item := range response.Items {
		match := MatchInfo{
			MatchID:         item.MatchID,
			Game:            item.GameID,
			CompetitionName: item.CompetitionName,
			Status:          item.Status,
			FaceitURL:       replaceLangPlaceholder(item.FaceitURL, ""),
			StartedAt:       item.StartedAt,
			FinishedAt:      item.FinishedAt,
			Winner:          item.Results.Winner,
			Score:           item.Results.Score,
		}
		for faction, team := range item.Teams {
			teamInfo := MatchTeam{
				Faction: faction,
				ID:      team.TeamID,
				Name:    team.Nickname,
				Players: make([]MatchTeamPlayer, 0, len(team.Players)),
			}
			for _, p := range team.Players {
				teamInfo.Players = append(teamInfo.Players, MatchTeamPlayer{ID: p.PlayerID, Nickname: p.Nickname})
			}
			match.Teams = append(match.Teams, teamInfo)
		}
		matches = append(matches, match)
	}

	return matches, nil
}

func (c *Client) GetMatchDetails(ctx context.Context, matchID string) (*MatchDetails, error) {
	rawURL := fmt.Sprintf("%s/matches/%s", c.baseURL, url.PathEscape(matchID))
	body, err := c.doRequest(ctx, http.MethodGet, rawURL, nil, "")
	if err != nil {
		return nil, err
	}

	var response struct {
		MatchID         string   `json:"match_id"`
		Game            string   `json:"game"`
		CompetitionName string   `json:"competition_name"`
		FaceitURL       string   `json:"faceit_url"`
		Status          string   `json:"status"`
		StartedAt       int64    `json:"started_at"`
		FinishedAt      int64    `json:"finished_at"`
		DemoURLs        []string `json:"demo_url"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}

	return &MatchDetails{
		MatchID:         response.MatchID,
		Game:            response.Game,
		CompetitionName: response.CompetitionName,
		FaceitURL:       replaceLangPlaceholder(response.FaceitURL, ""),
		Status:          response.Status,
		StartedAt:       response.StartedAt,
		FinishedAt:      response.FinishedAt,
		DemoURLs:        response.DemoURLs,
	}, nil
}

func (c *Client) GetMatchStats(ctx context.Context, matchID, playerID string) (*MatchStats, error) {
	rawURL := fmt.Sprintf("%s/matches/%s/stats", c.baseURL, url.PathEscape(matchID))
	body, err := c.doRequest(ctx, http.MethodGet, rawURL, nil, "")
	if err != nil {
		return nil, err
	}

	var response struct {
		Rounds []struct {
			RoundStats map[string]string `json:"round_stats"`
			Teams      []struct {
				TeamID  string `json:"team_id"`
				Players []struct {
					PlayerID    string                 `json:"player_id"`
					Nickname    string                 `json:"nickname"`
					PlayerStats map[string]interface{} `json:"player_stats"`
				} `json:"players"`
			} `json:"teams"`
		} `json:"rounds"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	if len(response.Rounds) == 0 {
		return nil, fmt.Errorf("match %s has no rounds", matchID)
	}

	round := response.Rounds[0]
	stats := &MatchStats{MatchID: matchID, Map: NormalizeMapName(round.RoundStats["Map"])}
	for _, team := range round.Teams {
		for _, player := range team.Players {
			if player.PlayerID != playerID {
				continue
			}
			stats.PlayerID = player.PlayerID
			stats.PlayerName = player.Nickname
			stats.Kills = toInt(player.PlayerStats["Kills"])
			stats.Deaths = toInt(player.PlayerStats["Deaths"])
			stats.Assists = toInt(player.PlayerStats["Assists"])
			stats.Headshots = toInt(player.PlayerStats["Headshots"])
			stats.Damage = toInt(player.PlayerStats["Damage"])
			stats.UtilityDamage = toInt(player.PlayerStats["Utility Damage"])
			stats.KD = toFloat64(player.PlayerStats["K/D Ratio"])
			stats.HeadshotsPct = toFloat64(player.PlayerStats["Headshots %"])
			stats.ADR = toFloat64(player.PlayerStats["ADR"])
			if round.RoundStats["Winner"] == team.TeamID {
				stats.Result = "win"
			} else {
				stats.Result = "loss"
			}
			return stats, nil
		}
	}

	return nil, fmt.Errorf("player %s not found in match %s stats", playerID, matchID)
}

func (c *Client) ResolveDemoDownloadURL(ctx context.Context, resourceURL string) (string, error) {
	if strings.TrimSpace(resourceURL) == "" {
		return "", fmt.Errorf("resource url is empty")
	}
	if strings.TrimSpace(c.downloadsToken) == "" {
		return "", fmt.Errorf("downloads token is not configured")
	}
	endpoint := c.downloadsBaseURL + "/download/v2/demos/download"
	payload, _ := json.Marshal(map[string]string{"resource_url": resourceURL})
	body, err := c.doRequest(ctx, http.MethodPost, endpoint, bytes.NewReader(payload), c.downloadsToken)
	if err != nil {
		return "", err
	}
	var response demoDownloadResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", err
	}
	if response.Payload.DownloadURL == "" {
		return "", fmt.Errorf("download url is empty")
	}
	return response.Payload.DownloadURL, nil
}

func FormatMatchScore(match MatchInfo, playerID string) string {
	if len(match.Score) == 0 {
		return ""
	}

	playerFaction := ""
	for _, team := range match.Teams {
		for _, player := range team.Players {
			if player.ID == playerID {
				playerFaction = team.Faction
				break
			}
		}
		if playerFaction != "" {
			break
		}
	}

	if playerFaction != "" {
		playerScore, ok := match.Score[playerFaction]
		if ok {
			for faction, score := range match.Score {
				if faction == playerFaction {
					continue
				}
				return fmt.Sprintf("%d-%d", playerScore, score)
			}
			return fmt.Sprintf("%d", playerScore)
		}
	}

	if score1, ok1 := match.Score["faction1"]; ok1 {
		if score2, ok2 := match.Score["faction2"]; ok2 {
			return fmt.Sprintf("%d-%d", score1, score2)
		}
	}

	keys := make([]string, 0, len(match.Score))
	for key := range match.Score {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) >= 2 {
		return fmt.Sprintf("%d-%d", match.Score[keys[0]], match.Score[keys[1]])
	}
	if len(keys) == 1 {
		return fmt.Sprintf("%d", match.Score[keys[0]])
	}
	return ""
}

func BuildProfileURL(nickname string) string {
	nickname = strings.TrimSpace(nickname)
	if nickname == "" {
		return ""
	}
	return "https://www.faceit.com/en/players/" + url.PathEscape(nickname)
}

func ExtractNicknameFromProfileURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", false
	}
	host := strings.ToLower(u.Host)
	if !strings.Contains(host, "faceit.com") {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] == "players" && i+1 < len(parts) {
			nickname, err := url.PathUnescape(parts[i+1])
			if err != nil {
				return "", false
			}
			return nickname, nickname != ""
		}
	}
	return "", false
}

func replaceLangPlaceholder(rawURL, nickname string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	rawURL = strings.ReplaceAll(rawURL, "{lang}", "en")
	if strings.Contains(rawURL, "/players/") && nickname != "" && strings.HasSuffix(rawURL, "/players/") {
		rawURL += url.PathEscape(nickname)
	}
	return rawURL
}

func NormalizeMapName(raw string) string {
	name := strings.TrimSpace(raw)
	name = strings.TrimPrefix(name, "de_")
	name = strings.TrimPrefix(name, "cs_")
	if name == "" {
		return "Unknown"
	}
	if strings.EqualFold(name, "dust2") {
		return "Dust2"
	}
	parts := strings.Split(name, "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
	}
	return strings.Join(parts, " ")
}

func FileExtensionFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	ext := path.Ext(u.Path)
	if ext == ".zst" {
		return ".dem.zst"
	}
	return ext
}

func toInt(value any) int {
	if value == nil {
		return 0
	}
	switch v := value.(type) {
	case float64:
		return int(v)
	case float32:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return 0
		}
		i, err := strconv.Atoi(v)
		if err == nil {
			return i
		}
		f, _ := strconv.ParseFloat(v, 64)
		return int(f)
	default:
		return 0
	}
}

func toFloat64(value any) float64 {
	if value == nil {
		return 0
	}
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return 0
		}
		f, _ := strconv.ParseFloat(v, 64)
		return f
	default:
		return 0
	}
}
