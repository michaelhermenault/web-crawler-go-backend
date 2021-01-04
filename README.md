# Usage
This go program expects commands to be sent to a specific channel in a Redis database with the default settings. To test it start the cli and run the following command to start crawling xkcd.com:
```publish go-crawler-commands https://xkcd.com,foo```
