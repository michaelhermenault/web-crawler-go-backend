package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"golang.org/x/net/html"
)

const initialResultsSize = 50
const timeOutInSeconds = 2
const crawlResultsTTL = 10

// Fetcher returns the body of URL and
// a slice of URLs found on that page.
type Fetcher interface {
	Fetch(url string) (body string, urls []string, err error)
}

// SafeMap is a "thread-safe" string->bool Map
// We'll use it to remember which sites we've already visited
type SafeMap struct {
	sync.Mutex
	v map[string]bool
}

func (safeMap *SafeMap) flip(name string) bool {
	safeMap.Lock()
	defer safeMap.Unlock()
	// Result should be saved
	result := safeMap.v[name]
	// Whatever the value was, turn it to true
	safeMap.v[name] = true
	return result
}

type jsonTime time.Time

func (t jsonTime) MarshalJSON() ([]byte, error) {
	//do your serializing here
	stamp := fmt.Sprintf("\"%s\"", time.Time(t).Format("Mon Jan _2"))
	return []byte(stamp), nil
}

type graphNode struct {
	Parent    string
	Children  []string
	TimeFound jsonTime
}

type finishSentinel struct {
	DoneMessage string
}

// Crawl uses fetcher to recursively crawl
// pages starting with url, to a maximum of depth.
func Crawl(url string, depth int, fetcher Fetcher, parentChan chan bool, urlMap *SafeMap) {
	// Once we're done we inform our parent
	defer func() {
		parentChan <- true
	}()

	if depth <= 0 {
		return
	}

	// First we check if this url has already been visited
	if urlMap.flip(url) {
		return
	}
	_, urls, err := fetcher.Fetch(url)

	if err != nil {
		// If we can't find the url, return (future iterations)
		fmt.Println(err)
		return
	}

	// fmt.Printf("Crawling: %s %q, child length: %d\n", url, body, len(urls))

	doneCh := make(chan bool, len(urls))
	numToExplore := len(urls)

	for _, u := range urls {
		go Crawl(u, depth-1, fetcher, doneCh, urlMap)
	}

	numFin := 0
	for {
		if numFin >= numToExplore {
			break
		}
		<-doneCh
		numFin++

	}
	return
}

func crawlHelper(url, uniqueID string, depth int, client *http.Client, rdb *redis.Client) {

	resultsListName := "go-crawler-results-" + uniqueID

	doneCh := make(chan bool)
	graphCh := make(chan graphNode)

	defer func() {
		close(doneCh)
		close(graphCh)
	}()

	urlMap := SafeMap{v: make(map[string]bool)}
	go Crawl(url, depth, realFetcher{client: client, graphCh: graphCh}, doneCh, &urlMap)
	// Loop until crawling is done, publishing results to redis
	for {
		select {
		case <-doneCh:
			marshalled, _ := json.Marshal(finishSentinel{DoneMessage: "true"})
			rdb.LPush(ctx, resultsListName, marshalled) //.Publish(ctx, resultsChannelName, marshalled).Err()
			rdb.Expire(ctx, resultsListName, crawlResultsTTL*time.Second)
			fmt.Println("Done recursively crawling: ", url)
			return
		case newNode := <-graphCh:
			marshalled, _ := json.Marshal(&newNode)
			rdb.LPush(ctx, resultsListName, marshalled) //.Publish(ctx, resultsChannelName, marshalled).Err()
			fmt.Println(string(marshalled))

		}
	}
}

var ctx = context.Background()

func main() {

	// Set up the http client
	tr := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   timeOutInSeconds * time.Second,
			KeepAlive: timeOutInSeconds * time.Second,
			DualStack: true,
		}).DialContext,
		IdleConnTimeout:     timeOutInSeconds * time.Second,
		TLSHandshakeTimeout: timeOutInSeconds * time.Second,
	}
	client := &http.Client{Transport: tr}

	// Set up the redis client
	rdb := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	// Receive instructions from this channel
	commandCh := rdb.Subscribe(ctx, "go-crawler-commands").Channel()

	// Stay in this loop responding to incoming requests
	for msg := range commandCh {
		splitCommand := strings.Split(msg.Payload, ",")
		fmt.Println("Staring recursive crawl on url: ", splitCommand[0])
		fmt.Println("Unique ID: ", splitCommand[1])
		go crawlHelper(splitCommand[0], splitCommand[1], 3, client, rdb)
	}

}

// realFetcher is real Fetcher that returns real results.
type realFetcher struct {
	client  *http.Client
	graphCh chan graphNode
}

func (f realFetcher) Fetch(url string) (string, []string, error) {
	results := make([]string, 0, initialResultsSize)

	resp, err := f.client.Get(url)
	if err != nil {
		fmt.Println(err)
		return "", nil, err
	}

	z := html.NewTokenizer(resp.Body)

	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			f.graphCh <- graphNode{Parent: url, Children: results, TimeFound: jsonTime(time.Now())}
			return "", results, nil
		case html.StartTagToken, html.EndTagToken:
			tn, _ := z.TagName()
			if len(tn) == 1 && tn[0] == 'a' {
				if tt == html.StartTagToken {
					for key, val, moreAttrs := z.TagAttr(); ; _, val, moreAttrs = z.TagAttr() {
						if string(key) == "href" {
							if isHTTP, _ := regexp.Match(`https?://.*`, val); isHTTP {
								results = append(results, string(val))
							}
							break
						}
						if !moreAttrs {
							break
						}

					}

				}
			}
		}
	}

}
