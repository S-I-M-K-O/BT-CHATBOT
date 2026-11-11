# Documents & links

[← README](../README.md)

- [Your documents](#your-documents)
- [A watched folder](#a-watched-folder)
- [Read a link](#read-a-link)

## Your documents

HERMES can answer questions from your own files: rental agreement, manuals, notes.

- **Add files:** Hub → **Dokumente / Documents** → *Upload*, or simply **drag files onto the chat**. Supported: **PDF** (with a text layer), **Markdown** (`.md`) and **text** (`.txt`). Scanned PDFs without a text layer cannot be read (no OCR). PDF reading needs the browser assets (`sh scripts/fetch-assets.sh`, already done in the Docker image); `.md`/`.txt` work without them.
- **Ask:** "Was steht in meinem Mietvertrag über Haustiere?", "Wie lang ist die Kündigungsfrist laut meinen Unterlagen?", "Durchsuche meine Notizen nach Kundennummer" / "what does my lease say about pets?", "search my notes for the warranty". He answers in 2–5 sentences and names the document. When a normal question is clearly answered by your documents, he offers: "In deinen Notizen steht dazu etwas. Soll ich nachsehen?"
- **Whose documents:** an upload belongs to the household or to one person (the owner select in the tab). A person only gets answers from the household's and their own documents; guests get none. Files dropped on the chat go to the household. With the PIN lock on, the documents list needs the unlock.
- **Local only:** the text is extracted **in the browser**, sent to HERMES, cut into passages and stored in `data/docs/` (one 0600 file per document plus `data/docs/index.json`). Answers come only from the **local** model; document text never leaves the machine.
- **Search:** with `ollama pull embeddinggemma` the search understands meaning ("animals" finds "Tierhaltung"; one- and two-word queries use a lower similarity threshold than sentences, because they score lower). Without it, keyword search is used.
- **Limits:** 2 MB of text per document (a PDF file may be up to 40 MB), 200 documents, 2,000 passages per upload, 50 MB of text in total. Deleting a document (or the person it belongs to) deletes its passages too.

## A watched folder

Optional: set `HERMES_DOCS_DIR` to a folder of notes. HERMES reads `.md` and `.txt` there (`.pdf` only if `pdftotext` is installed, which the Docker image does not have), rescans every 10 minutes and on *Scan folder now*, and never writes to it. In Docker, mount the folder read-only (`- ~/Notes:/notes:ro` and `HERMES_DOCS_DIR=/notes`).

## Read a link

Paste a URL into the chat, optionally with what you want ("https://… fasse kurz zusammen", "https://… what does it say about prices?"). HERMES fetches the page, extracts the article in the browser (Readability, never inserted into the page), and the **local** model summarises it in 4–6 sentences to read aloud. Later, "lies den Link vor" / "read me this article" (typed or spoken) reads the last link again. PDFs work too.

- **Local only, and not stored:** the article goes only to the local model and never into the conversation; the history keeps only `[Link: domain]`. The article is treated as data: instructions inside it are ignored, and a link turn cannot set reminders or switch devices.
- **What is blocked:** only `http(s)` on ports 80 and 443 is allowed. Local and private addresses (localhost, `10.x`, `192.168.x`, `fd…`, `fec0::/10`, IPv4-translated IPv6, the host's own addresses and every network it is on, including IPv6 LAN prefixes) are refused, also after a redirect (at most 3). In Docker the container cannot see your LAN, so set `HERMES_BLOCK_CIDRS` to your LAN prefixes (e.g. `192.168.1.0/24,2a02:db8:1::/64`). Pages over 3 MB (PDF 10 MB) or slower than 15 s are refused.
- **Paywalls and bot blocks:** if only the teaser is readable he says so. Some sites (e.g. zeit.de) refuse automated readers (HTTP 403); then you get that error instead of a summary.
- **What the site sees:** a plain GET request from your IP address with a browser-like `Accept` header and no cookies.
