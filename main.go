package main

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"golang.org/x/net/html"
)

const initialResultsSize = 50
const timeOutInSeconds = 10

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
	body, urls, err := fetcher.Fetch(url)

	if err != nil {
		// If we can't find the url, return (future iterations)
		fmt.Println(err)
		return
	}

	fmt.Printf("Crawling: %s %q, child length: %d\n", url, body, len(urls))

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

func doTheShit() {
	fmt.Println("Hello!")
}

var ctx = context.Background()

func main() {

	tr := &http.Transport{
		IdleConnTimeout: timeOutInSeconds * time.Second,
	}
	client := &http.Client{Transport: tr}

	rdb := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	pubsub := rdb.Subscribe(ctx, "go-crawler-commands")

	ch := pubsub.Channel()

	for msg := range ch {
		fmt.Println("Staring recursive crawl on url: ", msg.Payload)
		var fetcher = realFetcher{client}
		doneCh := make(chan bool)
		urlMap := SafeMap{v: make(map[string]bool)}
		go Crawl(msg.Payload, 3, fetcher, doneCh, &urlMap)
	}

}

// realFetcher is real Fetcher that returns real results.
type realFetcher struct{ client *http.Client }

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
