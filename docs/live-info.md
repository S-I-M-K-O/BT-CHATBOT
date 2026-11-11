# Live info: weather, time, news and pictures

[← README](../README.md)

- [How it works](#how-it-works)
- [Weather](#weather)
- [Time and date](#time-and-date)
- [News](#news)
- [Follow-ups and errors](#follow-ups-and-errors)
- [Image search](#image-search)
- [What leaves the machine](#what-leaves-the-machine)

## How it works

HERMES answers weather, time and news questions **himself**, without any language model and without an API key. He fetches the data directly from free public services and replies with short fixed sentences in the language of the current voice: a German voice (JarvisDE) answers in German, an English voice (BT-7274, Scorch, PDA) in English, whatever language the question was in.

The router in `internal/hermes/routing.go` (`wantsRealtime`) decides whether a message is a live-data question at all; it has many guards against false positives ("Ich habe heute keine Zeit" is not a time question). `internal/hermes/realtime.go` then works out which kind it is and answers.

## Weather

Source: [Open-Meteo](https://open-meteo.com) geocoding (the place) and forecast (7 days).

- **Current conditions:** "Wie ist das Wetter?" / "what's the weather in London?" → temperature, conditions, today's high and rain chance.
- **A day or a span:** today, tomorrow ("Wie wird das Wetter morgen in Berlin?"), the day after, a named weekday ("am Samstag"), the weekend, the next days.
- **Rain and umbrella:** "Brauche ich einen Schirm?" / "will it rain tomorrow?" → the rain chance and advice.
- **Where:** the place you name, or the home city from Settings → Location & time (`homeCity`). Without either, he asks "Für welchen Ort?" / "For which place?", and a short answer like "Hamburg" or "in London please" resumes the question.
- A **weather card** shows the place, current conditions (temperature, feels-like, wind) and up to four days with high/low, rain chance and an icon.

## Time and date

- **Here:** "Wie spät ist es?" / "what time is it?", "Welcher Tag ist heute?" / "what's the date?" use the server clock in the household time zone (Settings → Location & time, or the browser's).
- **Elsewhere:** "Wie spät ist es in Tokio?" looks up the city's IANA time zone through Open-Meteo geocoding. He says when the other city is already on the next day, or still on the previous one, and names the UTC offset when you ask for the time zone.
- No card; the answer is one sentence.

## News

| Question | Source |
| --- | --- |
| German top news ("Was gibt es Neues?") | [Tagesschau API](https://www.tagesschau.de/api2u/news), national stories, newest first; Google News German if the Tagesschau is unreachable |
| English top news ("any news?") | Google News RSS in English |
| A topic ("Nachrichten über Fußball", "news about football") | Google News RSS search, in the reply language |

He speaks three headlines; the **news card** shows five (with the Tagesschau's topline) and the source. The [morning briefing](briefing-calendar-checkins.md#morning-briefing) uses the same sources for its optional headline.

## Follow-ups and errors

- **Follow-ups:** right after a weather or time answer, "Und morgen?" / "And in Paris?" from the same speaker on the same device continues it. The last request is kept in memory for 10 minutes and dropped when a person is deleted.
- **Limits:** every request has a 5-second timeout and a size cap. Weather is cached for 10 minutes and news for 15; failures are never cached. If a slow service takes more than 1.5 seconds, the UI shows a busy notice.
- **Errors** are short sentences: "Ich erreiche den Wetterdienst gerade nicht." / "I can't reach the weather service right now.", and "Ich finde keinen Ort namens X." for an unknown place.

## Image search

"Zeig mir Bilder von Leuchttürmen" / "find a picture of a lighthouse" searches [Openverse](https://openverse.org), which indexes openly licensed pictures from Wikimedia Commons, Flickr and other collections; no key needed. The pictures are copied to `data/gen/` (pruned after 24 hours, with a size budget) and open in an overlay with a full-size lightbox and download link.

HERMES does not draw or generate pictures. A request like "male ein Bild von …" / "draw a picture of …" gets a fixed reply pointing to image search, so the local model never pretends it drew something.

## What leaves the machine

| Service | What it gets |
| --- | --- |
| Open-Meteo | Only the city name (geocoding) and its coordinates (forecast) |
| Tagesschau | A plain request without parameters |
| Google News | The language and, for a topic, the topic words |
| Openverse | The search words |

Never the conversation history, names, memories or anything else personal. The privacy tests in `internal/hermes/privacy_test.go` check this against fake services. See also [Security & privacy](security.md).
