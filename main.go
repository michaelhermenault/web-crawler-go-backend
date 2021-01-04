package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"golang.org/x/net/html"
)

const (
	maxLinksScraped         = 7
	timeOutInSeconds        = 2
	crawlResultsTTL         = 60
	crawlDepth              = 7
	maxConcurrencyPerWorker = 3
)

type (
	// Fetcher returns the body of URL and
	// a slice of URLs found on that page.
	Fetcher interface {
		Fetch(url string) (body string, urls []string, err error)
	}
	// SafeMap is a "thread-safe" string->bool Map
	// We'll use it to remember which sites we've already visited
	SafeMap struct {
		sync.Mutex
		v map[string]bool
	}
	graphNode struct {
		Parent    string
		Children  []string
		TimeFound time.Duration
		Depth     int
	}
	finishSentinel struct {
		DoneMessage string
	}
	realFetcher struct {
		client *http.Client
		guard  chan struct{}
	}
	helperOptions struct {
		url, uniqueID string
		depth         int
		client        *http.Client
		rdb           *redis.Client
	}
)

func (safeMap *SafeMap) flip(name string) bool {
	safeMap.Lock()
	defer safeMap.Unlock()
	// Result should be saved
	result := safeMap.v[name]
	// Whatever the value was, turn it to true
	safeMap.v[name] = true
	return result
}

// Crawl uses fetcher to recursively crawl
// pages starting with url, to a maximum of depth.
func Crawl(url string, depth int, fetcher Fetcher, parentChan chan struct{}, resultsChan chan graphNode, startTime time.Time, urlMap *SafeMap) {
	// Once we're done we inform our parent
	defer func() {
		parentChan <- struct{}{}
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
	resultsChan <- graphNode{Parent: url, Children: urls, TimeFound: time.Since(startTime), Depth: depth}

	// fmt.Printf("Crawling: %s %q, child length: %d\n", url, body, len(urls))

	doneCh := make(chan struct{}, len(urls))

	numToExplore := len(urls)

	for _, u := range urls {

		go Crawl(u, depth-1, fetcher, doneCh, resultsChan, startTime, urlMap)
	}

	numFin := 0
	for {
		if numFin >= numToExplore {
			break
		}
		<-doneCh
		numFin++

	}
	close(doneCh)
	return
}

func crawlHelper(args helperOptions) {

	resultsListName := fmt.Sprintf("go-crawler-results-%s", args.uniqueID)
	// In case thread crashes, set ttl beforehand (no memory leaks)
	args.rdb.Expire(ctx, resultsListName, crawlResultsTTL*time.Second)

	doneCh := make(chan struct{})
	graphCh := make(chan graphNode)
	guard := make(chan struct{}, maxConcurrencyPerWorker)

	defer func() {
		close(doneCh)
		close(graphCh)
		close(guard)
	}()

	urlMap := SafeMap{v: make(map[string]bool)}
	go Crawl(args.url, args.depth, realFetcher{client: args.client, guard: guard}, doneCh, graphCh, time.Now(), &urlMap)
	// Loop until crawling is done, publishing results to redis
	for {
		select {
		case <-doneCh:
			marshalled, _ := json.Marshal(finishSentinel{DoneMessage: "true"})
			args.rdb.RPush(ctx, resultsListName, marshalled)
			// TTL will be set after crawl completes
			args.rdb.Expire(ctx, resultsListName, crawlResultsTTL*time.Second)
			fmt.Println("Done recursively crawling: ", args.url)
			return
		case newNode := <-graphCh:
			marshalled, _ := json.Marshal(&newNode)
			args.rdb.RPush(ctx, resultsListName, marshalled)

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
		go crawlHelper(helperOptions{url: splitCommand[0], uniqueID: splitCommand[1], depth: crawlDepth, client: client, rdb: rdb})
	}

}

// realFetcher is real Fetcher that returns real results.
func (f realFetcher) Fetch(urlToFetch string) (string, []string, error) {
	f.guard <- struct{}{}
	defer func() {
		<-f.guard
	}()

	domain, _ := getDomainFromURL(urlToFetch)
	results := make([]string, 0, maxLinksScraped)
	linksScraped := 0
	resp, err := f.client.Get(urlToFetch)

	if err != nil {
		fmt.Println(err)
		return "", nil, err
	}

	defer func() {
		resp.Body.Close()
	}()

	z := html.NewTokenizer(resp.Body)

	for {
		tt := z.Next()

		switch tt {
		case html.ErrorToken:
			return "", results, nil
		case html.StartTagToken:

			tn, _ := z.TagName()
			if len(tn) == 1 && tn[0] == 'a' {
				// Scan anchor tag for href attribute
				key, val, moreAttrs := z.TagAttr()
				for {

					if string(key) == "href" {
						if isHTTP, _ := regexp.Match(`https?://.*`, val); isHTTP {
							childDomain, err := getDomainFromURL(string(val))

							// Check if the url was valid (html document could always be bad)
							// Then check that the domain is different from our parent
							if err == nil && domain != childDomain {
								results = append(results, string(val))
								linksScraped++
								if linksScraped >= maxLinksScraped {
									return "", results, nil
								}

							}

						}
						// Stop reading attributes once we get to the href attribute
						break
					}
					if !moreAttrs {
						break
					}
					key, val, moreAttrs = z.TagAttr()

				}
			}
		}
	}

}

func getDomainFromURL(urlToParse string) (string, error) {
	parsedURL, err := url.Parse(urlToParse)
	if err != nil {
		return "", err
	}
	splitDomain := strings.Split(parsedURL.Host, ".")
	if len(splitDomain) < 2 {
		return "", errors.New("invalid url")
	}
	return splitDomain[len(splitDomain)-2], nil
}
