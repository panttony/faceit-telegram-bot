//go:build ignore

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"
)

func saveToFile(filename string, data []byte) error {
	return os.WriteFile(filename, data, 0o644)
}

func main() {
	_ = godotenv.Load()
	apiKey := os.Getenv("FACEIT_API_KEY")
	if apiKey == "" {
		fmt.Println("FACEIT_API_KEY not found in .env")
		return
	}

	nickname := "s1mple"
	searchURL := fmt.Sprintf("https://open.faceit.com/data/v4/search/players?nickname=%s&limit=5", nickname)
	req, _ := http.NewRequest("GET", searchURL, nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	_ = saveToFile("response_search.json", body)

	var searchRes struct {
		Items []struct {
			PlayerID string `json:"player_id"`
			Nickname string `json:"nickname"`
		} `json:"items"`
	}
	_ = json.Unmarshal(body, &searchRes)
	fmt.Printf("saved search response, found %d players\n", len(searchRes.Items))
}
