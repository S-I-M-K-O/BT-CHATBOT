# Smart Home (Home Assistant)

[← README](../README.md)

- [What he can do](#what-he-can-do)
- [What is always refused](#what-is-always-refused)
- [Permissions and privacy](#permissions-and-privacy)
- [Setting up Home Assistant](#setting-up-home-assistant)

HERMES can switch lights and plugs, set the heating, move blinds and run scenes through your own [Home Assistant](https://www.home-assistant.io/), and answer questions about them. It is optional; without it he never mentions a smart home (except a short hint when you clearly give a device command).

## What he can do

- **Commands:** "Mach das Licht im Wohnzimmer an", "Schalte die Kaffeemaschine aus", "Stell die Heizung im Wohnzimmer auf 21 Grad", "Mach den Rollladen in der Küche zu", "Dimm das Licht auf 30 Prozent" / "turn on the living room light", "set the heating to 21.5 degrees", "close the kitchen blinds".
- **How a command runs.** The sentence first goes to Home Assistant's own Assist. If Assist cannot match it, HERMES calls one of a few whitelisted services itself: light on/off/brightness, switch on/off, climate temperature 5–30 °C and heat/off/auto, cover open/close, scene on, media player on/off/play/pause.
- **Questions:** "Ist das Küchenlicht an?", "Wie warm ist es im Wohnzimmer?" / "is the kitchen light on?", "how warm is it in the living room?". Answered from Home Assistant's states, without switching anything.
- **Language:** Assist gets the language of the voice (de/en), or the one set under Settings → Smart Home → *Language for Home Assistant* (e.g. `de-CH`).

## What is always refused

Refused without even asking Home Assistant:

- doors, locks, gates, garage doors and alarm systems ("Entriegle die Haustür", "unlock the front door", "Öffne das Garagentor", "disarm the alarm system");
- scripts and automations.

Vacuums, cameras, fans, valves, buttons and other device types are not controlled either. A sentence that names one of them next to an allowed device ("Schalte das Küchenlicht und Robi ein") is refused as a whole, so Assist can't switch it on the side. The short form without a verb ("Küchenlicht an", "Küchenlicht und Robi ein") is handled the same way, and a device the model picks for a vaguer sentence is checked against your words again before anything is switched.

"Turn off the alarm" means the alarm clock (reminders), not the alarm system. With a security verb ("disarm the alarm", "Alarmanlage unscharf") it is the alarm system and refused.

## Permissions and privacy

- **Who may switch devices** (Settings → Smart Home): only recognised people (default), everyone, or nobody (`homeControl = known | everyone | off`; `off` turns off device **commands** only). Guests and unknown voices can't switch anything unless you choose *everyone*.
- **Who may ask about device states:** only recognised people (default) or everyone (`homeQueries = known | everyone`). This applies even when control is off.
- **Privacy:** device names and states go only to the local model and never into the memory. The token is never sent to the browser or written to the log. Nothing goes to the internet unless your HA URL is on the internet.

## Setting up Home Assistant

One time, about 5 minutes:

1. **A dedicated user:** in Home Assistant, *Settings → People → Users → Add user*, e.g. "hermes", **without** administrator rights. HERMES should not be able to do more than this user.
2. **A long-lived token:** log in as that user, open the **profile** (bottom left) → **Security** → *Long-lived access tokens* → *Create token*. Copy it (Home Assistant shows it once).
3. **Expose only what HERMES may control:** *Settings → Voice assistants → Expose*. Expose only **lights, switches, climate (heating) and covers (blinds)**; un-expose locks, alarm panels, garage doors and anything else. (HERMES refuses those anyway and logs a warning if Assist ever acts on one.)
4. **Give HERMES the address and token:** Settings → **Smart Home** → address `http://192.168.1.20:8123` (your HA's IP and port, no path) and the token → *Save*. The window then tests the connection and shows the HA version and the number of devices.
   - Address and token can only be changed **on the host itself** (`http://localhost:8080`) or with the master token; other devices see them read-only.
   - Changing the address without pasting the token again deletes the stored token, so it can never be sent to a different server.
   - Alternatively set `HA_URL` / `HA_TOKEN` in `.env`; they win over the window (see `.env.example`).
5. **Docker:** use the **LAN IP** of Home Assistant (`http://192.168.1.20:8123`), not `homeassistant.local`: mDNS names do not resolve inside the container.

The status (*connected*, *token rejected*, *unreachable*, *timeout*) also shows in Settings → Smart Home and in `/api/status`. If Home Assistant does not react, check that the device is exposed to Assist and that the token belongs to the HA user, not to a deleted one.
