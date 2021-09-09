package loadgen

import (
	"encoding/hex"
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/pyroscope-io/pyroscope/benchmark/config"
	"github.com/pyroscope-io/pyroscope/pkg/agent/upstream"
	"github.com/pyroscope-io/pyroscope/pkg/agent/upstream/remote"
	"github.com/pyroscope-io/pyroscope/pkg/structs/transporttrie"
	"github.com/sirupsen/logrus"
)

// how many retries to check the pyroscope server is up
const MaxReadinessRetries = 10

type Fixtures [][]*transporttrie.Trie

type LoadGen struct {
	Config    *config.LoadGen
	Rand      *rand.Rand
	SymbolBuf []byte
}

func Cli(cfg *config.LoadGen) error {
	r := rand.New(rand.NewSource(int64(cfg.RandSeed)))
	l := &LoadGen{
		Config:    cfg,
		Rand:      r,
		SymbolBuf: make([]byte, cfg.ProfileSymbolLength),
	}

	return l.Run(cfg)
}

func (l *LoadGen) Run(cfg *config.LoadGen) error {
	logrus.Info("checking server is available...")
	err := waitUntilEndpointReady(cfg.ServerAddress)
	if err != nil {
		return err
	}

	logrus.Info("generating fixtures")
	fixtures := l.generateFixtures()
	logrus.Debug("done generating fixtures.")

	logrus.Info("starting sending requests")
	wg := sync.WaitGroup{}
	wg.Add(l.Config.Apps * l.Config.Clients)
	appNameBuf := make([]byte, 25)

	for i := 0; i < l.Config.Apps; i++ {
		// generate a random app name
		l.Rand.Read(appNameBuf)
		appName := hex.EncodeToString(appNameBuf)
		for j := 0; j < l.Config.Clients; j++ {
			go l.startClientThread(appName, &wg, fixtures[i])
		}
	}
	wg.Wait()

	logrus.Debug("done sending requests")
	return nil
}

func (l *LoadGen) generateFixtures() Fixtures {
	var f Fixtures

	for i := 0; i < l.Config.Apps; i++ {
		f = append(f, []*transporttrie.Trie{})

		randomGen := rand.New(rand.NewSource(int64(l.Config.RandSeed + i)))
		p := l.generateProfile(randomGen)
		for j := 0; j < l.Config.Fixtures; j++ {
			f[i] = append(f[i], p)
		}
	}

	return f
}

func (l *LoadGen) startClientThread(appName string, wg *sync.WaitGroup, appFixtures []*transporttrie.Trie) {
	rc := remote.RemoteConfig{
		UpstreamThreads:        1,
		UpstreamAddress:        l.Config.ServerAddress,
		UpstreamRequestTimeout: 10 * time.Second,
	}
	r, err := remote.New(rc, logrus.StandardLogger())
	if err != nil {
		panic(err)
	}

	requestsCount := l.Config.Requests

	threadStartTime := time.Now().Truncate(10 * time.Second)
	threadStartTime = threadStartTime.Add(time.Duration(-1*requestsCount) * (10 * time.Second))

	st := threadStartTime

	for i := 0; i < requestsCount; i++ {
		t := appFixtures[i%len(appFixtures)]

		st = st.Add(10 * time.Second)
		et := st.Add(10 * time.Second)
		err := r.UploadSync(&upstream.UploadJob{
			Name:            appName + "{}",
			StartTime:       st,
			EndTime:         et,
			SpyName:         "gospy",
			SampleRate:      100,
			Units:           "samples",
			AggregationType: "sum",
			Trie:            t,
		})
		if err != nil {
			// TODO(eh-am): calculate errors
			time.Sleep(time.Second)
		} else {
			// TODO(eh-am): calculate success
		}
	}

	wg.Done()
}

func (l *LoadGen) generateProfile(randomGen *rand.Rand) *transporttrie.Trie {
	t := transporttrie.New()

	for w := 0; w < l.Config.ProfileWidth; w++ {
		symbol := []byte("root")
		for d := 0; d < 2+l.Rand.Intn(l.Config.ProfileDepth); d++ {
			randomGen.Read(l.SymbolBuf)
			symbol = append(symbol, byte(';'))
			symbol = append(symbol, []byte(hex.EncodeToString(l.SymbolBuf))...)
			if l.Rand.Intn(100) <= 20 {
				t.Insert(symbol, uint64(l.Rand.Intn(100)), true)
			}
		}

		t.Insert(symbol, uint64(l.Rand.Intn(100)), true)
	}
	return t
}

// TODO(eh-am) exponential backoff and whatnot
func waitUntilEndpointReady(url string) error {
	client := http.Client{Timeout: 10 * time.Second}
	retries := 0

	for {
		_, err := client.Get(url)

		// all good?
		if err == nil {
			return nil
		}
		if retries >= MaxReadinessRetries {
			break
		}

		time.Sleep(time.Second)
		retries++
	}

	return fmt.Errorf("maximum retries exceeded ('%d') waiting for server ('%s') to respond", retries, url)
}