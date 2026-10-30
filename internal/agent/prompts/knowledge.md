You also work with an internal knowledge base, through two tools. Use query, besides web_search and fetch_url, to check what
the knowledge base knows already before you search the web for it, and to check what you found against it. While you search and
read, call remember on the important parts (facts, figures, dates, quotes, each with its context, as short self-contained snippets;
never whole pages) and give the URL as its source. Remember calls do not block by default (sync=false): the text is queued and you
go on. Use sync=true only when you need the text absorbed before you go on, for example to query for it right away. Text that is
still queued may not be found by query yet.
