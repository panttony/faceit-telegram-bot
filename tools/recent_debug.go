//go:build ignore

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"faceit-telegram-bot/config"
	"faceit-telegram-bot/faceit"
)

func main() {
	cfg := config.LoadConfig()
	client := faceit.NewClient(cfg.FaceitAPIKey, cfg.FaceitDownloadsToken, cfg.FaceitDownloadsBaseURL)
	players, err := client.SearchPlayer(context.Background(), "DeeSee")
	if err != nil {
		log.Fatal(err)
	}
	if len(players) == 0 {
		log.Fatal("player not found")
	}
	player := players[0]
	matches, err := client.GetPlayerMatches(context.Background(), player.PlayerID, player.Game, 5)
	if err != nil {
		log.Fatal(err)
	}
	if len(matches) == 0 {
		fmt.Println("no matches")
		return
	}
	first := matches[0]
	fmt.Printf("match id: %s\n", first.MatchID)
	fmt.Printf("finished at: %s\n", time.Unix(first.FinishedAt, 0).Format(time.RFC3339))
	jsonData, _ := json.MarshalIndent(first, "", "  ")
	fmt.Println(string(jsonData))
}
