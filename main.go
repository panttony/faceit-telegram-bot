package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"faceit-telegram-bot/bot"
	"faceit-telegram-bot/config"
	"faceit-telegram-bot/faceit"
	"faceit-telegram-bot/monitor"
	"faceit-telegram-bot/storage"
)

func main() {
	log.Println("starting FACEIT monitor bot")
	cfg := config.LoadConfig()
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatal(err)
	}

	store := storage.NewStorage(cfg.DataDir)
	faceitClient := faceit.NewClient(cfg.FaceitAPIKey, cfg.FaceitDownloadsToken, cfg.FaceitDownloadsBaseURL)
	botHandler, err := bot.NewBotHandler(cfg.TelegramToken, faceitClient, store, cfg)
	if err != nil {
		log.Fatal(err)
	}
	monitorService := monitor.NewMonitor(faceitClient, store, botHandler.GetBot(), cfg)
	monitorService.Start()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go botHandler.Start(ctx)

	<-ctx.Done()
	log.Println("shutdown signal received")
	monitorService.Stop()
	botHandler.GetBot().StopReceivingUpdates()
}
