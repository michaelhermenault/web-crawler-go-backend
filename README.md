# Usage
This go program expects commands to be sent to a specific channel in a Redis database with the default settings. To test it start the Redis cli and run the following command to start crawling xkcd.com:
```publish go-crawler-commands https://xkcd.com,foo```

To view the results, simply watch the output of the go program. The results will also be output to a list in Redis with the key `go-crawler-results-foo`. 
