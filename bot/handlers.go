package bot

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"faceit-telegram-bot/config"
	"faceit-telegram-bot/faceit"
	"faceit-telegram-bot/storage"
	"faceit-telegram-bot/telegram"
)

type BotHandler struct {
	Bot           *telegram.Bot
	faceit        *faceit.Client
	storage       *storage.Storage
	cfg           *config.Config
	mu            sync.Mutex
	pendingPhoto  map[string]string
	commandWindow map[string]time.Time
}

func NewBotHandler(token string, faceitClient *faceit.Client, store *storage.Storage, cfg *config.Config) (*BotHandler, error) {
	bot, err := telegram.NewBot(token)
	if err != nil {
		return nil, err
	}
	log.Printf("telegram authorized as @%s", bot.Self.UserName)
	return &BotHandler{
		Bot:           bot,
		faceit:        faceitClient,
		storage:       store,
		cfg:           cfg,
		pendingPhoto:  make(map[string]string),
		commandWindow: make(map[string]time.Time),
	}, nil
}

func (h *BotHandler) Start(ctx context.Context) {
	offset := 0
	for {
		if ctx.Err() != nil {
			return
		}
		updates, err := h.Bot.GetUpdates(ctx, offset, 50)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("telegram getUpdates failed: %v", err)
			time.Sleep(3 * time.Second)
			continue
		}
		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			if update.Message == nil || update.Message.From == nil || update.Message.From.IsBot {
				continue
			}
			msg := update.Message
			go h.handleMessage(msg)
		}
	}
}

func (h *BotHandler) handleMessage(message *telegram.Message) {
	if len(message.Photo) > 0 && !message.IsCommand() {
		h.handlePhotoMessage(message)
		return
	}
	if !message.IsCommand() {
		return
	}

	command := message.Command()
	if wait, ok := h.commandCooldownRemaining(message, command); ok {
		h.reply(message.Chat.ID, fmt.Sprintf("Слишком часто. Подожди еще %s перед /%s.", wait, command))
		return
	}

	switch command {
	case "start":
		h.handleStart(message)
	case "help":
		h.handleHelp(message)
	case "track":
		h.handleTrack(message)
	case "untrack":
		h.handleUntrack(message)
	case "list":
		h.handleList(message)
	case "setphoto":
		h.handleSetPhoto(message)
	case "removephoto", "deletephoto":
		h.handleRemovePhoto(message)
	case "recent":
		h.handleRecent(message)
	case "stats":
		h.handleStats(message)
	default:
		h.reply(message.Chat.ID, "Неизвестная команда. Используй /help.")
	}
}

func (h *BotHandler) handleStart(message *telegram.Message) {
	text := strings.Join([]string{
		"Привет. Я отслеживаю последние FACEIT-матчи и присылаю карточку со статистикой.",
		"",
		"Основные команды:",
		"/track <FACEIT link или nickname> - начать отслеживание игрока (максимум 15 игроков на чат)",
		"/list - список отслеживаемых игроков в этом чате",
		"/setphoto <номер из /list | FACEIT link | player_id | nickname> - привязать или заменить фото игрока",
		"/removephoto <номер из /list | FACEIT link | player_id | nickname> - удалить сохраненное фото",
		"/recent <номер из /list | FACEIT link | nickname> - последний матч игрока",
		"/stats <номер из /list | FACEIT link | nickname> - краткая статистика по игроку",
		"/untrack <номер из /list | FACEIT link | player_id | nickname> - убрать из отслеживания",
		"",
		"Лучше использовать номер из /list или ссылку на профиль FACEIT.",
	}, "\n")
	h.reply(message.Chat.ID, text)
}

func (h *BotHandler) handleHelp(message *telegram.Message) {
	h.handleStart(message)
}

func (h *BotHandler) handleTrack(message *telegram.Message) {
	ref := strings.TrimSpace(message.CommandArguments())
	if ref == "" {
		h.reply(message.Chat.ID, "Использование: /track <FACEIT link или nickname>")
		return
	}
	resolvedNickname := ref
	if nickname, ok := faceit.ExtractNicknameFromProfileURL(ref); ok {
		resolvedNickname = nickname
	}
	players, err := h.faceit.SearchPlayer(context.Background(), resolvedNickname)
	if err != nil {
		h.reply(message.Chat.ID, fmt.Sprintf("Ошибка FACEIT: %v", err))
		return
	}
	selected, variants, err := choosePlayer(players, resolvedNickname)
	if err != nil {
		h.reply(message.Chat.ID, err.Error())
		return
	}
	if len(variants) > 1 {
		h.reply(message.Chat.ID, renderVariants(resolvedNickname, variants))
		return
	}

	scopeID := storage.ScopeIDForChat(message.Chat.ID)
	ownerUserID := fmt.Sprintf("%d", message.From.ID)
	if existing, _ := h.storage.GetPlayer(scopeID, selected.PlayerID); existing != nil {
		h.reply(message.Chat.ID, fmt.Sprintf("%s уже отслеживается.", existing.PlayerNickname))
		return
	}
	if h.storage.CountTrackedPlayersByChat(message.Chat.ID) >= h.cfg.MaxTrackedPerChat {
		h.reply(message.Chat.ID, fmt.Sprintf("В этом чате уже достигнут лимит: %d игроков. Удали кого-то через /untrack", h.cfg.MaxTrackedPerChat))
		return
	}
	if h.storage.CountAllTrackedPlayers() >= h.cfg.MaxTrackedGlobal {
		h.reply(message.Chat.ID, fmt.Sprintf("Достигнут глобальный лимит: %d игроков. Удали кого-то через /untrack", h.cfg.MaxTrackedGlobal))
		return
	}

	details, err := h.faceit.GetPlayerDetails(context.Background(), selected.PlayerID)
	if err != nil {
		h.reply(message.Chat.ID, fmt.Sprintf("Не удалось получить профиль игрока: %v", err))
		return
	}

	lastSeen, lastNotified, lastTime := h.initialTrackingState(selected.PlayerID, selected.Game)
	tracked := &storage.TrackedPlayer{
		ScopeID:             scopeID,
		UserID:              ownerUserID,
		ChatID:              message.Chat.ID,
		PlayerID:            selected.PlayerID,
		PlayerNickname:      details.Nickname,
		ProfileURL:          firstNonEmpty(details.ProfileURL, selected.ProfileURL, faceit.BuildProfileURL(details.Nickname)),
		Game:                selected.Game,
		Avatar:              details.Avatar,
		CurrentElo:          details.Elo,
		LastSeenMatchID:     lastSeen,
		LastNotifiedMatchID: lastNotified,
		LastMatchTime:       lastTime,
		TrackedSince:        time.Now(),
	}
	if err := h.storage.AddOrUpdatePlayer(tracked); err != nil {
		h.reply(message.Chat.ID, fmt.Sprintf("Не удалось сохранить игрока: %v", err))
		return
	}

	text := strings.Join([]string{
		fmt.Sprintf("Отслеживание включено: %s", tracked.PlayerNickname),
		fmt.Sprintf("Player ID: %s", tracked.PlayerID),
		fmt.Sprintf("Profile: %s", tracked.ProfileURL),
		fmt.Sprintf("Current ELO: %d", tracked.CurrentElo),
		"",
		"Если хочешь свою картинку игрока на карточке - используй /setphoto с номером из /list и потом загрузи фото.",
	}, "\n")
	h.reply(message.Chat.ID, text)
}

func (h *BotHandler) initialTrackingState(playerID, game string) (lastSeen, lastNotified string, lastTime int64) {
	matches, err := h.faceit.GetPlayerMatches(context.Background(), playerID, game, 1)
	if err != nil || len(matches) == 0 {
		return "", "", 0
	}
	latest := matches[0]
	lastSeen = latest.MatchID
	lastTime = firstNonZero(latest.FinishedAt, latest.StartedAt)
	if strings.EqualFold(latest.Status, "FINISHED") || latest.Status == "" {
		lastNotified = latest.MatchID
	}
	return lastSeen, lastNotified, lastTime
}

func (h *BotHandler) handleSetPhoto(message *telegram.Message) {
	ref := strings.TrimSpace(message.CommandArguments())
	if ref == "" {
		h.reply(message.Chat.ID, "Использование: /setphoto <номер из /list | FACEIT link | player_id | nickname>")
		return
	}
	scopeID := storage.ScopeIDForChat(message.Chat.ID)
	player, err := h.storage.FindChatPlayerByReference(scopeID, ref)
	if err != nil {
		h.reply(message.Chat.ID, "Сначала добавь игрока через /track в этом чате, потом ставь фото.")
		return
	}
	h.setPendingPhoto(pendingPhotoKey(message.Chat.ID, message.From.ID), player.PlayerID)
	if player.PhotoPath != "" {
		h.reply(message.Chat.ID, fmt.Sprintf("Теперь просто отправь новую фотографию для %s одним следующим сообщением. Старое фото будет заменено.", player.PlayerNickname))
		return
	}
	h.reply(message.Chat.ID, fmt.Sprintf("Теперь просто отправь фотографию для %s одним следующим сообщением.", player.PlayerNickname))
}

func (h *BotHandler) handleRemovePhoto(message *telegram.Message) {
	ref := strings.TrimSpace(message.CommandArguments())
	if ref == "" {
		h.reply(message.Chat.ID, "Использование: /removephoto <номер из /list | FACEIT link | player_id | nickname>")
		return
	}
	scopeID := storage.ScopeIDForChat(message.Chat.ID)
	player, err := h.storage.FindChatPlayerByReference(scopeID, ref)
	if err != nil {
		h.reply(message.Chat.ID, "Игрок не найден в списке отслеживания этого чата.")
		return
	}
	if player.PhotoPath == "" {
		h.clearPendingPhoto(pendingPhotoKey(message.Chat.ID, message.From.ID))
		h.reply(message.Chat.ID, fmt.Sprintf("У %s и так нет сохраненного фото.", player.PlayerNickname))
		return
	}
	if err := h.storage.RemovePlayerPhoto(scopeID, player.PlayerID); err != nil {
		h.reply(message.Chat.ID, fmt.Sprintf("Не удалось удалить фото: %v", err))
		return
	}
	h.clearPendingPhoto(pendingPhotoKey(message.Chat.ID, message.From.ID))
	h.reply(message.Chat.ID, fmt.Sprintf("Фото для %s удалено.", player.PlayerNickname))
}

func (h *BotHandler) handlePhotoMessage(message *telegram.Message) {
	scopeID := storage.ScopeIDForChat(message.Chat.ID)
	pendingKey := pendingPhotoKey(message.Chat.ID, message.From.ID)
	playerID, ok := h.getPendingPhoto(pendingKey)
	if !ok {
		return
	}
	defer h.clearPendingPhoto(pendingKey)

	player, err := h.storage.GetPlayer(scopeID, playerID)
	if err != nil {
		h.reply(message.Chat.ID, "Не удалось найти игрока для этой фотографии.")
		return
	}
	hadPhoto := player.PhotoPath != ""

	photo := message.Photo[len(message.Photo)-1]
	directURL, err := h.Bot.GetFileDirectURL(context.Background(), photo.FileID)
	if err != nil {
		h.reply(message.Chat.ID, fmt.Sprintf("Не удалось получить файл из Telegram: %v", err))
		return
	}
	data, ext, err := downloadImage(directURL, h.cfg.MaxPhotoBytes)
	if err != nil {
		h.reply(message.Chat.ID, fmt.Sprintf("Не удалось скачать фотографию: %v", err))
		return
	}
	relPath, err := h.storage.SavePlayerPhoto(scopeID, player.PlayerID, data, ext)
	if err != nil {
		h.reply(message.Chat.ID, fmt.Sprintf("Не удалось сохранить фотографию: %v", err))
		return
	}
	player.PhotoPath = relPath
	if err := h.storage.UpdatePlayer(player); err != nil {
		h.reply(message.Chat.ID, fmt.Sprintf("Не удалось обновить игрока: %v", err))
		return
	}
	if hadPhoto {
		h.reply(message.Chat.ID, fmt.Sprintf("Фото для %s обновлено.", player.PlayerNickname))
		return
	}
	h.reply(message.Chat.ID, fmt.Sprintf("Фото для %s сохранено.", player.PlayerNickname))
}

func (h *BotHandler) handleUntrack(message *telegram.Message) {
	ref := strings.TrimSpace(message.CommandArguments())
	if ref == "" {
		h.reply(message.Chat.ID, "Использование: /untrack <номер из /list | FACEIT link | player_id | nickname>")
		return
	}
	scopeID := storage.ScopeIDForChat(message.Chat.ID)
	player, err := h.storage.FindChatPlayerByReference(scopeID, ref)
	if err != nil {
		h.reply(message.Chat.ID, "Игрок не найден в списке отслеживания этого чата.")
		return
	}
	if err := h.storage.RemovePlayer(scopeID, player.PlayerID); err != nil {
		h.reply(message.Chat.ID, fmt.Sprintf("Не удалось удалить игрока: %v", err))
		return
	}
	h.clearPendingPhoto(pendingPhotoKey(message.Chat.ID, message.From.ID))
	h.reply(message.Chat.ID, fmt.Sprintf("%s убран из отслеживания.", player.PlayerNickname))
}

func (h *BotHandler) handleList(message *telegram.Message) {
	scopeID := storage.ScopeIDForChat(message.Chat.ID)
	players := h.storage.GetChatPlayers(scopeID)
	if len(players) == 0 {
		h.reply(message.Chat.ID, "Список отслеживания пуст.")
		return
	}
	var b strings.Builder
	b.WriteString("Твои отслеживаемые игроки:\n")
	b.WriteString("Используй номер игрока из этого списка в /setphoto, /removephoto, /recent, /stats и /untrack.\n\n")
	for i, p := range players {
		fmt.Fprintf(&b, "%d. %s\n", i+1, p.PlayerNickname)
		fmt.Fprintf(&b, "   Player ID: %s\n", p.PlayerID)
		fmt.Fprintf(&b, "   Profile: %s\n", p.ProfileURL)
		fmt.Fprintf(&b, "   ELO: %d\n", p.CurrentElo)
		if p.PhotoPath != "" {
			b.WriteString("   Photo: Uploaded\n")
		} else {
			b.WriteString("   Photo: Not uploaded\n")
		}
		if p.LastMatchTime > 0 {
			t := time.Unix(p.LastMatchTime, 0).In(h.cfg.Timezone)
			fmt.Fprintf(&b, "   Last match: %s\n", t.Format("02.01.2006 15:04"))
		}
		b.WriteString("\n")
	}
	h.reply(message.Chat.ID, strings.TrimSpace(b.String()))
}

func (h *BotHandler) handleRecent(message *telegram.Message) {
	ref := strings.TrimSpace(message.CommandArguments())
	if ref == "" {
		h.reply(message.Chat.ID, "Использование: /recent <номер из /list | FACEIT link | nickname>")
		return
	}
	selected, errText := h.resolveReference(message, ref)
	if errText != "" {
		h.reply(message.Chat.ID, errText)
		return
	}
	matches, err := h.faceit.GetPlayerMatches(context.Background(), selected.PlayerID, selected.Game, 1)
	if err != nil {
		h.reply(message.Chat.ID, fmt.Sprintf("Ошибка FACEIT: %v", err))
		return
	}
	if len(matches) == 0 {
		h.reply(message.Chat.ID, "У игрока пока нет матчей в истории.")
		return
	}
	latest := matches[0]
	details, _ := h.faceit.GetMatchDetails(context.Background(), latest.MatchID)
	stats, _ := h.faceit.GetMatchStats(context.Background(), latest.MatchID, selected.PlayerID)
	if details == nil {
		details = &faceit.MatchDetails{MatchID: latest.MatchID, FaceitURL: latest.FaceitURL, CompetitionName: latest.CompetitionName, Status: latest.Status, StartedAt: latest.StartedAt, FinishedAt: latest.FinishedAt}
	}
	matchTime := time.Unix(firstNonZero(details.FinishedAt, details.StartedAt), 0).In(h.cfg.Timezone)
	var b strings.Builder
	fmt.Fprintf(&b, "Last match: %s\n", selected.Nickname)
	fmt.Fprintf(&b, "Date: %s\n", matchTime.Format("02.01.2006 15:04"))
	if stats != nil {
		fmt.Fprintf(&b, "Map: %s\n", stats.Map)
		if score := formatScoreForPlayer(latest, selected.PlayerID); score != "" {
			fmt.Fprintf(&b, "Score: %s\n", score)
		}
		fmt.Fprintf(&b, "K-D: %d-%d\n", stats.Kills, stats.Deaths)
		fmt.Fprintf(&b, "K/D: %.2f\n", stats.KD)
		fmt.Fprintf(&b, "HS: %.1f%%\n", stats.HeadshotsPct)
		fmt.Fprintf(&b, "Damage: %d\n", stats.Damage)
		fmt.Fprintf(&b, "Utility: %d\n", stats.UtilityDamage)
		fmt.Fprintf(&b, "Result: %s\n", strings.ToUpper(stats.Result))
	}
	if details.FaceitURL != "" {
		fmt.Fprintf(&b, "FACEIT room: %s\n", details.FaceitURL)
	}
	//в случае получение Downloads API токена
	/*if demo := firstNonEmptySlice(details.DemoURLs); demo != "" {
		fmt.Fprintf(&b, "Demo URL:\n%s", demo)
	} else {
		b.WriteString("Не удалось получить ссылку на демо.")
	}*/
	h.reply(message.Chat.ID, b.String())
}

func (h *BotHandler) handleStats(message *telegram.Message) {
	ref := strings.TrimSpace(message.CommandArguments())
	if ref == "" {
		h.reply(message.Chat.ID, "Использование: /stats <номер из /list | FACEIT link | nickname>")
		return
	}
	selected, errText := h.resolveReference(message, ref)
	if errText != "" {
		h.reply(message.Chat.ID, errText)
		return
	}
	stats, err := h.faceit.GetPlayerStats(context.Background(), selected.PlayerID, selected.Game)
	if err != nil {
		h.reply(message.Chat.ID, fmt.Sprintf("Ошибка FACEIT: %v", err))
		return
	}
	text := strings.Join([]string{
		fmt.Sprintf("- Player: %s", stats.Nickname),
		fmt.Sprintf("- Profile: %s", stats.ProfileURL),
		fmt.Sprintf("- ELO: %d", stats.Elo),
		fmt.Sprintf("- Level: %d", stats.Level),
		fmt.Sprintf("- Matches: %d", stats.TotalMatches),
		"Stats for the last 30 matches:",
		fmt.Sprintf("- K/D: %.2f", stats.KDRatio),
		fmt.Sprintf("- Winrate: %.1f%%", stats.WinRate),
		fmt.Sprintf("- AVG kills: %.1f", stats.AvgKills),
		fmt.Sprintf("- ADR: %.1f", stats.ADR),
		fmt.Sprintf("- Headshots: %.1f%%", stats.HeadshotsPct),
	}, "\n")
	h.reply(message.Chat.ID, text)
}

func (h *BotHandler) resolveReference(message *telegram.Message, ref string) (faceit.SearchPlayerResult, string) {
	scopeID := storage.ScopeIDForChat(message.Chat.ID)
	if tracked, err := h.storage.FindChatPlayerByReference(scopeID, ref); err == nil && tracked != nil {
		return faceit.SearchPlayerResult{
			PlayerID:   tracked.PlayerID,
			Nickname:   tracked.PlayerNickname,
			Avatar:     tracked.Avatar,
			Game:       firstNonEmpty(tracked.Game, "cs2"),
			ProfileURL: tracked.ProfileURL,
		}, ""
	}
	return h.resolveReferenceToFaceit(ref)
}

func (h *BotHandler) resolveReferenceToFaceit(ref string) (faceit.SearchPlayerResult, string) {
	resolvedNickname := ref
	if nickname, ok := faceit.ExtractNicknameFromProfileURL(ref); ok {
		resolvedNickname = nickname
	}
	players, err := h.faceit.SearchPlayer(context.Background(), resolvedNickname)
	if err != nil {
		return faceit.SearchPlayerResult{}, fmt.Sprintf("Ошибка FACEIT: %v", err)
	}
	selected, variants, err := choosePlayer(players, resolvedNickname)
	if err != nil {
		return faceit.SearchPlayerResult{}, err.Error()
	}
	if len(variants) > 1 {
		return faceit.SearchPlayerResult{}, renderVariants(resolvedNickname, variants)
	}
	return selected, ""
}

func choosePlayer(players []faceit.SearchPlayerResult, requested string) (faceit.SearchPlayerResult, []faceit.SearchPlayerResult, error) {
	if len(players) == 0 {
		return faceit.SearchPlayerResult{}, nil, fmt.Errorf("игрок не найден")
	}
	exact := make([]faceit.SearchPlayerResult, 0)
	for _, p := range players {
		if strings.EqualFold(p.Nickname, requested) {
			exact = append(exact, p)
		}
	}
	if len(exact) == 1 {
		return exact[0], exact, nil
	}
	if len(exact) > 1 {
		sort.SliceStable(exact, func(i, j int) bool { return exact[i].Game == "cs2" && exact[j].Game != "cs2" })
		if len(exact) == 1 {
			return exact[0], exact, nil
		}
		return faceit.SearchPlayerResult{}, exact, nil
	}
	if len(players) == 1 {
		return players[0], players[:1], nil
	}
	return faceit.SearchPlayerResult{}, players[:min(5, len(players))], nil
}

func renderVariants(requested string, players []faceit.SearchPlayerResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Найдено несколько вариантов для %q. Лучше пришли прямую ссылку на профиль FACEIT.\n\n", requested)
	for i, p := range players {
		fmt.Fprintf(&b, "%d. %s | %s | level %d\n", i+1, p.Nickname, strings.ToUpper(p.Game), p.SkillLevel)
		fmt.Fprintf(&b, "   %s\n", p.ProfileURL)
	}
	return strings.TrimSpace(b.String())
}

func downloadImage(rawURL string, maxBytes int) ([]byte, string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("status %d", resp.StatusCode)
	}
	reader := io.Reader(resp.Body)
	if maxBytes > 0 {
		reader = io.LimitReader(resp.Body, int64(maxBytes)+1)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, "", err
	}
	if maxBytes > 0 && len(data) > maxBytes {
		return nil, "", fmt.Errorf("файл слишком большой: максимум %d байт", maxBytes)
	}
	ext := guessImageExtension(rawURL, resp.Header.Get("Content-Type"), data)
	return data, ext, nil
}

func guessImageExtension(rawURL, contentType string, data []byte) string {
	if u, err := url.Parse(rawURL); err == nil {
		if ext := strings.ToLower(path.Ext(u.Path)); ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp" {
			return ext
		}
	}
	contentType = strings.ToLower(contentType)
	switch {
	case strings.Contains(contentType, "png"):
		return ".png"
	case strings.Contains(contentType, "webp"):
		return ".webp"
	case strings.Contains(contentType, "jpeg") || strings.Contains(contentType, "jpg"):
		return ".jpg"
	}
	if http.DetectContentType(bytes.TrimSpace(data)) == "image/png" {
		return ".png"
	}
	return ".jpg"
}

func (h *BotHandler) commandCooldownRemaining(message *telegram.Message, command string) (string, bool) {
	key := fmt.Sprintf("%d:%d:%s", message.Chat.ID, message.From.ID, strings.ToLower(command))
	now := time.Now()
	limit := h.cfg.CommandCooldown
	switch strings.ToLower(command) {
	case "track", "recent", "stats":
		limit = h.cfg.HeavyCommandCooldown
	}
	if limit <= 0 {
		return "", false
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if until, ok := h.commandWindow[key]; ok && now.Before(until) {
		return until.Sub(now).Round(time.Second).String(), true
	}
	h.commandWindow[key] = now.Add(limit)
	if len(h.commandWindow) > 1024 {
		for k, until := range h.commandWindow {
			if now.After(until.Add(time.Minute)) {
				delete(h.commandWindow, k)
			}
		}
	}
	return "", false
}

func (h *BotHandler) reply(chatID int64, text string) {
	_ = h.Bot.SendMessage(context.Background(), chatID, text, true)
}

func (h *BotHandler) GetBot() *telegram.Bot { return h.Bot }

func (h *BotHandler) setPendingPhoto(userID, playerID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pendingPhoto[userID] = playerID
}

func (h *BotHandler) getPendingPhoto(userID string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	playerID, ok := h.pendingPhoto[userID]
	return playerID, ok
}

func (h *BotHandler) clearPendingPhoto(userID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.pendingPhoto, userID)
}

func pendingPhotoKey(chatID, userID int64) string {
	return fmt.Sprintf("%d:%d", chatID, userID)
}

func formatScoreForPlayer(match faceit.MatchInfo, playerID string) string {
	return faceit.FormatMatchScore(match, playerID)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

/*func firstNonEmptySlice(values []string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}*/

func firstNonZero(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}
