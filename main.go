package main

import (
	"context"
	crand "crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"github.com/BurntSushi/toml"
	"github.com/bwmarrin/discordgo"
	goredislib "github.com/go-redis/redis/v8"
	"github.com/go-redsync/redsync/v4"
	"github.com/go-redsync/redsync/v4/redis/goredis/v8"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	tokenFile     = "/run/vibetron/token" // #nosec
	ipv4Localhost = "127.0.0.1"
)

var (
	// The version number overriden at buildtime via -ldflags="-X main.version x.y.z"
	version = "undefined"
)

type botState struct {
	startTime time.Time
	rdb       *goredislib.Client
	rs        *redsync.Redsync
	hostname  string
}

// apiServiceConfig holds information read from the config file used by the API service.
type vibetronConfig struct {
	Redis redisConfig
}

type redisConfig struct {
	Address  string
	Port     int
	Password string
}

func getMessageLock(bs botState, m *discordgo.MessageCreate) bool {
	mutex := bs.rs.NewMutex(
		m.ID,
		redsync.WithTries(1),
		redsync.WithExpiry(10*time.Second),
	)

	if err := mutex.Lock(); err != nil {
		log.Printf("unable to get lock for message ID %s: %s", m.ID, err)
		return false
	}
	log.Printf("aquired lock for message ID %s", m.ID)
	return true
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

			if ok := getMessageLock(bs, m); !ok {
				return
			}

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
					"* .runtime: the go version used to build the bot\n"+
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

		case ".runtime":
			_, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("%s runtime: %s", bs.hostname, runtime.Version()))
			if err != nil {
				log.Printf(".runtime: %s", err)
			}

		case ".version":
			_, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("%s version: %s", bs.hostname, version))
			if err != nil {
				log.Printf(".version: %s", err)
			}

		case ".uptime":
			_, err := s.ChannelMessageSend(
				m.ChannelID,
				fmt.Sprintf(
					"%s uptime: %s",
					// Round time to time.Second to not get
					// an unnecessary amount of decimals in
					// the string.
					bs.hostname,
					time.Since(bs.startTime).Round(time.Second).String(),
				),
			)
			if err != nil {
				log.Printf(".uptime: %s", err)
			}

		case ".roll":
			if ok := getMessageLock(bs, m); !ok {
				return
			}

			rollMin := 1
			rollMax := 100
			res := rollMin + rand.Intn(rollMax-rollMin+1)
			_, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("%s rolls (%d-%d): %d", m.Author.Mention(), rollMin, rollMax, res))
			if err != nil {
				log.Printf(".roll: %s", err)
			}

		case ".flip":
			if ok := getMessageLock(bs, m); !ok {
				return
			}

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
			if ok := getMessageLock(bs, m); !ok {
				return
			}

			started, err := startwatchStart(bs, m.Author.ID)
			if err != nil {
				log.Printf(".swstart(init): %s", err)
				_, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("%s: stopwatch failed", m.Author.Mention()))
				if err != nil {
					log.Printf(".swstart(init 2): %s", err)
				}
				return
			}

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
			if ok := getMessageLock(bs, m); !ok {
				return
			}

			duration, running, err := startwatchLap(bs, m.Author.ID)
			if err != nil {
				log.Printf(".swlap(init): %s", err)
				_, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("%s: stopwatch lap failed", m.Author.Mention()))
				if err != nil {
					log.Printf(".swlap(init 2): %s", err)
				}
				return
			}

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
			if ok := getMessageLock(bs, m); !ok {
				return
			}

			duration, stopped, err := startwatchStop(bs, m.Author.ID)
			if err != nil {
				log.Printf(".swtop(init): %s", err)
				_, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("%s: stopwatch stop failed", m.Author.Mention()))
				if err != nil {
					log.Printf(".swstop(init 2): %s", err)
				}
				return
			}

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

func startwatchStart(bs botState, id string) (bool, error) {
	err := bs.rdb.HGet(context.Background(), "startwatch", id).Err()
	if err == goredislib.Nil {
		// The key did not exist, create it.
		//
		// If multiple .swstart messages arrive simultaneously for the
		// same author ID I assume it is possible a race
		// condition occurs where multiple calls get a Nil result from
		// the HGET above.
		//
		// For this reason use HSETNX below so we only store the first
		// (hopefully oldest) timestamp and ignore the rest in that
		// case.
		err := bs.rdb.HSetNX(
			context.Background(),
			"startwatch",
			id,
			strconv.FormatInt(time.Now().Unix(), 10),
		).Err()
		if err != nil {
			return false, err
		}

		return true, nil
	} else if err != nil {
		log.Printf("startwatchStart err: %s", err)
		return false, err
	}
	// The key already exists
	return false, nil
}

func startwatchLap(bs botState, id string) (time.Duration, bool, error) {
	unixTs, err := bs.rdb.HGet(context.Background(), "startwatch", id).Result()
	if err == goredislib.Nil {
		return 0, false, nil
	} else if err != nil {
		log.Printf("startwatchLap err: %s", err)
		return 0, false, err
	}

	unixTsInt, err := strconv.ParseInt(unixTs, 10, 0)
	if err != nil {
		return 0, false, err
	}

	t := time.Unix(unixTsInt, 0)

	return time.Since(t), true, nil
}

func startwatchStop(bs botState, id string) (time.Duration, bool, error) {
	unixTs, err := bs.rdb.HGet(context.Background(), "startwatch", id).Result()
	if err == goredislib.Nil {
		return 0, false, nil
	} else if err != nil {
		log.Printf("startwatchStop err: %s", err)
		return 0, false, err
	}

	err = bs.rdb.HDel(context.Background(), "startwatch", id).Err()
	if err == goredislib.Nil {
		// Someone else already removed the key, this is OK
		log.Printf("startwatchStop tried removing nonexistent id: %s", id)
	} else if err != nil {
		return 0, false, err
	}

	unixTsInt, err := strconv.ParseInt(unixTs, 10, 0)
	if err != nil {
		return 0, false, err
	}

	t := time.Unix(unixTsInt, 0)

	return time.Since(t), true, nil
}

func runBot(token string, rdb *goredislib.Client, rs *redsync.Redsync) error {

	if token == "" {
		return errors.New("runBot: token is required")
	}

	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("hostname error: %s", err)
	}

	bs := botState{
		startTime: time.Now(),
		rdb:       rdb,
		rs:        rs,
		hostname:  hostname,
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

// readAPIServiceConfig parses the supplied configuration file or falls back to
// default settings.
func readVibetronConfig(configFile *string) *vibetronConfig {
	config := newVibetronConfig()

	if *configFile != "" {
		log.Printf("reading config file %s", *configFile)
		if _, err := toml.DecodeFile(*configFile, config); err != nil {
			log.Fatalf("TOML decoding failed: %s", err)
		}
	}

	return config
}

// newVibetronConfig returns default configuration
func newVibetronConfig() *vibetronConfig {
	return &vibetronConfig{
		Redis: redisConfig{
			Address:  ipv4Localhost,
			Port:     6379,
			Password: "",
		},
	}
}

func main() {

	configFile := flag.String("config", "", "configuration file")
	flag.Parse()

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

	// Fetch configuration settings.
	config := readVibetronConfig(configFile)

	// Since the bot has some randomness dependent functionality like the
	// .flip and .roll commands, seed the PRNG with some unknown data.
	seed, err := getRandomSeed()
	if err != nil {
		log.Fatal(err)
	}
	rand.Seed(seed)

	// Create a pool with go-redis (or redigo) which is the pool redisync will
	// use while communicating with Redis. This can also be any pool that
	// implements the `redis.Pool` interface.
	rdb := goredislib.NewClient(&goredislib.Options{
		Addr:      fmt.Sprintf("%s:%d", config.Redis.Address, config.Redis.Port),
		Password:  config.Redis.Password,
		TLSConfig: &tls.Config{},
	})
	pool := goredis.NewPool(rdb) // or, pool := redigo.NewPool(...)

	// Create an instance of redisync to be used to obtain a mutual exclusion
	// lock.
	rs := redsync.New(pool)

	err = runBot(token, rdb, rs)
	if err != nil {
		log.Fatal(err)
	}
}
