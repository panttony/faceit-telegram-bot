package monitor

import (
	"context"
	"fmt"
	"log"
	"os"
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
	for _, player := range players {
		notification, err := m.checkNewMatch(player)
		if err != nil {
			log.Printf("check player %s failed: %v", player.PlayerNickname, err)
			continue
		}
		if notification == nil {
			continue
		}
		if err := m.sendMatchNotification(notification); err != nil {
			log.Printf("send notification for %s failed: %v", player.PlayerNickname, err)
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

func (m *Monitor) sendMatchNotification(n *MatchNotification) error {
	_, renderPath, err := m.storage.BuildRenderedImagePath(n.ScopeID, n.PlayerID, n.MatchStats.MatchID)
	if err != nil {
		return err
	}
	defer cleanupTempRender(renderPath)

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
		return fmt.Errorf("render card: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := m.bot.SendPhoto(ctx, n.ChatID, renderPath, m.buildNotificationText(n, matchTime)); err != nil {
		return fmt.Errorf("send photo: %w", err)
	}
	return nil
}

func (m *Monitor) buildNotificationText(n *MatchNotification, matchTime time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s | %s\n", n.PlayerName, matchTime.Format("02.01.2006 15:04"))
	if n.MatchDetails.FaceitURL != "" {
		fmt.Fprintf(&b, "FACEIT Room: %s\n", n.MatchDetails.FaceitURL)
	}
	//в случае получения Downloads API токена
	/*if demoURL := firstNonEmpty(n.MatchDetails.DemoURLs); demoURL != "" {
		fmt.Fprintf(&b, "Demo URL: %s", demoURL)
	} else {
		b.WriteString("Demo URL: не удалось получить ссылку на демо")
	}*/
	return b.String()
}

func formatScoreForPlayer(match faceit.MatchInfo, playerID string) string {
	return faceit.FormatMatchScore(match, playerID)
}

func cleanupTempRender(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("cleanup render %s failed: %v", path, err)
	}
}

/*func firstNonEmpty(values []string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}*/

func nonZero(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return time.Now().Unix()
}
