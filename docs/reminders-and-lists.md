# Reminders & lists

[← README](../README.md)

- [Reminders and timers](#reminders-and-timers)
- [Repeating reminders](#repeating-reminders)
- [Who announces them](#who-announces-them)
- [Lists](#lists)

## Reminders and timers

Set them by voice or typed, in German or English; the voice's language decides the language of the reply.

- **Set:** "Erinnere mich in 20 Minuten an die Pizza", "Erinnere mich morgen um 8, die Pflanzen zu gießen", "Stell einen Timer auf 10 Minuten", "Weck mich um 7" / "remind me in 20 min to take the pizza out", "remind me tomorrow at 8am to …", "remind me next Monday to …", "set a timer for 10 minutes".
- **Confirmation.** HERMES always answers with the absolute time ("Reminder set for today at 2:50 PM: …"), so a misheard time is caught at once. Without a time he asks "Wann soll ich dich erinnern?". A day without a clock time means 9:00; "um 7" is the next 7 o'clock.
- **Private:** add "privat" / "privately" and only "Anna, du hast eine Erinnerung." is spoken.
- **Fix right after setting:** "nein, abends" / "nein, um 20 Uhr" / "no, in the evening" (within 2 minutes).
- **Ask and cancel:** "Welche Erinnerungen habe ich?", "Wie lange noch?" / "how long is left?", "Lösch den Timer" / "cancel the timer". "Rückgängig" / "undo" brings back what was just set or cancelled.
- **When it rings** he speaks it (with a chime) and **repeats it every minute until you acknowledge it** (at most 5 repeats after the first announcement): "okay", "danke", "stopp", or the *Erledigt* / *Done* button. "Schlummern", "noch 5 Minuten" / "snooze" moves it. An acknowledgement on any device stops the repeats everywhere.

## Repeating reminders

- **Examples:** "Erinnere mich jeden Montag um 8 an den Müll", "jeden Tag um 22 Uhr Tabletten", "jeden Werktag", "montags und donnerstags um 18 Uhr", "jeden zweiten Tag", "alle zwei Wochen am Freitag", "jeden Abend die Blumen gießen", "am Ersten jedes Monats Miete zahlen", "jedes Jahr am 24.12.", "jeden Tag ab Montag um 9" / "remind me every Monday at 8 to take out the trash", "every day at 10pm", "every weekday", "every other Friday", "on the 1st of every month", "every year on December 24", "water the plants every morning".
- **Confirmation.** He confirms the rule and the first time ("Wiederkehrende Erinnerung gesetzt, jeden Montag um 8 Uhr: den Müll. Das erste Mal am Montag, 9. November, um 8 Uhr.").
- **Rules.** A series keeps its clock time across daylight-saving changes; day 31 means the last day of the month. Missed occurrences never fire in a burst after downtime. Hourly repeats ("jede Stunde") are not supported.
- **Done.** "Erledigt" / "done" finishes only the current occurrence. The *Erledigt* / *Done* button on a series card appears once it is due and sends the occurrence it shows, so an old card never finishes a later one.
- **Skip or stop.** "Überspring die nächste Müll-Erinnerung" / "skip the next one" skips one; "lösch die ganze Serie", "lösch die Müll-Serie" / "delete the whole rent series" ends it (without a name only series qualify; with several he asks which). Otherwise he asks "Nur die nächste oder die ganze Serie?", and "nur die nächste", "die ganze Serie", "komplett" / "just this one", "the whole series", "stop it for good" answer it.
- **Several matches** (two identical timers, say) get a numbered list: answer "die erste" / "Nummer 2" / "the last one" or "alle" / "both".
- **Reminders tab.** A series shows "↻ every Monday …" with *Skip next* and *Stop series*, and the add form has a repeat choice.

## Who announces them

Reminders are kept by the server (`data/reminders.json`) and fire on time, but they are **announced by an open HERMES tab**. If none is open, they wait and are announced as "Verpasst: …" / "Missed: …" when one connects.

- **One announcing tab:** per browser, only one tab speaks — **the one you last clicked or typed in**. The others stay silent.
- **Speak here** (Settings → Reminders & check-ins): on by default on this computer (localhost), off on other devices, which only show a silent notice.
- **Quiet hours** (default 22:00–07:00) silence nudges, but not reminders you set yourself or meeting check-ins.
- **Chrome Memory Saver** can put a background tab to sleep and miss a reminder: add `localhost` under `chrome://settings/performance` → "Always keep these sites active".
- **System notifications** for a hidden tab need `localhost` or HTTPS. Allow them in the same category.

## Lists

A shopping list and a to-do list are built in; more can be created in the Lists tab.

- **Voice:** "Setz Milch und Eier auf die Einkaufsliste", "Was steht auf der Einkaufsliste?", "Streich Milch" (ticks it off), "Nimm Milch von der Einkaufsliste" (removes it), "Leere die Einkaufsliste" (asks first), "Schick mir die Einkaufsliste" / "add milk to the shopping list", "what's on my shopping list?", "cross off milk", "clear the shopping list". "die Liste" / "the list" means the list you used last. Everything can be undone.
- **Phone page:** set `HERMES_LISTS_TOKEN` in `.env` and open `http://<host-IP>:8080/lists.html?token=<lists token>` once on the phone; after that `http://<host-IP>:8080/lists.html` is enough (`?lang=de|en` optional). It updates live, polls every 30 s, and shows the last copy read-only when HERMES is unreachable. If the server refuses the token (wrong or revoked), the page deletes its saved copy and shows only "No access". The lists token opens only this page, `/api/lists` and list change events. In Docker this needs the [LAN layout](setup.md#reaching-hermes-from-other-devices).
- **Outside the home:** use Tailscale (or another VPN) rather than forwarding the port. With Tailscale Serve (HTTPS) the Share button can use the phone's share sheet; on plain `http://` it copies the list as text.
