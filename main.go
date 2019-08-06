package main

import (
	"errors"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	tokenFile = "/run/secrets/vibetron_token"
)

var (
	// The version number overriden at buildtime via -ldflags="-X main.version x.y.z"
	version = "undefined"
)

type botState struct {
	startTime time.Time
}

func messageCreateWrapper(bs botState) func(*discordgo.Session, *discordgo.MessageCreate) {

	return func(s *discordgo.Session, m *discordgo.MessageCreate) {

		// Ignore messages from the bot itself
		if m.Author.ID == s.State.User.ID {
			return
		}

		// Ignore messages from other bots
		if m.Author.Bot {
			return
		}

		if m.Content == ".version" {
			s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("version: %s", version))
		}

		if m.Content == ".uptime" {
			s.ChannelMessageSend(
				m.ChannelID,
				fmt.Sprintf(
					"uptime: %s",
					// Round time to time.Second to not get
					// an unnecessary amount of decimals in
					// the string.
					time.Since(bs.startTime).Round(time.Second).String(),
				),
			)
		}
	}
}

func runBot(token string) error {

	if token == "" {
		return errors.New("runBot: token is required")
	}

	bs := botState{
		startTime: time.Now(),
	}

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		return fmt.Errorf("runBot: unable to init new Discord session: %s", err)
	}

	dg.AddHandler(messageCreateWrapper(bs))

	err = dg.Open()
	if err != nil {
		return fmt.Errorf("runBot: error opening connection: %s", err)
	}

	fmt.Println("vibetron online. Press CTRL-C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, os.Interrupt, syscall.SIGTERM)
	<-sc

	log.Println("runBot: Closing Discord session...")

	err = dg.Close()
	if err != nil {
		return fmt.Errorf("runBot: Failed closing Discord session: %s", err)
	}

	return nil
}

func main() {

	// Read bot token from file
	tokenBytes, err := ioutil.ReadFile(tokenFile)
	if err != nil {
		log.Fatal(err)
	}

	// Make sure no whitespace is present in the token data
	token := strings.TrimSpace(string(tokenBytes))

	if token == "" {
		log.Fatalf("token file %s appears empty, exiting", tokenFile)
	}

	err = runBot(token)

	if err != nil {
		log.Fatal(err)
	}
}
