package monitor

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"faceit-telegram-bot/config"
	"faceit-telegram-bot/faceit"
	"faceit-telegram-bot/renderer"
	"faceit-telegram-bot/storage"
	"faceit-telegram-bot/telegram"
)

type MatchNotification struct {
	ScopeID      string
	ChatID       int64
	PlayerID     string
	PlayerName   string
	PlayerPhoto  string
	PlayerAvatar string
	ProfileURL   string
	MatchDetails *faceit.MatchDetails
	MatchStats   *faceit.MatchStats
	MapScore     string
	OldElo       int
	NewElo       int
	EloChange    int
}

type renderedNotification struct {
	Notification *MatchNotification
	RenderPath   string
	MatchTime    time.Time
}

type Monitor struct {
	faceitClient *faceit.Client
	storage      *storage.Storage
	bot          *telegram.Bot
	cfg          *config.Config
	stopChan     chan struct{}
}

func NewMonitor(faceitClient *faceit.Client, storage *storage.Storage, bot *telegram.Bot, cfg *config.Config) *Monitor {
	return &Monitor{faceitClient: faceitClient, storage: storage, bot: bot, cfg: cfg, stopChan: make(chan struct{})}
}

func (m *Monitor) Start() {
	log.Printf("monitor started, check interval: %s", m.cfg.CheckInterval)
	go func() {
		m.checkAllPlayers()
		ticker := time.NewTicker(m.cfg.CheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.checkAllPlayers()
			case <-m.stopChan:
				return
			}
		}
	}()
}

func (m *Monitor) Stop() {
	select {
	case <-m.stopChan:
	default:
		close(m.stopChan)
	}
}

func (m *Monitor) checkAllPlayers() {
	players := m.storage.GetAllTrackedPlayers()
	if len(players) == 0 {
		return
	}
	log.Printf("checking %d tracked players", len(players))

	notificationsByChat := make(map[int64][]*MatchNotification)

	for _, player := range players {
		notification, err := m.checkNewMatch(player)
		if err != nil {
			log.Printf("check player %s failed: %v", player.PlayerNickname, err)
			continue
		}
		if notification == nil {
			continue
		}
		notificationsByChat[notification.ChatID] = append(notificationsByChat[notification.ChatID], notification)
	}

	for chatID, notifications := range notificationsByChat {
		sort.SliceStable(notifications, func(i, j int) bool {
			left := nonZero(notifications[i].MatchDetails.FinishedAt, notifications[i].MatchDetails.StartedAt)
			right := nonZero(notifications[j].MatchDetails.FinishedAt, notifications[j].MatchDetails.StartedAt)
			if left == right {
				return strings.ToLower(notifications[i].PlayerName) < strings.ToLower(notifications[j].PlayerName)
			}
			return left < right
		})

		if err := m.sendMatchNotifications(chatID, notifications); err != nil {
			log.Printf("send batched notifications for chat %d failed: %v", chatID, err)
		}
	}
}

func (m *Monitor) checkNewMatch(player *storage.TrackedPlayer) (*MatchNotification, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	matches, err := m.faceitClient.GetPlayerMatches(ctx, player.PlayerID, player.Game, 3)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil
	}
	latest := matches[0]
	if latest.MatchID == "" {
		return nil, nil
	}

	details, err := m.faceitClient.GetMatchDetails(ctx, latest.MatchID)
	if err != nil {
		log.Printf("match details for %s unavailable, fallback to history data: %v", latest.MatchID, err)
		details = &faceit.MatchDetails{
			MatchID:         latest.MatchID,
			Game:            latest.Game,
			CompetitionName: latest.CompetitionName,
			FaceitURL:       latest.FaceitURL,
			Status:          latest.Status,
			StartedAt:       latest.StartedAt,
			FinishedAt:      latest.FinishedAt,
		}
	}

	status := strings.ToUpper(strings.TrimSpace(details.Status))
	if status == "ONGOING" {
		if latest.MatchID != player.LastSeenMatchID {
			player.LastSeenMatchID = latest.MatchID
			_ = m.storage.UpdatePlayer(player)
		}
		return nil, nil
	}

	if latest.MatchID == player.LastNotifiedMatchID {
		if player.LastSeenMatchID != latest.MatchID {
			player.LastSeenMatchID = latest.MatchID
			_ = m.storage.UpdatePlayer(player)
		}
		return nil, nil
	}

	stats, err := m.faceitClient.GetMatchStats(ctx, latest.MatchID, player.PlayerID)
	if err != nil {
		return nil, err
	}

	newElo := player.CurrentElo
	if playerDetails, err := m.faceitClient.GetPlayerDetails(ctx, player.PlayerID); err == nil && playerDetails != nil {
		newElo = playerDetails.Elo
		if player.Avatar == "" {
			player.Avatar = playerDetails.Avatar
		}
		if player.ProfileURL == "" {
			player.ProfileURL = playerDetails.ProfileURL
		}
	} else if err != nil {
		log.Printf("failed to refresh player details for %s: %v", player.PlayerNickname, err)
	}

	notification := &MatchNotification{
		ScopeID:      player.ScopeID,
		ChatID:       player.ChatID,
		PlayerID:     player.PlayerID,
		PlayerName:   player.PlayerNickname,
		PlayerPhoto:  player.PhotoPath,
		PlayerAvatar: player.Avatar,
		ProfileURL:   player.ProfileURL,
		MatchDetails: details,
		MatchStats:   stats,
		MapScore:     formatScoreForPlayer(latest, player.PlayerID),
		OldElo:       player.CurrentElo,
		NewElo:       newElo,
		EloChange:    newElo - player.CurrentElo,
	}

	player.CurrentElo = newElo
	player.LastSeenMatchID = latest.MatchID
	player.LastNotifiedMatchID = latest.MatchID
	player.LastMatchTime = nonZero(details.FinishedAt, latest.FinishedAt, details.StartedAt, latest.StartedAt)
	if err := m.storage.UpdatePlayer(player); err != nil {
		return nil, err
	}

	return notification, nil
}

func (m *Monitor) sendMatchNotifications(chatID int64, notifications []*MatchNotification) error {
	if len(notifications) == 0 {
		return nil
	}

	rendered := make([]*renderedNotification, 0, len(notifications))
	cleanupPaths := make([]string, 0, len(notifications))
	defer func() {
		for _, p := range cleanupPaths {
			cleanupTempRender(p)
		}
	}()

	for _, n := range notifications {
		item, err := m.renderNotification(n)
		if err != nil {
			return err
		}
		rendered = append(rendered, item)
		cleanupPaths = append(cleanupPaths, item.RenderPath)
	}

	caption := m.buildBatchNotificationText(rendered)
	paths := make([]string, 0, len(rendered))
	for _, item := range rendered {
		paths = append(paths, item.RenderPath)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if len(paths) == 1 {
		return m.bot.SendPhoto(ctx, chatID, paths[0], caption)
	}
	return m.bot.SendMediaGroup(ctx, chatID, paths, caption)
}

func (m *Monitor) renderNotification(n *MatchNotification) (*renderedNotification, error) {
	_, renderPath, err := m.storage.BuildRenderedImagePath(n.ScopeID, n.PlayerID, n.MatchStats.MatchID)
	if err != nil {
		return nil, err
	}

	matchTime := time.Unix(nonZero(n.MatchDetails.FinishedAt, n.MatchDetails.StartedAt), 0).In(m.cfg.Timezone)
	photoPath := m.storage.AbsolutePath(n.PlayerPhoto)
	if err := renderer.RenderMatchCard(renderer.CardData{
		PlayerName:      n.PlayerName,
		Map:             n.MatchStats.Map,
		MapScore:        n.MapScore,
		Kills:           n.MatchStats.Kills,
		Deaths:          n.MatchStats.Deaths,
		KD:              n.MatchStats.KD,
		HeadshotsPct:    n.MatchStats.HeadshotsPct,
		Damage:          n.MatchStats.Damage,
		UtilityDamage:   n.MatchStats.UtilityDamage,
		OldElo:          n.OldElo,
		NewElo:          n.NewElo,
		EloChange:       n.EloChange,
		Result:          n.MatchStats.Result,
		MatchTime:       matchTime,
		CompetitionName: n.MatchDetails.CompetitionName,
		PhotoPath:       photoPath,
		PhotoURL:        n.PlayerAvatar,
		OutputPath:      renderPath,
	}); err != nil {
		cleanupTempRender(renderPath)
		return nil, fmt.Errorf("render card: %w", err)
	}

	return &renderedNotification{Notification: n, RenderPath: renderPath, MatchTime: matchTime}, nil
}

func (m *Monitor) buildBatchNotificationText(items []*renderedNotification) string {
	var b strings.Builder
	for i, item := range items {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%d) %s | %s\n", i+1, item.Notification.PlayerName, item.MatchTime.Format("02.01.2006 15:04"))
		if item.Notification.MatchDetails.FaceitURL != "" {
			fmt.Fprintf(&b, "FACEIT Room: %s\n", item.Notification.MatchDetails.FaceitURL)
		}
	}
	return strings.TrimSpace(b.String())
}

func formatScoreForPlayer(match faceit.MatchInfo, playerID string) string {
	return faceit.FormatMatchScore(match, playerID)
}

func cleanupTempRender(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("cleanup render %s failed: %v", path, err)
	}
}

func nonZero(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return time.Now().Unix()
}
