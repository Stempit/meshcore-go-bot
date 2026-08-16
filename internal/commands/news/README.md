# news — RSS feed reader

Package `news` registers the `.news` command: lists feeds or returns recent RSS items.
It reads per-feed text files under `log/news/`, which are populated by the **separate**
[newswire](https://github.com/Stempit/newswire) poller — this plugin only reads them.

## Command surface

```
.news                       – list available feeds
.news <feed>                – last 5 items
.news <feed> <n>            – last n items
.news <feed> <n> <offset>   – last n items, skipping the most recent <offset>
```

Each feed is `log/news/<feed>.txt`, one item title per line, oldest→newest. Items are
returned newest-first (so the most recent arrives last in chat), each as a separate
logical message. Long titles are shrunk at the send site (the `news` shrink override,
typically `skip`). Missing feed → `feed "…" not found`; no feeds dir → `no feeds yet`.

## Configuration

None. Reads from `log/news/` (path is fixed).
