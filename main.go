package main

import (
	crand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	tokenFile = "/run/secrets/vibetron_token" // #nosec
)

var (
	// The version number overriden at buildtime via -ldflags="-X main.version x.y.z"
	version = "undefined"
)

type timer struct {
	mux     sync.RWMutex
	timeMap map[string]time.Time
}

type botState struct {
	startTime time.Time
	// Use pointer to timer struct so the included sync.RWMutex is not
	// copied by value
	timer *timer
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

		switch m.Content {
		case ".help":
			st, err := s.UserChannelCreate(m.Author.ID)
			if err != nil {
				log.Printf("unable to find user with ID %s: %s", m.Author.ID, err)
				return
			}
			_, err = s.ChannelMessageSend(
				st.ID,
				"```md\n"+
					"# available commands are:\n"+
					"* .flip: flip a coin\n"+
					"* .help: this information\n"+
					"* .roll: get a random number (1-100)\n"+
					"* .swstart: start stopwatch timer\n"+
					"* .swlap: show current stopwatch time\n"+
					"* .swstop: stop stopwatch and show final time\n"+
					"* .uptime: the bot uptime\n"+
					"* .version: the bot version\n"+
					"```",
			)

			if err != nil {
				log.Printf(".help: %s", err)
			}

		case ".version":
			_, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("version: %s", version))
			if err != nil {
				log.Printf(".version: %s", err)
			}

		case ".uptime":
			_, err := s.ChannelMessageSend(
				m.ChannelID,
				fmt.Sprintf(
					"uptime: %s",
					// Round time to time.Second to not get
					// an unnecessary amount of decimals in
					// the string.
					time.Since(bs.startTime).Round(time.Second).String(),
				),
			)
			if err != nil {
				log.Printf(".uptime: %s", err)
			}

		case ".roll":
			rollMin := 1
			rollMax := 100
			res := rollMin + rand.Intn(rollMax-rollMin+1)
			_, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("%s rolls (%d-%d): %d", m.Author.Mention(), rollMin, rollMax, res))
			if err != nil {
				log.Printf(".roll: %s", err)
			}

		case ".flip":
			res := rand.Intn(2)

			side := "HEADS"

			if res == 1 {
				side = "TAILS"
			}

			_, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("%s flips: %s", m.Author.Mention(), side))
			if err != nil {
				log.Printf(".flip: %s", err)
			}

		case ".swstart":
			started := startwatchStart(bs, m.Author.ID)

			if started {
				_, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("%s: stopwatch started", m.Author.Mention()))
				if err != nil {
					log.Printf(".swstart(started): %s", err)
				}
			} else {
				_, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("%s: stopwatch already running, stop with `.swstop`", m.Author.Mention()))
				if err != nil {
					log.Printf(".swstart(not started): %s", err)
				}
			}

		case ".swlap":
			duration, running := startwatchLap(bs, m.Author.ID)

			if running {
				_, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("%s: stopwatch lap time: %s", m.Author.Mention(), duration.String()))
				if err != nil {
					log.Printf(".swlap(running): %s", err)
				}
			} else {
				_, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("%s: stopwatch is not running, start with `.swstart`", m.Author.Mention()))
				if err != nil {
					log.Printf(".swlap(not running): %s", err)
				}
			}

		case ".swstop":
			duration, stopped := startwatchStop(bs, m.Author.ID)

			if stopped {
				_, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("%s: stopwatch stopped, final time: %s", m.Author.Mention(), duration.String()))
				if err != nil {
					log.Printf(".swstop(stopped): %s", err)
				}
			} else {
				_, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("%s: stopwatch is not running, start with `.swstart`", m.Author.Mention()))
				if err != nil {
					log.Printf(".swstop(not stopped): %s", err)
				}
			}

		}
	}
}

func startwatchStart(bs botState, id string) bool {
	bs.timer.mux.Lock()
	defer bs.timer.mux.Unlock()
	if _, ok := bs.timer.timeMap[id]; !ok {
		bs.timer.timeMap[id] = time.Now()
		return true
	}

	return false
}

func startwatchLap(bs botState, id string) (time.Duration, bool) {
	bs.timer.mux.RLock()
	defer bs.timer.mux.RUnlock()
	if t, ok := bs.timer.timeMap[id]; ok {
		return time.Since(t), true
	}

	return 0, false
}

func startwatchStop(bs botState, id string) (time.Duration, bool) {
	bs.timer.mux.Lock()
	defer bs.timer.mux.Unlock()
	if t, ok := bs.timer.timeMap[id]; ok {
		delete(bs.timer.timeMap, id)
		return time.Since(t), true
	}

	return 0, false
}

func runBot(token string) error {

	if token == "" {
		return errors.New("runBot: token is required")
	}

	bs := botState{
		startTime: time.Now(),
		timer: &timer{
			timeMap: map[string]time.Time{},
		},
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

// Get a fairly random seed, at least more random than the startup time.
func getRandomSeed() (int64, error) {

	// Mix of https://godoc.org/crypto/rand and
	// https://stackoverflow.com/questions/12321133/golang-random-number-generator-how-to-seed-properly
	c := 10
	b := make([]byte, c)
	_, err := crand.Read(b)
	if err != nil {
		return 0, err
	}

	return int64(binary.LittleEndian.Uint64(b)), nil

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

	// Since the bot has some randomness dependent functionality like the
	// .flip and .roll commands, seed the PRNG with some unknown data.
	seed, err := getRandomSeed()
	if err != nil {
		log.Fatal(err)
	}
	rand.Seed(seed)

	err = runBot(token)
	if err != nil {
		log.Fatal(err)
	}
}
