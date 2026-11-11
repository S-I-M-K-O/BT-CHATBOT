# Briefing, calendar & check-ins

[← README](../README.md)

- [Morning briefing](#morning-briefing)
- [Calendar (Google)](#calendar-google)
- [Setting up Google Calendar](#setting-up-google-calendar)
- [Check-ins](#check-ins)

## Morning briefing

Say **"Guten Morgen, Hermes"** / **"Good morning, Hermes"** between 4:00 and 12:00, and the first greeting of the day (per person, or per device for guests) gets a short briefing instead of a hello:

> Guten Morgen, Anna. Es ist Freitag, der 6. November, 7:30 Uhr. In Berlin sind es 6 Grad, bedeckt. Heute bis 11 Grad, Regenrisiko 40 Prozent. Heute hast du einen Termin: um 10 Uhr Team-Call. Heute eine Erinnerung, um 18 Uhr: Müll rausbringen. Auf der Einkaufsliste steht: Milch.

- **Sections:** greeting and date, weather, calendar, today's reminders, lists, and (optional) news headlines. A section that fails or is slow is left out. About 70 words.
- **Any time:** "Morgenbriefing", "Lagebericht", "Wie sieht mein Tag aus?" / "brief me", "what does my day look like?". A second "Guten Morgen" that day just gets a hello and the offer to repeat it.
- **Weather** needs a home city (Settings → Location & time). Only the city name goes to Open-Meteo.
- **News** is off by default ("Headlines in the briefing", `briefingNews`). It speaks one headline and offers more: from the Tagesschau with a German voice, from Google News with an English one (see [Live info](live-info.md#news)).
- **Off switch:** untick the briefing on the first good morning (`briefingOnGreeting`); the explicit requests still work.
- Guests get no calendar, no personal reminders and no name.

## Calendar (Google)

HERMES can read and add events in **one** Google Calendar (the primary calendar of one Google account). It's optional; with nothing set up he never mentions a calendar.

- **Ask:** "Was steht morgen an?", "Was habe ich am Freitag?", "Wann ist mein nächster Termin?" / "what's on tomorrow?", "what's my next appointment?". A calendar card comes with the reply.
- **Add:** "Trag morgen um 15 Uhr Zahnarzt ein", "Neuer Termin am Montag um 10 Uhr Friseur für 2 Stunden", "Trag mir am Mittwoch Urlaub ein" (all day) / "add dentist tomorrow at 3pm to my calendar". Events last an hour unless you say otherwise; "rückgängig" / "undo" deletes it again. A plain statement ("Ich hab morgen um 8 einen Zahnarzttermin") gets an offer ("Soll ich das eintragen?") instead.
- **Whose calendar:** you pick the owner when connecting. The owner hears "du hast…"; somebody else hears "In Annas Kalender: …", or a refusal if you chose *Refuse* for other people. Guests never get calendar entries. With the PIN lock on, `/api/calendar/events` needs the unlock cookie.
- **Privacy:** event titles go only to the local model, marked as data, and never leave the machine otherwise. The OAuth token is stored in `data/google_token.json` (0600) and never sent to the browser.

## Setting up Google Calendar

One time, about 10 minutes:

1. Open <https://console.cloud.google.com/>, sign in with the Google account whose calendar HERMES should use, and **create a project** (any name, e.g. "HERMES").
2. **Enable the Calendar API:** *APIs & Services → Library*, search for "Google Calendar API", *Enable*.
3. **OAuth consent screen** (*APIs & Services → OAuth consent screen*, in newer consoles *Google Auth Platform → Branding / Audience*): user type **External**, app name "HERMES", your e-mail as support and developer contact. Add yourself as a test user if asked. HERMES asks only for `calendar.events`.
4. **Publish the app** so its status is **In production**. While it is in "Testing", Google expires the refresh token after **7 days**. You don't need Google's verification for your own account; when you connect, click *Advanced → Go to HERMES (unsafe)*.
5. **Create the OAuth client:** *Credentials → Create credentials → OAuth client ID*, type **Desktop app** (a *Web application* client does not work: HERMES uses a loopback redirect). Copy the **client ID** and **client secret**, or download the JSON.
6. **Give them to HERMES**, either in Settings → **Calendar** (paste or upload the `client_secret_….json`, *Save* — only from the host itself at `http://localhost:8080`; stored in `data/secrets.json` (0600)), or in `.env` as `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` and restart. Not in `hermes.conf`.
7. **Choose the owner** in the same category, then **Connect Google Calendar** — on the host at **http://localhost:8080**, because Google sends you back to `http://localhost:8080/api/calendar/callback`.

Good to know: **"Connect again"** shows up when Google revoked the token or the client ID changed (a new secret alone keeps the link). **Disconnect** revokes and deletes the token; deleting the calendar owner disconnects too. Connect errors name the reason. In the Docker LAN layout, connect from the host's own browser with the master token.

## Check-ins

- **Meetings:** with a connected calendar, HERMES announces your next event a few minutes before it starts ("In 10 Minuten: Team-Call."). Lead time 1–60 minutes (default 10). All-day events and invitations you haven't accepted are skipped. They ignore quiet hours, because a meeting doesn't wait.
- **Sitting:** optional. After you've been active at the computer for a while (20–240 min, default 60), HERMES suggests standing up. Needs Chrome or Edge, `localhost` or HTTPS, and the idle-detection permission. It waits during calendar events, while he's talking, in quiet hours and while check-ins are paused. *Later* (15 min) and *Not today* (this device, until midnight).
- **Pause by voice:** "keine Check-ins mehr heute", "pausiere die Check-ins für eine Stunde", "Check-ins wieder an" / "pause check-ins for an hour", "resume check-ins". "Nicht jetzt" / "not now" right after a check-in pauses too. Your own reminders still ring.
