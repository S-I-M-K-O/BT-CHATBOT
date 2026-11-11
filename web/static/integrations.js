/*
 * HERMES integrations settings (W2-N, SPEC 5.5 "integrations.js").
 *
 * Part 1 (UMD, no DOM): pure helpers for the browser
 * (window.HermesIntegrations) and the node tests:
 *
 *   maskSecret, credentialState, normalizeIntegrations,
 *   normalizeCalendarStatus, calendarState, parseClientSecretJson,
 *   googleBody, validatePin, pinRequest, safeAuthUrl, ownerOptions,
 *   errorKey, calendarReturn, formatUntil, STRINGS,
 *   normalizeHaUrl, normalizeHA, haRequest, normalizeHomeStatus, homeState,
 *   homeStateText, normalizeHomeEntities, haLanguageOptions
 *
 * Part 2 (browser only, needs window.Hermes): four Settings sections
 *   - "Kalender" / "Calendar" (order 40): Google client id / secret (never
 *     shown, only "saved" / "from .env"; JSON upload), owner person,
 *     `calendarOthers`, connect (POST /api/calendar/connect → navigate to
 *     the Google URL), status, disconnect. SPEC 3.7, 3.11.
 *   - "Privatsphäre" / "Privacy" (order 45): household PIN set / change /
 *     remove (POST /api/integrations/lock), `personalLock`,
 *     `keepHistoryOnSpeakerChange`, lock this device now. SPEC 3.11, 4.10.
 *   - "Smart Home" (id "home", order 50): Home Assistant address + long-lived
 *     token (POST /api/integrations/ha, admin only; the token is never
 *     shown; a new address without a token deletes the stored one, so the
 *     UI asks first), env read-only state, test (POST /api/home/test for
 *     admins, else the cached GET /api/home/status), device count with the
 *     blocked ones marked (GET /api/home/entities), `homeControl`,
 *     `homeQueries`, `haLanguage`, setup help. SPEC 3.8, 3.11, 5.5, 9.5.
 *   - "Check-ins" (order 60): `checkinMeetings`, `checkinMeetingLead`,
 *     `checkinSitting` (asks for the idle-detection permission through
 *     HermesCheckins.ui in the toggle's gesture), `checkinSittingMin`,
 *     `quietStart`, `quietEnd`, pause state. SPEC 2.4, 5.5.
 *
 * Plain settings use data-setting="<key>" (app.js binds them and saves them
 * with the Settings "Save" button). Secrets and the PIN go only to their own
 * admin endpoints and are never stored in the page. Server texts are set
 * with textContent only.
 */
(function (root, factory) {
  var api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root && typeof root === "object") root.HermesIntegrations = api;
})(typeof window !== "undefined" ? window : typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  // the server takes 1..512 visible ASCII characters per field (secrets.go
  // validCredential) and a 16 KB body; the JSON file travels inside a JSON
  // string, so escaping can almost double it
  var ID_MAX = 512;
  var SECRET_MAX = 512;
  var JSON_MAX = 8 * 1024;
  var PERSON_ID = /^p_[A-Za-z0-9]{1,40}$/;
  var AUTH_PREFIX = "https://accounts.google.com/";

  var STRINGS = {
    en: {
      // calendar
      secCalendar: "Calendar",
      calStatus: "Status",
      calConnected: "Connected.",
      calConnectedOwner: "Connected – {name}'s calendar.",
      calDisconnected: "Not connected.",
      calRelink: "The connection has expired. Connect again.",
      calNoClient: "Enter the Google client ID and secret first.",
      calUnavailable: "The calendar is not available on this server version.",
      calError: "Calendar problem: {msg}",
      calLoading: "Checking …",
      clientId: "Google client ID",
      clientSecret: "Google client secret",
      credSet: "saved",
      credEnv: "set in the environment (.env)",
      credUnset: "not set",
      credEnvHint: "The Google client comes from GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET and cannot be changed here.",
      adminOnly: "Viewing only: the Google client, the PIN and the connection can only be changed directly on the Mac (http://localhost:8080).",
      clientIdPlaceholder: "…apps.googleusercontent.com",
      secretPlaceholderSet: "•••••••• (saved – type to replace)",
      idPlaceholderSet: "•••••••• (saved – type to replace)",
      jsonUpload: "Load client_secret….json",
      saveClient: "Save client",
      clearClient: "Remove client",
      clearClientConfirm: "Remove the Google client ID and secret from HERMES?",
      clientSaved: "Google client saved.",
      clientSavedMasked: "Google client saved ({id}).",
      clientCleared: "Google client removed.",
      clientNothing: "Type a client ID or secret, or load the JSON file.",
      setupHint:
        "Google Cloud Console → APIs & Services: enable the Google Calendar API, create an OAuth client ID of type “Desktop app”, and set the app to “In production” (otherwise the connection expires after 7 days).",
      owner: "Whose calendar is this?",
      ownerHousehold: "Household / just me",
      ownerUnknown: "Unknown person",
      ownerChangeHint: "To change the owner, disconnect and connect again.",
      others: "When someone else asks",
      othersThird: "Read it to them (“In Anna's calendar: …”)",
      othersRefuse: "Refuse",
      connect: "Connect Google Calendar",
      reconnect: "Connect again",
      disconnect: "Disconnect",
      disconnectConfirm: "Disconnect the calendar? HERMES revokes its access and deletes the token.",
      disconnected: "Calendar disconnected.",
      connecting: "Opening Google …",
      connectedToast: "Calendar connected.",
      connectFailed: "Connecting the calendar failed: {reason}",
      reason_denied: "access was not granted on Google's page.",
      reason_state: "the sign-in expired or was opened twice. Please connect again.",
      reason_exchange: "Google did not accept the sign-in code. Check the client id and secret.",
      reason_scope: "calendar access was not ticked on Google's page.",
      reason_refresh: "Google sent no refresh token. Remove HERMES under myaccount.google.com/permissions and connect again.",
      reason_store: "the token could not be saved on this Mac.",
      reason_owner: "the chosen person no longer exists.",
      reason_unknown: "unknown error.",
      cardAdded: "In the calendar: {title}, {when}",
      // privacy
      secPrivacy: "Privacy",
      pinStatus: "Household PIN",
      pinSet: "A PIN is set.",
      pinNone: "No PIN set.",
      pinNew: "New PIN (4–12 digits)",
      pinConfirm: "Repeat the new PIN",
      pinOld: "Current PIN",
      pinSave: "Set PIN",
      pinChange: "Change PIN",
      pinRemove: "Remove PIN",
      pinRemoveConfirm: "Remove the PIN? The personal lock is switched off too.",
      pinSaved: "PIN saved.",
      pinChanged: "PIN changed.",
      pinRemoved: "PIN removed; the personal lock is off.",
      errPinFormat: "The PIN must be 4 to 12 digits.",
      errPinMismatch: "The two PINs do not match.",
      errPinOld: "Enter the current PIN.",
      errPinWrong: "The current PIN is wrong.",
      errPinSame: "The new PIN is the same as the current one.",
      lock: "Personal lock",
      lockHint:
        "With the lock on, choosing a person in the speaker chip, the Memory and People panels, documents, calendar events and exports need the PIN. Voice ID is not a security feature; the lock stops casual click-through.",
      lockNeedsPin: "Set a PIN first.",
      lockOn: "Personal lock switched on.",
      lockOff: "Personal lock switched off.",
      unlockedHere: "This device is unlocked.",
      lockNow: "Lock now",
      lockedNow: "Locked.",
      keepHistory: "Keep the chat history visible when the speaker changes",
      keepHistoryHint: "Off: earlier messages of other people are collapsed when someone else starts talking.",
      // check-ins
      secCheckins: "Check-ins",
      meetings: "Announce upcoming calendar events",
      meetingsHint: "HERMES says “In 10 minutes: Team call.” before an accepted event (needs the calendar).",
      meetingsNoCal: "The calendar is not connected, so there are no event check-ins yet.",
      lead: "Minutes before the event",
      sitting: "Remind me to stand up after sitting for a long time",
      sittingHint:
        "Uses the browser's idle detection on this device. A nudge comes only on the tab that announces reminders, not during calendar events, quiet hours or while HERMES is talking.",
      sittingMin: "After how many minutes",
      sitRunning: "Active on this device. Current stretch: {n} min.",
      sitRunningIdle: "Active on this device. You seem to be away.",
      sitUnsupported: "Not available in this browser (needs Chrome or Edge with idle detection).",
      sitInsecure: "Not available here: idle detection needs http://localhost or HTTPS.",
      sitDenied: "Not available: the idle-detection permission was denied. Allow it in the site settings.",
      sitAsk: "Not allowed on this device yet.",
      sitAllow: "Allow on this device",
      sitError: "Idle detection could not start.",
      sitOff: "Off.",
      sitLocalOff: "Paused on this device until {until}.",
      sitResume: "Resume",
      pausedUntil: "Check-ins are paused until {until} (by voice).",
      busyUntil: "Calendar event in progress until {until}.",
      quietStart: "Quiet hours from",
      quietEnd: "until",
      quietHint: "During quiet hours HERMES gives no sitting nudges. Reminders you set and calendar check-ins are still announced.",
      // smart home
      secHome: "Smart Home",
      haStatusLabel: "Home Assistant",
      haLoading: "Checking …",
      haUnconfigured: "Not set up.",
      haNoToken: "Address saved, but no token yet.",
      haConnected: "Connected.",
      haConnectedVersion: "Connected – Home Assistant {version}.",
      haUnreachable: "Home Assistant is not responding. Check the address and that Home Assistant is running.",
      haAuth: "Home Assistant rejected the token. Create a new long-lived token and save it here.",
      haTimeout: "Home Assistant took too long to answer.",
      haBadResponse: "Something answers at this address, but it does not look like Home Assistant.",
      haError: "Connection problem: {msg}",
      haErrorPlain: "Connection problem.",
      haUnavailable: "Smart home is not available on this server version.",
      haEntities: "{n} devices are available to HERMES.",
      haEntitiesBlocked: "{n} devices are available to HERMES, {b} blocked (locks, doors, gates, alarms, scripts).",
      haEntitiesShow: "Show devices",
      haBlocked: "blocked",
      haUrl: "Home Assistant address",
      haUrlPlaceholder: "http://192.168.1.20:8123",
      haToken: "Long-lived access token",
      haTokenPlaceholder: "Paste the token from Home Assistant",
      haTokenPlaceholderSet: "•••••••• (saved – type to replace)",
      haEnvHint: "HA_URL / HA_TOKEN in the environment (.env) fix these values; they cannot be changed here.",
      haAdminOnly: "Viewing only: the Home Assistant address and token can only be changed directly on the Mac (http://localhost:8080).",
      haBindHint:
        "The token belongs to the address. If you change the address, paste the token again in the same step – otherwise HERMES deletes the saved token so it is never sent to a different host.",
      haUrlChangeConfirm: "The address changes, but no new token was entered. HERMES deletes the saved token so it is never sent to the new address. Continue?",
      haSave: "Save",
      haClear: "Remove",
      haClearConfirm: "Remove the Home Assistant address and token from HERMES?",
      haSaved: "Home Assistant saved.",
      haSavedNoToken: "Address saved; the old token was deleted. Paste the token for the new address.",
      haCleared: "Home Assistant removed.",
      haNothing: "Change the address or paste a token first.",
      haTest: "Test connection",
      haTesting: "Testing …",
      haTestOk: "Home Assistant is reachable.",
      haLanguage: "Language for Home Assistant",
      haLangAuto: "Same as the reply (German or English)",
      haLangDe: "German",
      haLangEn: "English",
      haLangHint: "The language HERMES uses when it passes a command to Home Assistant Assist. Keep “same as the reply” unless your Assist understands only one language.",
      homeControl: "Who may switch devices",
      hcEveryone: "Everyone",
      hcKnown: "Only recognised people (default)",
      hcOff: "Nobody (no device commands)",
      homeQueries: "Who may ask about device states",
      hqKnown: "Only recognised people (default)",
      hqEveryone: "Everyone",
      homeAccessHint: "“Recognised” means voice ID knows the speaker. Locks, doors, gates and alarms are never controlled, whatever is set here.",
      homeOffHint: "Device control is off: HERMES switches nothing. Questions about devices (\"Is the kitchen light on?\") still follow the setting \"Who may ask about device states\".",
      haSetupTitle: "Setup",
      haSetup1: "In Home Assistant, create a separate user without admin rights for HERMES.",
      haSetup2: "Log in as that user, open the profile → Security → Long-lived access tokens → Create token, and paste the token here.",
      haSetup3: "Under Settings → Voice assistants → Expose, expose only the lights, switches, thermostats and blinds HERMES should control.",
      errHaUrl: "The address must look like http://192.168.1.20:8123 (no path).",
      errHaToken: "The token looks wrong (no spaces, at most 2048 characters).",
      errHaUrlFirst: "Enter the Home Assistant address first.",
      errEnvOverride: "The address is set by HA_URL in the environment.",
      // errors
      errLocalOnly: "Only directly on the Mac (http://localhost:8080).",
      errLocked: "Locked. Unlock with the household PIN first.",
      errNoClient: "Enter the Google client ID and secret first.",
      errUnavailable: "Not available on this server version.",
      errTooMany: "Too many attempts. Wait a minute.",
      errJson: "That is not a Google client_secret JSON file.",
      errJsonWeb: "This is a “Web application” client. Create an OAuth client of type “Desktop app”.",
      errJsonLarge: "That file is too large.",
      errIdFormat: "The client ID looks wrong (no spaces, at most 512 characters).",
      errSecretFormat: "The client secret looks wrong (no spaces, at most 512 characters).",
      errBadClient: "The client ID or client secret is invalid.",
      errTooLarge: "That is too large.",
      errConflict: "This was just changed somewhere else. Check and try again.",
      errAuthUrl: "The server sent an unexpected sign-in address.",
      errReadOnly: "The data file was written by a newer HERMES; changes are blocked.",
      errGeneric: "That did not work: {msg}",
    },
    de: {
      secCalendar: "Kalender",
      calStatus: "Status",
      calConnected: "Verbunden.",
      calConnectedOwner: "Verbunden – Kalender von {name}.",
      calDisconnected: "Nicht verbunden.",
      calRelink: "Die Verbindung ist abgelaufen. Bitte neu verbinden.",
      calNoClient: "Trage zuerst die Google-Client-ID und das Secret ein.",
      calUnavailable: "Der Kalender ist in dieser Server-Version nicht verfügbar.",
      calError: "Kalender-Problem: {msg}",
      calLoading: "Wird geprüft …",
      clientId: "Google-Client-ID",
      clientSecret: "Google-Client-Secret",
      credSet: "gespeichert",
      credEnv: "aus der Umgebung (.env)",
      credUnset: "nicht gesetzt",
      credEnvHint: "Der Google-Client kommt aus GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET und lässt sich hier nicht ändern.",
      adminOnly: "Nur Ansicht: Google-Client, PIN und Verbindung lassen sich nur direkt am Mac (http://localhost:8080) ändern.",
      clientIdPlaceholder: "…apps.googleusercontent.com",
      secretPlaceholderSet: "•••••••• (gespeichert – zum Ersetzen eintippen)",
      idPlaceholderSet: "•••••••• (gespeichert – zum Ersetzen eintippen)",
      jsonUpload: "client_secret….json laden",
      saveClient: "Client speichern",
      clearClient: "Client entfernen",
      clearClientConfirm: "Google-Client-ID und Secret aus HERMES entfernen?",
      clientSaved: "Google-Client gespeichert.",
      clientSavedMasked: "Google-Client gespeichert ({id}).",
      clientCleared: "Google-Client entfernt.",
      clientNothing: "Gib eine Client-ID oder ein Secret ein oder lade die JSON-Datei.",
      setupHint:
        "Google Cloud Console → APIs & Dienste: Google Calendar API aktivieren, eine OAuth-Client-ID vom Typ „Desktop-App“ anlegen und die App auf „In Produktion“ stellen (sonst läuft die Verbindung nach 7 Tagen ab).",
      owner: "Wessen Kalender ist das?",
      ownerHousehold: "Haushalt / nur ich",
      ownerUnknown: "Unbekannte Person",
      ownerChangeHint: "Um den Besitzer zu ändern, trenne die Verbindung und verbinde neu.",
      others: "Wenn jemand anderes fragt",
      othersThird: "Vorlesen („In Annas Kalender: …“)",
      othersRefuse: "Ablehnen",
      connect: "Google Kalender verbinden",
      reconnect: "Neu verbinden",
      disconnect: "Trennen",
      disconnectConfirm: "Kalender trennen? HERMES widerruft den Zugriff und löscht das Token.",
      disconnected: "Kalender getrennt.",
      connecting: "Google wird geöffnet …",
      connectedToast: "Kalender verbunden.",
      connectFailed: "Die Kalenderverbindung hat nicht geklappt: {reason}",
      reason_denied: "Der Zugriff wurde auf der Google-Seite nicht erlaubt.",
      reason_state: "Die Anmeldung ist abgelaufen oder wurde doppelt geöffnet. Bitte neu verbinden.",
      reason_exchange: "Google hat den Anmeldecode nicht akzeptiert. Prüfe Client-ID und Secret.",
      reason_scope: "Der Kalenderzugriff wurde auf der Google-Seite nicht angehakt.",
      reason_refresh: "Google hat kein Refresh-Token geschickt. Entferne HERMES unter myaccount.google.com/permissions und verbinde neu.",
      reason_store: "Das Token konnte auf diesem Mac nicht gespeichert werden.",
      reason_owner: "Die gewählte Person gibt es nicht mehr.",
      reason_unknown: "Unbekannter Fehler.",
      cardAdded: "Im Kalender: {title}, {when}",
      secPrivacy: "Privatsphäre",
      pinStatus: "Haushalts-PIN",
      pinSet: "Eine PIN ist gesetzt.",
      pinNone: "Keine PIN gesetzt.",
      pinNew: "Neue PIN (4–12 Ziffern)",
      pinConfirm: "Neue PIN wiederholen",
      pinOld: "Aktuelle PIN",
      pinSave: "PIN setzen",
      pinChange: "PIN ändern",
      pinRemove: "PIN entfernen",
      pinRemoveConfirm: "PIN entfernen? Die persönliche Sperre wird dabei ausgeschaltet.",
      pinSaved: "PIN gespeichert.",
      pinChanged: "PIN geändert.",
      pinRemoved: "PIN entfernt; die persönliche Sperre ist aus.",
      errPinFormat: "Die PIN muss aus 4 bis 12 Ziffern bestehen.",
      errPinMismatch: "Die beiden PINs stimmen nicht überein.",
      errPinOld: "Gib die aktuelle PIN ein.",
      errPinWrong: "Die aktuelle PIN ist falsch.",
      errPinSame: "Die neue PIN ist dieselbe wie die aktuelle.",
      lock: "Persönliche Sperre",
      lockHint:
        "Mit Sperre brauchen die Personenwahl im Sprecher-Chip, die Bereiche Gedächtnis und Personen, Dokumente, Kalendertermine und Exporte die PIN. Stimmerkennung ist kein Schutz; die Sperre verhindert nur beiläufiges Durchklicken.",
      lockNeedsPin: "Setze zuerst eine PIN.",
      lockOn: "Persönliche Sperre eingeschaltet.",
      lockOff: "Persönliche Sperre ausgeschaltet.",
      unlockedHere: "Dieses Gerät ist entsperrt.",
      lockNow: "Jetzt sperren",
      lockedNow: "Gesperrt.",
      keepHistory: "Chatverlauf beim Sprecherwechsel sichtbar lassen",
      keepHistoryHint: "Aus: Frühere Nachrichten anderer Personen werden eingeklappt, sobald jemand anderes spricht.",
      secCheckins: "Check-ins",
      meetings: "Anstehende Termine ansagen",
      meetingsHint: "HERMES sagt „In 10 Minuten: Team-Call.“ vor einem angenommenen Termin (braucht den Kalender).",
      meetingsNoCal: "Der Kalender ist nicht verbunden, daher gibt es noch keine Termin-Hinweise.",
      lead: "Minuten vor dem Termin",
      sitting: "Ans Aufstehen erinnern, wenn ich lange sitze",
      sittingHint:
        "Nutzt die Leerlauferkennung des Browsers auf diesem Gerät. Ein Hinweis kommt nur im Tab, der Erinnerungen ansagt – nicht während Terminen, in der Ruhezeit oder während HERMES spricht.",
      sittingMin: "Nach wie vielen Minuten",
      sitRunning: "Auf diesem Gerät aktiv. Aktuelle Sitzzeit: {n} Min.",
      sitRunningIdle: "Auf diesem Gerät aktiv. Du scheinst gerade weg zu sein.",
      sitUnsupported: "In diesem Browser nicht verfügbar (braucht Chrome oder Edge mit Leerlauferkennung).",
      sitInsecure: "Hier nicht verfügbar: Die Leerlauferkennung braucht http://localhost oder HTTPS.",
      sitDenied: "Nicht verfügbar: Die Berechtigung zur Leerlauferkennung wurde abgelehnt. Erlaube sie in den Website-Einstellungen.",
      sitAsk: "Auf diesem Gerät noch nicht erlaubt.",
      sitAllow: "Auf diesem Gerät erlauben",
      sitError: "Die Leerlauferkennung konnte nicht starten.",
      sitOff: "Aus.",
      sitLocalOff: "Auf diesem Gerät pausiert bis {until}.",
      sitResume: "Fortsetzen",
      pausedUntil: "Check-ins sind bis {until} pausiert (per Sprache).",
      busyUntil: "Termin läuft bis {until}.",
      quietStart: "Ruhezeit von",
      quietEnd: "bis",
      quietHint: "In der Ruhezeit gibt HERMES keine Sitz-Hinweise. Erinnerungen, die du stellst, und Termin-Hinweise werden trotzdem angesagt.",
      secHome: "Smart Home",
      haStatusLabel: "Home Assistant",
      haLoading: "Wird geprüft …",
      haUnconfigured: "Nicht eingerichtet.",
      haNoToken: "Adresse gespeichert, aber noch kein Token.",
      haConnected: "Verbunden.",
      haConnectedVersion: "Verbunden – Home Assistant {version}.",
      haUnreachable: "Home Assistant antwortet nicht. Prüfe die Adresse und ob Home Assistant läuft.",
      haAuth: "Home Assistant hat das Token abgelehnt. Erstelle ein neues langlebiges Token und speichere es hier.",
      haTimeout: "Home Assistant hat zu lange gebraucht.",
      haBadResponse: "Unter dieser Adresse antwortet etwas, aber es sieht nicht nach Home Assistant aus.",
      haError: "Verbindungsproblem: {msg}",
      haErrorPlain: "Verbindungsproblem.",
      haUnavailable: "Smart Home ist in dieser Server-Version nicht verfügbar.",
      haEntities: "{n} Geräte stehen HERMES zur Verfügung.",
      haEntitiesBlocked: "{n} Geräte stehen HERMES zur Verfügung, {b} gesperrt (Schlösser, Türen, Tore, Alarmanlagen, Skripte).",
      haEntitiesShow: "Geräte anzeigen",
      haBlocked: "gesperrt",
      haUrl: "Home-Assistant-Adresse",
      haUrlPlaceholder: "http://192.168.1.20:8123",
      haToken: "Langlebiges Zugriffstoken",
      haTokenPlaceholder: "Token aus Home Assistant einfügen",
      haTokenPlaceholderSet: "•••••••• (gespeichert – zum Ersetzen eintippen)",
      haEnvHint: "HA_URL / HA_TOKEN aus der Umgebung (.env) legen diese Werte fest; sie lassen sich hier nicht ändern.",
      haAdminOnly: "Nur Ansicht: Adresse und Token von Home Assistant lassen sich nur direkt am Mac (http://localhost:8080) ändern.",
      haBindHint:
        "Das Token gehört zur Adresse. Wenn du die Adresse änderst, füge das Token im selben Schritt neu ein – sonst löscht HERMES das gespeicherte Token, damit es nie an einen anderen Rechner geht.",
      haUrlChangeConfirm: "Die Adresse ändert sich, aber es wurde kein neues Token eingegeben. HERMES löscht das gespeicherte Token, damit es nie an die neue Adresse geht. Fortfahren?",
      haSave: "Speichern",
      haClear: "Entfernen",
      haClearConfirm: "Adresse und Token von Home Assistant aus HERMES entfernen?",
      haSaved: "Home Assistant gespeichert.",
      haSavedNoToken: "Adresse gespeichert; das alte Token wurde gelöscht. Füge das Token für die neue Adresse ein.",
      haCleared: "Home Assistant entfernt.",
      haNothing: "Ändere zuerst die Adresse oder füge ein Token ein.",
      haTest: "Verbindung testen",
      haTesting: "Wird getestet …",
      haTestOk: "Home Assistant ist erreichbar.",
      haLanguage: "Sprache für Home Assistant",
      haLangAuto: "Wie die Antwort (Deutsch oder Englisch)",
      haLangDe: "Deutsch",
      haLangEn: "Englisch",
      haLangHint: "Die Sprache, in der HERMES Befehle an Home Assistant Assist weitergibt. Lass „wie die Antwort“, außer dein Assist versteht nur eine Sprache.",
      homeControl: "Wer darf Geräte schalten",
      hcEveryone: "Alle",
      hcKnown: "Nur erkannte Personen (Standard)",
      hcOff: "Niemand (keine Gerätebefehle)",
      homeQueries: "Wer darf nach Gerätezuständen fragen",
      hqKnown: "Nur erkannte Personen (Standard)",
      hqEveryone: "Alle",
      homeAccessHint: "„Erkannt“ heißt: Die Stimmerkennung kennt die sprechende Person. Schlösser, Türen, Tore und Alarmanlagen steuert HERMES nie, egal was hier steht.",
      homeOffHint: "Gerätesteuerung ist aus: HERMES schaltet nichts. Fragen zu Geräten („Ist das Küchenlicht an?“) richten sich weiter nach der Einstellung „Wer darf nach Gerätezuständen fragen“.",
      haSetupTitle: "Einrichtung",
      haSetup1: "Lege in Home Assistant einen eigenen Benutzer ohne Admin-Rechte für HERMES an.",
      haSetup2: "Melde dich als dieser Benutzer an, öffne das Profil → Sicherheit → Langlebige Zugriffstoken → Token erstellen und füge das Token hier ein.",
      haSetup3: "Gib unter Einstellungen → Sprachassistenten → Freigeben nur die Lampen, Schalter, Thermostate und Rollläden frei, die HERMES steuern soll.",
      errHaUrl: "Die Adresse muss so aussehen: http://192.168.1.20:8123 (ohne Pfad).",
      errHaToken: "Das Token sieht falsch aus (keine Leerzeichen, höchstens 2048 Zeichen).",
      errHaUrlFirst: "Gib zuerst die Home-Assistant-Adresse an.",
      errEnvOverride: "Die Adresse ist über HA_URL in der Umgebung festgelegt.",
      errLocalOnly: "Nur direkt am Mac (http://localhost:8080).",
      errLocked: "Gesperrt. Entsperre zuerst mit der Haushalts-PIN.",
      errNoClient: "Trage zuerst die Google-Client-ID und das Secret ein.",
      errUnavailable: "In dieser Server-Version nicht verfügbar.",
      errTooMany: "Zu viele Versuche. Warte eine Minute.",
      errJson: "Das ist keine client_secret-JSON-Datei von Google.",
      errJsonWeb: "Das ist ein Client vom Typ „Webanwendung“. Lege einen OAuth-Client vom Typ „Desktop-App“ an.",
      errJsonLarge: "Die Datei ist zu groß.",
      errIdFormat: "Die Client-ID sieht falsch aus (keine Leerzeichen, höchstens 512 Zeichen).",
      errSecretFormat: "Das Client-Secret sieht falsch aus (keine Leerzeichen, höchstens 512 Zeichen).",
      errBadClient: "Client-ID oder Client-Secret ist ungültig.",
      errTooLarge: "Das ist zu groß.",
      errConflict: "Das wurde gerade woanders geändert. Prüfe es und versuch es noch einmal.",
      errAuthUrl: "Der Server hat eine unerwartete Anmeldeadresse geschickt.",
      errReadOnly: "Die Datendatei stammt von einem neueren HERMES; Änderungen sind gesperrt.",
      errGeneric: "Das hat nicht geklappt: {msg}",
    },
  };

  // ------------------------------------------------------------ small utils

  function format(str, vars) {
    str = String(str == null ? "" : str);
    if (!vars) return str;
    return str.replace(/\{(\w+)\}/g, function (all, k) {
      return Object.prototype.hasOwnProperty.call(vars, k) && vars[k] != null ? String(vars[k]) : all;
    });
  }

  function makeTr(dict, langFn) {
    return function (key, vars) {
      var lang = (typeof langFn === "function" ? langFn() : langFn) === "de" ? "de" : "en";
      var t = dict[lang] || {};
      var s = Object.prototype.hasOwnProperty.call(t, key) ? t[key] : Object.prototype.hasOwnProperty.call(dict.en, key) ? dict.en[key] : key;
      return format(s, vars);
    };
  }

  /* A server string: string only, no control characters, capped in code points. */
  function cleanStr(v, max) {
    if (typeof v !== "string") return "";
    // eslint-disable-next-line no-control-regex
    var s = v.replace(/[\u0000-\u001f\u007f]/g, " ").trim();
    var cps = Array.from(s);
    return cps.length > max ? cps.slice(0, max).join("") : s;
  }

  // ------------------------------------------------------------ masking

  /*
   * Mask a credential for display: bullets plus, for long values, the last
   * `keep` characters (0..4) so the user can tell which one was saved.
   * Short values (< 12) are fully masked. "" stays "".
   */
  function maskSecret(v, keep) {
    var s = typeof v === "string" ? v.trim() : "";
    if (!s) return "";
    var cps = Array.from(s);
    var k = typeof keep === "number" && isFinite(keep) ? Math.max(0, Math.min(4, Math.floor(keep))) : 0;
    if (cps.length < 12) k = 0;
    return "••••••••" + (k ? cps.slice(cps.length - k).join("") : "");
  }

  /* How a credential is shown: "env" (from .env), "set" or "unset". */
  function credentialState(isSet, fromEnv) {
    if (fromEnv) return "env";
    return isSet ? "set" : "unset";
  }

  // ------------------------------------------------------------ server shapes

  /*
   * GET /api/integrations (SPEC 3.11 plus the secrets.go extras: per-field
   * *FromEnv, lock.enabled, admin). Booleans only; anything else, and every
   * field this page does not need (the HA part), is ignored, so a secret can
   * never end up in the page state.
   *   admin  this device may change the client, the PIN and the connection
   */
  function normalizeIntegrations(j) {
    var ok = !!(j && typeof j === "object" && !Array.isArray(j));
    var g = ok && j.google && typeof j.google === "object" ? j.google : {};
    var l = ok && j.lock && typeof j.lock === "object" ? j.lock : {};
    var anyEnv = g.fromEnv === true;
    // an older server sends only fromEnv: then both fields count as env
    var idEnv = typeof g.clientIdFromEnv === "boolean" ? g.clientIdFromEnv : anyEnv;
    var secretEnv = typeof g.clientSecretFromEnv === "boolean" ? g.clientSecretFromEnv : anyEnv;
    return {
      available: ok,
      // without the field (older server) assume yes: the server still refuses
      admin: ok ? j.admin !== false : false,
      google: {
        clientIdSet: g.clientIdSet === true,
        clientSecretSet: g.clientSecretSet === true,
        fromEnv: anyEnv || idEnv || secretEnv,
        clientIdFromEnv: idEnv,
        clientSecretFromEnv: secretEnv,
      },
      lock: { pinSet: l.pinSet === true, enabled: l.enabled === true },
    };
  }

  /* GET /api/calendar/status (SPEC 3.7). */
  function normalizeCalendarStatus(j) {
    if (!j || typeof j !== "object") return null;
    var owner = typeof j.owner === "string" && PERSON_ID.test(j.owner) ? j.owner : "";
    return {
      configured: j.configured === true,
      connected: j.connected === true,
      needsRelink: j.needsRelink === true,
      owner: owner,
      ownerName: cleanStr(j.ownerName, 40),
      error: cleanStr(j.error, 200),
    };
  }

  /*
   * One state for the calendar block:
   *   "unavailable"  no calendar API on the server
   *   "no_client"    no Google client configured
   *   "relink"       connected once, refresh token rejected
   *   "connected"    connected
   *   "error"        not connected, with an error text
   *   "disconnected" configured, not connected
   */
  function calendarState(st) {
    if (!st) return "unavailable";
    if (st.needsRelink) return "relink";
    if (st.connected) return "connected";
    if (!st.configured) return "no_client";
    if (st.error) return "error";
    return "disconnected";
  }

  // ------------------------------------------------------------ Google client

  /* 1..max visible ASCII characters, as the server checks it. */
  function credOk(s, max) {
    return typeof s === "string" && s.length > 0 && s.length <= max && /^[\x21-\x7e]+$/.test(s);
  }

  /*
   * Read a Google client_secret_*.json (Desktop app: {"installed":{…}}).
   * Returns {ok:true, clientId, clientSecret} or {ok:false, key}.
   * Only for a quick check in the browser; the file text itself goes to
   * the server as clientSecretJson.
   */
  function parseClientSecretJson(text) {
    if (typeof text !== "string") return { ok: false, key: "errJson" };
    if (text.length > JSON_MAX) return { ok: false, key: "errJsonLarge" };
    if (!text.trim()) return { ok: false, key: "errJson" };
    var j;
    try {
      j = JSON.parse(text);
    } catch (e) {
      return { ok: false, key: "errJson" };
    }
    if (!j || typeof j !== "object" || Array.isArray(j)) return { ok: false, key: "errJson" };
    var c = j.installed;
    if (!c || typeof c !== "object") return { ok: false, key: j.web && typeof j.web === "object" ? "errJsonWeb" : "errJson" };
    var id = typeof c.client_id === "string" ? c.client_id.trim() : "";
    var secret = typeof c.client_secret === "string" ? c.client_secret.trim() : "";
    if (!credOk(id, ID_MAX) || !credOk(secret, SECRET_MAX)) return { ok: false, key: "errJson" };
    return { ok: true, clientId: id, clientSecret: secret };
  }

  /*
   * Body for POST /api/integrations/google from the typed fields. Untouched
   * (empty) fields are left out so they keep their stored value; clearing is
   * a separate action ({clientId:"", clientSecret:""}).
   */
  function googleBody(clientId, clientSecret) {
    var id = typeof clientId === "string" ? clientId.trim() : "";
    var secret = typeof clientSecret === "string" ? clientSecret.trim() : "";
    if (!id && !secret) return { ok: false, key: "clientNothing" };
    var body = {};
    if (id) {
      if (!credOk(id, ID_MAX)) return { ok: false, key: "errIdFormat" };
      body.clientId = id;
    }
    if (secret) {
      if (!credOk(secret, SECRET_MAX)) return { ok: false, key: "errSecretFormat" };
      body.clientSecret = secret;
    }
    return { ok: true, body: body };
  }

  // ------------------------------------------------------------ PIN

  function validatePin(pin) {
    return typeof pin === "string" && /^[0-9]{4,12}$/.test(pin);
  }

  /*
   * The request for POST /api/integrations/lock.
   *   mode "set"    {pin, confirm}            (no PIN yet)
   *   mode "change" {oldPin, pin, confirm}
   *   mode "remove" {oldPin}                  → {pin:"", oldPin}
   * Returns {ok:true, body} or {ok:false, key, field}.
   */
  function pinRequest(mode, f) {
    f = f || {};
    var pin = typeof f.pin === "string" ? f.pin.trim() : "";
    var confirm = typeof f.confirm === "string" ? f.confirm.trim() : "";
    var oldPin = typeof f.oldPin === "string" ? f.oldPin.trim() : "";
    if (mode === "remove") {
      if (!oldPin) return { ok: false, key: "errPinOld", field: "old" };
      if (!validatePin(oldPin)) return { ok: false, key: "errPinWrong", field: "old" };
      return { ok: true, body: { pin: "", oldPin: oldPin } };
    }
    if (mode === "change") {
      if (!oldPin) return { ok: false, key: "errPinOld", field: "old" };
      if (!validatePin(oldPin)) return { ok: false, key: "errPinWrong", field: "old" };
    }
    if (!validatePin(pin)) return { ok: false, key: "errPinFormat", field: "pin" };
    if (pin !== confirm) return { ok: false, key: "errPinMismatch", field: "confirm" };
    if (mode === "change") {
      if (pin === oldPin) return { ok: false, key: "errPinSame", field: "pin" };
      return { ok: true, body: { pin: pin, oldPin: oldPin } };
    }
    return { ok: true, body: { pin: pin } };
  }

  // ------------------------------------------------------------ calendar connect

  /* Only ever navigate to Google's own sign-in host. */
  function safeAuthUrl(u) {
    if (typeof u !== "string" || u.length > 4096 || u.indexOf(AUTH_PREFIX) !== 0) return false;
    // no credentials, backslashes or whitespace tricks in the URL
    return !/[\s\\]/.test(u) && !/^https:\/\/accounts\.google\.com\/[^?#]*@/.test(u);
  }

  /*
   * Owner select options: household first, then people (valid ids only).
   * An owner that is not in the list (deleted person) stays selectable as
   * "unknown" so the select shows the truth.
   */
  function ownerOptions(people, owner, tr) {
    var out = [{ value: "", label: tr("ownerHousehold") }];
    var seen = {};
    (Array.isArray(people) ? people : []).forEach(function (p) {
      if (!p || typeof p.id !== "string" || !PERSON_ID.test(p.id) || seen[p.id]) return;
      var name = cleanStr(p.name, 40);
      if (!name) return;
      seen[p.id] = true;
      out.push({ value: p.id, label: name });
    });
    if (owner && PERSON_ID.test(owner) && !seen[owner]) out.push({ value: owner, label: tr("ownerUnknown") });
    return out;
  }

  /*
   * The OAuth callback redirects to /?calendar=connected or
   * /?calendar=error&reason=…. Returns {kind, reason, search} where
   * `search` is the query string without those two parameters ("" or "?…").
   */
  function calendarReturn(search) {
    var s = typeof search === "string" ? search.replace(/^\?/, "") : "";
    var kind = "";
    var reason = "";
    var keep = [];
    s.split("&").forEach(function (pair) {
      if (!pair) return;
      var i = pair.indexOf("=");
      var k = i >= 0 ? pair.slice(0, i) : pair;
      var v = i >= 0 ? pair.slice(i + 1) : "";
      var dv;
      try {
        dv = decodeURIComponent(v.replace(/\+/g, " "));
      } catch (e) {
        dv = "";
      }
      if (k === "calendar") {
        kind = dv === "connected" ? "connected" : dv === "error" ? "error" : "";
      } else if (k === "reason") {
        reason = /^[A-Za-z0-9_.-]{1,40}$/.test(dv) ? dv : "unknown";
      } else keep.push(pair);
    });
    if (kind === "error" && !reason) reason = "unknown";
    if (kind !== "error") reason = "";
    return { kind: kind, reason: reason, search: keep.length ? "?" + keep.join("&") : "" };
  }

  // ------------------------------------------------------------ errors and time

  /* Map a fetchJSON error to a STRINGS key. */
  var CODE_KEYS = {
    local_only: "errLocalOnly",
    locked: "errLocked",
    no_client: "errNoClient",
    pin: "errPinWrong",
    wrong_pin: "errPinWrong",
    bad_pin: "errPinFormat",
    old_pin_required: "errPinOld",
    lockout: "errTooMany",
    bad_client: "errBadClient",
    bad_json: "errJson",
    not_desktop: "errJsonWeb",
    too_large: "errTooLarge",
    conflict: "errConflict",
    read_only: "errReadOnly",
    bad_url: "errHaUrl",
    bad_token: "errHaToken",
    url_required: "errHaUrlFirst",
    env_override: "errEnvOverride",
  };

  function errorKey(err) {
    if (!err) return "errGeneric";
    var code = typeof err.code === "string" ? err.code : "";
    var st = err.status || 0;
    if (code && Object.prototype.hasOwnProperty.call(CODE_KEYS, code)) return CODE_KEYS[code];
    if (st === 429) return "errTooMany";
    if (st === 503 && /read-?only|newer/i.test(String(err.message || ""))) return "errReadOnly";
    if (st === 404 || st === 405) return "errUnavailable";
    if (st === 403 && /local|localhost|this machine/i.test(String(err.message || ""))) return "errLocalOnly";
    if (st === 403 && /unlock|pin/i.test(String(err.message || ""))) return "errLocked";
    return "errGeneric";
  }

  /*
   * "18:00" for a time later today, "Mo 07:00" / "Mon 07:00" otherwise,
   * in the settings time zone ("" = browser). "" for 0 / invalid.
   */
  function formatUntil(ms, now, lang, tz) {
    if (typeof ms !== "number" || !isFinite(ms) || ms <= 0) return "";
    var loc = lang === "de" ? "de-DE" : "en-GB";
    var base = { hour: "2-digit", minute: "2-digit", hourCycle: "h23" };
    var dayFmt = { year: "numeric", month: "2-digit", day: "2-digit" };
    function fmt(o, t) {
      var opts = Object.assign({}, o);
      if (tz) opts.timeZone = tz;
      try {
        return new Intl.DateTimeFormat(loc, opts).format(new Date(t));
      } catch (e) {
        delete opts.timeZone;
        return new Intl.DateTimeFormat(loc, opts).format(new Date(t));
      }
    }
    var time = fmt(base, ms);
    if (fmt(dayFmt, ms) === fmt(dayFmt, now)) return time;
    return fmt({ weekday: "short" }, ms) + " " + time;
  }

  /* The STRINGS key for a callback reason ("denied" → "reason_denied"). */
  function reasonKey(reason) {
    var k = "reason_" + String(reason || "unknown");
    return Object.prototype.hasOwnProperty.call(STRINGS.en, k) ? k : "reason_unknown";
  }

  /*
   * The toast for a {kind:"calendar", data:{added:true, events:[ev]}} card:
   * {title, when} with when as "Tue 10 Nov, 15:00" / "Di., 10. Nov., 15:00"
   * in the event's own offset (the server sends local RFC 3339 times), or
   * null when the card is not an add or is malformed.
   */
  function calendarAdded(card, lang) {
    if (!card || card.kind !== "calendar" || !card.data || typeof card.data !== "object" || card.data.added !== true) return null;
    var evs = Array.isArray(card.data.events) ? card.data.events : [];
    var ev = evs[0];
    if (!ev || typeof ev !== "object") return null;
    var title = cleanStr(ev.title, 80);
    var m = typeof ev.start === "string" ? /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})/.exec(ev.start) : null;
    if (!title || !m) return null;
    var de = lang === "de";
    var d = new Date(Date.UTC(+m[1], +m[2] - 1, +m[3]));
    var wd = (de ? ["So", "Mo", "Di", "Mi", "Do", "Fr", "Sa"] : ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"])[d.getUTCDay()];
    var day = de
      ? wd + "., " + +m[3] + "." + +m[2] + "."
      : wd + " " + ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"][+m[2] - 1] + " " + +m[3];
    var when = ev.allDay === true ? day + (de ? " (ganztägig)" : " (all day)") : day + ", " + m[4] + ":" + m[5];
    return { title: title, when: when };
  }

  // ------------------------------------------------------------ smart home

  // secrets.go: haURLMaxLen 512, haTokenMaxLen 2048
  var HA_URL_MAX = 512;
  var HA_TOKEN_MAX = 2048;
  var HA_URL_RE = /^(https?):\/\/(\[[0-9A-Fa-f:.]+\]|[^\s\/?#@\\%:\[\]]+)(?::(\d{1,5}))?\/?$/i;
  var HOME_CODES = { unreachable: 1, auth: 1, timeout: 1, bad_response: 1 };
  var HA_LANG_RE = /^[a-z]{2,3}(-[A-Za-z]{2,4})?$/;
  var ENTITIES_MAX = 300;

  /*
   * The browser-side check of a Home Assistant address, as secrets.go
   * normalizeHAURL does it: http(s)://host[:port] with an optional trailing
   * slash, no user info, path, query or fragment. Returns the normalised
   * form ("http://192.168.1.20:8123") or "" when invalid. The server checks
   * again; this only gives a quick message and avoids a pointless request.
   */
  function normalizeHaUrl(raw) {
    var s = typeof raw === "string" ? raw.trim() : "";
    if (!s || s.length > HA_URL_MAX) return "";
    var m = HA_URL_RE.exec(s);
    if (!m) return "";
    var port = m[3] || "";
    if (port) {
      var n = parseInt(port, 10);
      if (!(n >= 1 && n <= 65535) || String(n) !== port) return "";
    }
    return m[1].toLowerCase() + "://" + m[2].toLowerCase() + (port ? ":" + port : "");
  }

  /*
   * The HA part of GET /api/integrations (secrets.go integrationsHAView).
   * The token is never sent by the server; only `tokenSet` is read. The URL
   * is shown in the form, so it must pass normalizeHaUrl (an env value with
   * credentials or a path is already stripped by the server; anything else
   * is dropped here).
   */
  function normalizeHA(j) {
    var h = j && typeof j === "object" && !Array.isArray(j) && j.ha && typeof j.ha === "object" ? j.ha : {};
    var url = normalizeHaUrl(h.url);
    var anyEnv = h.fromEnv === true;
    var urlEnv = typeof h.urlFromEnv === "boolean" ? h.urlFromEnv : anyEnv;
    var tokenEnv = typeof h.tokenFromEnv === "boolean" ? h.tokenFromEnv : anyEnv;
    return {
      url: url,
      // false: the environment holds an address the server cannot use
      urlValid: h.urlValid !== false && !(typeof h.url === "string" && h.url && !url),
      tokenSet: h.tokenSet === true,
      fromEnv: anyEnv || urlEnv || tokenEnv,
      urlFromEnv: urlEnv,
      tokenFromEnv: tokenEnv,
    };
  }

  /*
   * The request for POST /api/integrations/ha from the form.
   *   form {url, token}   what the user typed (url is prefilled with the stored one)
   *   cur  normalizeHA()  what is stored
   * Returns {ok:false, key, field} or {ok:true, action:"clear"|"save", body,
   * clearsToken}: `clearsToken` is true when the URL changes without a new
   * token, so the server deletes the stored one (the UI asks first).
   */
  function haRequest(form, cur) {
    form = form || {};
    cur = cur || normalizeHA(null);
    var urlIn = typeof form.url === "string" ? form.url.trim() : "";
    var tokenIn = typeof form.token === "string" ? form.token.trim() : "";
    var body = {};
    if (!cur.urlFromEnv) {
      if (!urlIn) {
        if (cur.url || (cur.tokenSet && !cur.tokenFromEnv)) {
          if (tokenIn) return { ok: false, key: "errHaUrlFirst", field: "url" };
          return { ok: true, action: "clear", body: { url: "" }, clearsToken: cur.tokenSet && !cur.tokenFromEnv };
        }
      } else {
        var n = normalizeHaUrl(urlIn);
        if (!n) return { ok: false, key: "errHaUrl", field: "url" };
        if (n !== cur.url) body.url = n;
      }
    }
    if (tokenIn && !cur.tokenFromEnv) {
      if (!credOk(tokenIn, HA_TOKEN_MAX)) return { ok: false, key: "errHaToken", field: "token" };
      body.token = tokenIn;
    }
    if (!body.url && !body.token) return { ok: false, key: "haNothing", field: "token" };
    if (body.token && !body.url && !cur.url) return { ok: false, key: "errHaUrlFirst", field: "url" };
    return { ok: true, action: "save", body: body, clearsToken: !!body.url && !body.token && cur.tokenSet && !cur.tokenFromEnv };
  }

  /*
   * GET /api/home/status and POST /api/home/test. SPEC 3.8 sends
   * {configured, ok, version, code, entities}; the batch-3 contract names
   * {configured, connected, version?, entities?, error?}. Both are read.
   * Only generic codes are expected; any other error text is cleaned and
   * capped (it is set with textContent only).
   */
  function normalizeHomeStatus(j) {
    if (!j || typeof j !== "object" || Array.isArray(j)) return null;
    var code = typeof j.code === "string" && HOME_CODES[j.code] ? j.code : "";
    var err = "";
    if (typeof j.error === "string" && j.error) {
      if (!code && HOME_CODES[j.error]) code = j.error;
      else if (!HOME_CODES[j.error]) err = cleanStr(j.error, 160);
    }
    if (code === "ok") code = "";
    var version = cleanStr(j.version, 40);
    if (!/^[0-9A-Za-z][0-9A-Za-z.\-+ ]*$/.test(version)) version = "";
    var ent = typeof j.entities === "number" && isFinite(j.entities) && j.entities >= 0 ? Math.min(Math.floor(j.entities), 100000) : -1;
    var connected = j.connected === true || j.ok === true;
    return {
      configured: j.configured === true || connected,
      connected: connected,
      version: version,
      entities: ent,
      code: connected ? "" : code,
      error: connected ? "" : err,
    };
  }

  /*
   * One state for the smart home status line:
   *   "unavailable"  no /api/home on the server (`missing`) or nothing known
   *   "unconfigured" no address
   *   "no_token"     address but no token
   *   "connected"
   *   "unreachable" | "auth" | "timeout" | "bad_response"  (status codes)
   *   "error"        configured, not connected, no known code
   */
  function homeState(st, ha, missing) {
    if (missing) return "unavailable";
    var hasUrl = !!(ha && ha.url);
    var hasToken = !!(ha && ha.tokenSet);
    // the stored credentials beat a status the server may still cache (60 s)
    if (ha && !hasUrl && !hasToken) return "unconfigured";
    if (ha && hasUrl && !hasToken) return "no_token";
    if (st && st.connected) return "connected";
    if (!st) return ha ? "error" : "unavailable";
    if (!st.configured) return "unconfigured";
    if (st.code) return st.code;
    return "error";
  }

  /* The STRINGS key and vars for a home state. */
  function homeStateText(state, st) {
    switch (state) {
      case "connected":
        return st && st.version ? { key: "haConnectedVersion", vars: { version: st.version } } : { key: "haConnected" };
      case "unconfigured":
        return { key: "haUnconfigured" };
      case "no_token":
        return { key: "haNoToken" };
      case "unreachable":
        return { key: "haUnreachable" };
      case "auth":
        return { key: "haAuth" };
      case "timeout":
        return { key: "haTimeout" };
      case "bad_response":
        return { key: "haBadResponse" };
      case "unavailable":
        return { key: "haUnavailable" };
      default:
        return st && st.error ? { key: "haError", vars: { msg: st.error } } : { key: "haErrorPlain" };
    }
  }

  /*
   * GET /api/home/entities → {count, blocked, items:[{name, area, domain,
   * blocked}]} sorted blocked-last then by name; at most 300 items, names
   * and areas cleaned and capped. null for junk.
   */
  function normalizeHomeEntities(j) {
    if (!j || typeof j !== "object" || !Array.isArray(j.entities)) return null;
    var items = [];
    var blocked = 0;
    var count = 0;
    j.entities.forEach(function (e) {
      if (!e || typeof e !== "object") return;
      var id = cleanStr(e.id, 120);
      var name = cleanStr(e.name, 80) || id;
      if (!name) return;
      var isBlocked = e.blocked === true;
      count++;
      if (isBlocked) blocked++;
      if (items.length < ENTITIES_MAX) {
        var dom = cleanStr(e.domain, 30);
        items.push({ name: name, area: cleanStr(e.area, 60), domain: /^[a-z_]+$/.test(dom) ? dom : "", blocked: isBlocked });
      }
    });
    items.sort(function (a, b) {
      if (a.blocked !== b.blocked) return a.blocked ? 1 : -1;
      return a.name.localeCompare(b.name);
    });
    return { count: count, blocked: blocked, items: items };
  }

  /* Options for the haLanguage select; a stored other code stays selectable. */
  function haLanguageOptions(current, tr) {
    var out = [
      { value: "", label: tr("haLangAuto") },
      { value: "de", label: tr("haLangDe") },
      { value: "en", label: tr("haLangEn") },
    ];
    var c = typeof current === "string" ? current.trim() : "";
    if (c && c !== "de" && c !== "en" && HA_LANG_RE.test(c)) out.push({ value: c, label: c });
    return out;
  }

  return {
    STRINGS: STRINGS,
    normalizeHaUrl: normalizeHaUrl,
    normalizeHA: normalizeHA,
    haRequest: haRequest,
    normalizeHomeStatus: normalizeHomeStatus,
    homeState: homeState,
    homeStateText: homeStateText,
    normalizeHomeEntities: normalizeHomeEntities,
    haLanguageOptions: haLanguageOptions,
    reasonKey: reasonKey,
    calendarAdded: calendarAdded,
    JSON_MAX: JSON_MAX,
    format: format,
    makeTr: makeTr,
    cleanStr: cleanStr,
    maskSecret: maskSecret,
    credentialState: credentialState,
    normalizeIntegrations: normalizeIntegrations,
    normalizeCalendarStatus: normalizeCalendarStatus,
    calendarState: calendarState,
    parseClientSecretJson: parseClientSecretJson,
    googleBody: googleBody,
    validatePin: validatePin,
    pinRequest: pinRequest,
    safeAuthUrl: safeAuthUrl,
    ownerOptions: ownerOptions,
    calendarReturn: calendarReturn,
    errorKey: errorKey,
    formatUntil: formatUntil,
  };
});

/* ====================================================================== */
/* Browser UI                                                              */
/* ====================================================================== */
(function () {
  "use strict";
  if (typeof window === "undefined" || typeof document === "undefined") return;
  var H = window.Hermes;
  var I = window.HermesIntegrations;
  if (!H || !I) return;

  function api() {
    return H.api || {};
  }
  function lang() {
    try {
      return api().lang && api().lang() === "de" ? "de" : "en";
    } catch (e) {
      return "en";
    }
  }
  var tr = typeof H.strings === "function" ? H.strings(I.STRINGS) : I.makeTr(I.STRINGS, lang);

  function settings() {
    try {
      return (api().settings && api().settings()) || {};
    } catch (e) {
      return {};
    }
  }
  function toast(msg, o) {
    try {
      if (typeof api().toast === "function") return api().toast(msg, o || {});
    } catch (e) {
      /* ignore */
    }
    return null;
  }
  function fetchJSON(url, o) {
    if (typeof api().fetchJSON === "function") return api().fetchJSON(url, o);
    return Promise.reject(Object.assign(new Error("not ready"), { status: 0 }));
  }
  function errText(err) {
    var key = I.errorKey(err);
    if (key !== "errGeneric") return tr(key);
    // secrets.go adds a DE/EN `hint` (by the voice language); app.js
    // fetchJSON copies it onto the error as err.hint.
    var hint = err && typeof err.hint === "string" ? I.cleanStr(err.hint, 300) : "";
    return hint || tr(key, { msg: I.cleanStr((err && err.message) || "?", 200) || "?" });
  }
  function isAdmin() {
    return !!(shared.integ && shared.integ.admin);
  }
  function checkins() {
    var C = window.HermesCheckins;
    return C && C.ui ? C.ui : null;
  }

  // ---------------------------------------------------------------- DOM helpers

  function el(tag, attrs, kids) {
    var e = document.createElement(tag);
    if (attrs) {
      for (var k in attrs) {
        if (!Object.prototype.hasOwnProperty.call(attrs, k)) continue;
        var v = attrs[k];
        if (v == null || v === false) continue;
        if (k === "class") e.className = v;
        else if (k === "text") e.textContent = v;
        else if (k.slice(0, 2) === "on" && typeof v === "function") e.addEventListener(k.slice(2), v);
        else if (k === "hidden") e.hidden = !!v;
        else if (k === "disabled") e.disabled = !!v;
        else if (k === "checked") e.checked = !!v;
        else if (k === "value") e.value = v;
        else e.setAttribute(k, v === true ? "" : v);
      }
    }
    if (kids != null) {
      (Array.isArray(kids) ? kids : [kids]).forEach(function (c) {
        if (c == null || c === false) return;
        e.appendChild(typeof c === "string" ? document.createTextNode(c) : c);
      });
    }
    return e;
  }
  function clear(node) {
    while (node && node.firstChild) node.removeChild(node.firstChild);
  }
  function row(labelText, control, hint) {
    var nodes = [el("label", null, [el("span", { text: labelText }), control])];
    if (hint) nodes.push(el("p", { class: "hint", text: hint }));
    return nodes;
  }
  function append(root, nodes) {
    (Array.isArray(nodes) ? nodes : [nodes]).forEach(function (n) {
      if (n) root.appendChild(n);
    });
  }
  function setMsg(node, text, kind) {
    if (!node) return;
    node.textContent = text || "";
    node.hidden = !text;
    node.className = "int-msg" + (kind ? " " + kind : "");
  }
  function busy(btns, on) {
    btns.forEach(function (b) {
      if (b) b.disabled = !!on;
    });
  }
  /* Values of data-setting controls follow the live settings (also after a re-render). */
  function fillSettings(root) {
    var s = settings();
    Array.prototype.forEach.call(root.querySelectorAll("[data-setting]"), function (c) {
      var key = c.getAttribute("data-setting");
      var v = s[key];
      if (c.type === "checkbox") c.checked = !!v;
      else c.value = v == null ? "" : String(v);
    });
  }
  /* Tell app.js a bound control changed (it reads the form on "change"). */
  function notifyForm(control) {
    try {
      control.dispatchEvent(new Event("change", { bubbles: true }));
    } catch (e) {
      /* very old browser: the next edit picks it up */
    }
  }

  // ---------------------------------------------------------------- shared server state

  var shared = {
    integ: null, // normalizeIntegrations() or null when unknown
    integLoaded: false,
    cal: null, // normalizeCalendarStatus() or null
    calLoaded: false,
    unlock: { pinSet: false, unlocked: false, locked: false },
    people: [],
    checkState: { busyUntil: 0, pausedUntil: 0 },
    ha: null, // normalizeHA() or null when /api/integrations failed
    home: null, // normalizeHomeStatus() or null
    homeLoaded: false,
    homeMissing: false, // /api/home/* is not on this server
    homeEntities: null, // normalizeHomeEntities() or null
    at: 0,
  };
  var views = {}; // id → {render, update}

  function updateAll() {
    for (var k in views) if (Object.prototype.hasOwnProperty.call(views, k) && views[k].update) views[k].update();
  }

  var loading = null;
  function loadAll(force) {
    if (loading) return loading;
    if (!force && Date.now() - shared.at < 15000) return Promise.resolve();
    var jobs = [
      fetchJSON("/api/integrations")
        .then(function (j) {
          shared.integ = I.normalizeIntegrations(j);
          shared.ha = I.normalizeHA(j);
        })
        .catch(function () {
          shared.integ = null;
          shared.ha = null;
        })
        .then(function () {
          shared.integLoaded = true;
        }),
      fetchJSON("/api/calendar/status")
        .then(function (j) {
          shared.cal = I.normalizeCalendarStatus(j);
        })
        .catch(function () {
          shared.cal = null;
        })
        .then(function () {
          shared.calLoaded = true;
        }),
      fetchJSON("/api/unlock")
        .then(function (j) {
          shared.unlock = { pinSet: !!(j && j.pinSet), unlocked: !!(j && j.unlocked), locked: !!(j && j.locked) };
        })
        .catch(function () {
          /* keep the last value */
        }),
      fetchJSON("/api/people")
        .then(function (j) {
          shared.people = j && Array.isArray(j.people) ? j.people.slice(0, 50) : [];
        })
        .catch(function () {
          shared.people = [];
        }),
      loadHome(),
      fetchJSON("/api/checkins/state")
        .then(function (j) {
          var C = window.HermesCheckins;
          shared.checkState = C ? C.normalizeState(j, Date.now()) : { busyUntil: 0, pausedUntil: 0 };
        })
        .catch(function () {
          shared.checkState = { busyUntil: 0, pausedUntil: 0 };
        }),
    ];
    loading = Promise.all(jobs).then(function () {
      loading = null;
      shared.at = Date.now();
      updateAll();
    });
    return loading;
  }
  /* GET /api/home/status (cached by the server), then the entity list when connected. */
  function loadHome() {
    return fetchJSON("/api/home/status")
      .then(function (j) {
        shared.homeMissing = false;
        shared.home = I.normalizeHomeStatus(j);
      })
      .catch(function (err) {
        var st = (err && err.status) || 0;
        shared.homeMissing = st === 404 || st === 405;
        shared.home = null;
      })
      .then(function () {
        shared.homeLoaded = true;
        return loadHomeEntities();
      });
  }
  function loadHomeEntities() {
    if (!shared.home || !shared.home.connected || !views.home) {
      shared.homeEntities = null;
      return Promise.resolve();
    }
    return fetchJSON("/api/home/entities")
      .then(function (j) {
        shared.homeEntities = I.normalizeHomeEntities(j);
      })
      .catch(function () {
        // not built, or locked (personalLock + homeQueries=known): just no list
        shared.homeEntities = null;
      });
  }
  function pinSet() {
    return shared.integ ? shared.integ.lock.pinSet || shared.unlock.pinSet : shared.unlock.pinSet;
  }

  // ---------------------------------------------------------------- calendar section

  function mountCalendar(root) {
    root.classList.add("int", "int-calendar");
    var v = { root: root, lang: "" };
    views.calendar = v;

    v.render = function () {
      v.lang = lang();
      clear(root);
      v.status = el("p", { class: "int-status", role: "status", text: tr("calLoading") });
      root.appendChild(v.status);

      // Google client
      v.adminHint = el("p", { class: "hint warn", text: tr("adminOnly"), hidden: true });
      root.appendChild(v.adminHint);
      v.idIn = el("input", { type: "text", maxlength: "512", autocomplete: "off", spellcheck: "false", "aria-describedby": "int_cred_hint" });
      v.secretIn = el("input", { type: "password", maxlength: "512", autocomplete: "new-password", spellcheck: "false" });
      v.idState = el("span", { class: "int-cred" });
      v.secretState = el("span", { class: "int-cred" });
      root.appendChild(el("label", null, [el("span", null, [tr("clientId") + " ", v.idState]), v.idIn]));
      root.appendChild(el("label", null, [el("span", null, [tr("clientSecret") + " ", v.secretState]), v.secretIn]));
      v.envHint = el("p", { class: "hint", id: "int_cred_hint", text: tr("credEnvHint"), hidden: true });
      root.appendChild(v.envHint);
      v.file = el("input", { type: "file", accept: ".json,application/json", class: "int-file", "aria-label": tr("jsonUpload") });
      v.fileBtn = el("button", { type: "button", class: "small ghost", text: tr("jsonUpload") });
      v.fileBtn.addEventListener("click", function () {
        v.file.value = "";
        v.file.click();
      });
      v.file.addEventListener("change", function () {
        var f = v.file.files && v.file.files[0];
        if (f) uploadJson(f);
      });
      v.saveBtn = el("button", { type: "button", class: "small", text: tr("saveClient"), onclick: saveClient });
      v.clearBtn = el("button", { type: "button", class: "small ghost danger", text: tr("clearClient"), onclick: clearClient });
      root.appendChild(el("div", { class: "int-actions" }, [v.saveBtn, v.fileBtn, v.clearBtn, v.file]));
      v.clientMsg = el("p", { class: "int-msg", role: "alert", hidden: true });
      root.appendChild(v.clientMsg);
      root.appendChild(el("p", { class: "hint", text: tr("setupHint") }));

      // owner + others
      v.owner = el("select", { "aria-describedby": "int_owner_hint" });
      v.ownerHint = el("p", { class: "hint", id: "int_owner_hint", text: tr("ownerChangeHint"), hidden: true });
      append(root, row(tr("owner"), v.owner));
      root.appendChild(v.ownerHint);
      var others = el("select", { "data-setting": "calendarOthers" }, [el("option", { value: "third", text: tr("othersThird") }), el("option", { value: "refuse", text: tr("othersRefuse") })]);
      append(root, row(tr("others"), others));

      // connect / disconnect
      v.connectBtn = el("button", { type: "button", class: "small primary", text: tr("connect"), onclick: connect });
      v.disconnectBtn = el("button", { type: "button", class: "small ghost danger", text: tr("disconnect"), onclick: disconnect });
      root.appendChild(el("div", { class: "int-actions" }, [v.connectBtn, v.disconnectBtn]));
      v.calMsg = el("p", { class: "int-msg", role: "alert", hidden: true });
      root.appendChild(v.calMsg);
      fillSettings(root);
      v.update();
    };

    v.update = function () {
      if (v.lang !== lang()) return v.render();
      var integ = shared.integ;
      var g = integ ? integ.google : { clientIdSet: false, clientSecretSet: false, fromEnv: false, clientIdFromEnv: false, clientSecretFromEnv: false };
      var integOk = !!integ;
      var admin = isAdmin();
      var idSt = I.credentialState(g.clientIdSet, g.clientIdFromEnv);
      var secSt = I.credentialState(g.clientSecretSet, g.clientSecretFromEnv);
      v.idState.textContent = shared.integLoaded ? "(" + tr(idSt === "env" ? "credEnv" : idSt === "set" ? "credSet" : "credUnset") + ")" : "";
      v.secretState.textContent = shared.integLoaded ? "(" + tr(secSt === "env" ? "credEnv" : secSt === "set" ? "credSet" : "credUnset") + ")" : "";
      v.idIn.placeholder = idSt === "set" ? tr("idPlaceholderSet") : tr("clientIdPlaceholder");
      v.secretIn.placeholder = secSt === "set" ? tr("secretPlaceholderSet") : "";
      // each env variable fixes only its own field (secrets.go precedence)
      var noEdit = !integOk || !admin;
      v.idIn.disabled = noEdit || g.clientIdFromEnv;
      v.secretIn.disabled = noEdit || g.clientSecretFromEnv;
      v.saveBtn.disabled = noEdit || (g.clientIdFromEnv && g.clientSecretFromEnv);
      v.fileBtn.disabled = noEdit || g.clientIdFromEnv || g.clientSecretFromEnv;
      v.clearBtn.hidden = noEdit || g.fromEnv || !(g.clientIdSet || g.clientSecretSet);
      v.envHint.hidden = !g.fromEnv;
      v.adminHint.hidden = !(integOk && !admin);
      if (shared.integLoaded && !integOk) setMsg(v.clientMsg, tr("errUnavailable"), "warn");

      // status line
      var st = I.calendarState(shared.cal);
      var text;
      if (!shared.calLoaded) text = tr("calLoading");
      else if (st === "connected") text = shared.cal.ownerName ? tr("calConnectedOwner", { name: shared.cal.ownerName }) : tr("calConnected");
      else if (st === "relink") text = tr("calRelink");
      else if (st === "no_client") text = tr("calNoClient");
      else if (st === "error") text = tr("calError", { msg: shared.cal.error });
      else if (st === "unavailable") text = tr("calUnavailable");
      else text = tr("calDisconnected");
      v.status.textContent = text;
      v.status.setAttribute("data-state", shared.calLoaded ? st : "loading");

      // owner select: the stored owner while connected, the choice for the next connect otherwise
      var cur = v.owner.value;
      var owner = shared.cal && (st === "connected" || st === "relink") ? shared.cal.owner : cur || (shared.cal ? shared.cal.owner : "");
      clear(v.owner);
      I.ownerOptions(shared.people, owner, tr).forEach(function (o) {
        v.owner.appendChild(el("option", { value: o.value, text: o.label }));
      });
      v.owner.value = owner;
      var connected = st === "connected";
      v.owner.disabled = connected || st === "unavailable";
      v.ownerHint.hidden = !connected;
      v.connectBtn.textContent = st === "relink" ? tr("reconnect") : tr("connect");
      v.connectBtn.hidden = connected;
      v.connectBtn.disabled = st === "unavailable" || st === "no_client" || (integOk && !admin);
      v.disconnectBtn.hidden = !(connected || st === "relink");
      v.disconnectBtn.disabled = integOk && !admin;
    };

    function saveClient() {
      var b = I.googleBody(v.idIn.value, v.secretIn.value);
      if (!b.ok) {
        setMsg(v.clientMsg, tr(b.key), "warn");
        return;
      }
      postGoogle(b.body, b.body.clientId ? tr("clientSavedMasked", { id: I.maskSecret(b.body.clientId, 4) }) : tr("clientSaved"));
    }

    function postGoogle(body, okText) {
      setMsg(v.clientMsg, "");
      busy([v.saveBtn, v.fileBtn, v.clearBtn], true);
      return fetchJSON("/api/integrations/google", { method: "POST", body: body })
        .then(function () {
          v.idIn.value = "";
          v.secretIn.value = "";
          toast(okText, { type: "success", key: "int-google" });
        })
        .catch(function (err) {
          setMsg(v.clientMsg, errText(err), "warn");
        })
        .then(function () {
          busy([v.saveBtn, v.fileBtn, v.clearBtn], false);
          return loadAll(true);
        });
    }

    function uploadJson(file) {
      if (file.size > I.JSON_MAX) {
        setMsg(v.clientMsg, tr("errJsonLarge"), "warn");
        return;
      }
      var read = typeof file.text === "function" ? file.text() : Promise.reject(new Error("no File.text"));
      read
        .then(function (text) {
          var p = I.parseClientSecretJson(text);
          if (!p.ok) {
            setMsg(v.clientMsg, tr(p.key), "warn");
            return;
          }
          postGoogle({ clientSecretJson: text }, tr("clientSavedMasked", { id: I.maskSecret(p.clientId, 4) }));
        })
        .catch(function () {
          setMsg(v.clientMsg, tr("errJson"), "warn");
        });
    }

    function clearClient() {
      if (!window.confirm(tr("clearClientConfirm"))) return;
      postGoogle({ clientId: "", clientSecret: "" }, tr("clientCleared"));
    }

    function connect() {
      setMsg(v.calMsg, "");
      v.connectBtn.disabled = true;
      var owner = v.owner.value;
      fetchJSON("/api/calendar/connect", { method: "POST", body: { owner: owner } })
        .then(function (r) {
          var url = r && r.authUrl;
          if (!I.safeAuthUrl(url)) throw Object.assign(new Error("auth url"), { key: "errAuthUrl" });
          toast(tr("connecting"), { key: "int-cal" });
          window.location.assign(url);
        })
        .catch(function (err) {
          setMsg(v.calMsg, err && err.key ? tr(err.key) : errText(err), "warn");
          v.connectBtn.disabled = false;
        });
    }

    function disconnect() {
      if (!window.confirm(tr("disconnectConfirm"))) return;
      setMsg(v.calMsg, "");
      v.disconnectBtn.disabled = true;
      fetchJSON("/api/calendar/disconnect", { method: "POST", body: {} })
        .then(function () {
          toast(tr("disconnected"), { type: "success", key: "int-cal" });
        })
        .catch(function (err) {
          setMsg(v.calMsg, errText(err), "warn");
        })
        .then(function () {
          v.disconnectBtn.disabled = false;
          return loadAll(true);
        });
    }

    v.render();
    loadAll(false);
  }

  // ---------------------------------------------------------------- privacy section

  function mountPrivacy(root) {
    root.classList.add("int", "int-privacy");
    var v = { root: root, lang: "", mode: "" };
    views.privacy = v;

    v.render = function () {
      v.lang = lang();
      v.mode = pinSet() ? "change" : "set";
      clear(root);
      v.pinStatus = el("p", { class: "int-status", role: "status" });
      root.appendChild(el("div", { class: "int-label", text: tr("pinStatus") }));
      root.appendChild(v.pinStatus);
      v.adminHint = el("p", { class: "hint warn", text: tr("adminOnly"), hidden: true });
      root.appendChild(v.adminHint);

      function pinInput(id, ac) {
        return el("input", { type: "password", inputmode: "numeric", autocomplete: ac, maxlength: "12", pattern: "[0-9]*", id: id, spellcheck: "false" });
      }
      v.oldIn = pinInput("int_pin_old", "current-password");
      v.newIn = pinInput("int_pin_new", "new-password");
      v.confIn = pinInput("int_pin_conf", "new-password");
      v.oldRow = el("label", { for: "int_pin_old" }, [el("span", { text: tr("pinOld") }), v.oldIn]);
      var form = el("form", { class: "int-pin", novalidate: true }, [
        v.oldRow,
        el("div", { class: "row" }, [
          el("label", { for: "int_pin_new" }, [el("span", { text: tr("pinNew") }), v.newIn]),
          el("label", { for: "int_pin_conf" }, [el("span", { text: tr("pinConfirm") }), v.confIn]),
        ]),
      ]);
      v.pinBtn = el("button", { type: "submit", class: "small" });
      v.removeBtn = el("button", { type: "button", class: "small ghost danger", text: tr("pinRemove"), onclick: removePin });
      form.appendChild(el("div", { class: "int-actions" }, [v.pinBtn, v.removeBtn]));
      v.pinMsg = el("p", { class: "int-msg", role: "alert", hidden: true });
      form.appendChild(v.pinMsg);
      form.addEventListener("submit", function (e) {
        e.preventDefault();
        submitPin();
      });
      root.appendChild(form);

      // personal lock: saved at once (admin + unlock), not with the Save button
      v.lockBox = el("input", { type: "checkbox", id: "int_lock" });
      v.lockBox.addEventListener("change", function (e) {
        e.stopPropagation(); // not a data-setting; keep app.js's dirty state out of it
        toggleLock(v.lockBox.checked);
      });
      root.appendChild(el("label", { class: "check", for: "int_lock" }, [v.lockBox, " " + tr("lock")]));
      root.appendChild(el("p", { class: "hint", text: tr("lockHint") }));
      v.lockMsg = el("p", { class: "int-msg", role: "alert", hidden: true });
      root.appendChild(v.lockMsg);
      v.unlockLine = el("p", { class: "int-status" });
      v.lockNowBtn = el("button", { type: "button", class: "small ghost", text: tr("lockNow"), onclick: lockNow });
      root.appendChild(el("div", { class: "int-inline" }, [v.unlockLine, v.lockNowBtn]));

      // history on speaker change (normal setting)
      root.appendChild(el("label", { class: "check" }, [el("input", { type: "checkbox", "data-setting": "keepHistoryOnSpeakerChange" }), " " + tr("keepHistory")]));
      root.appendChild(el("p", { class: "hint", text: tr("keepHistoryHint") }));
      fillSettings(root);
      v.update();
    };

    v.update = function () {
      if (v.lang !== lang()) return v.render();
      var has = pinSet();
      var mode = has ? "change" : "set";
      if (mode !== v.mode) {
        v.mode = mode;
        v.oldIn.value = "";
        v.newIn.value = "";
        v.confIn.value = "";
      }
      v.pinStatus.textContent = has ? tr("pinSet") : tr("pinNone");
      var noEdit = !!shared.integ && !isAdmin();
      v.adminHint.hidden = !noEdit;
      [v.oldIn, v.newIn, v.confIn, v.pinBtn, v.removeBtn].forEach(function (n) {
        n.disabled = noEdit;
      });
      v.oldRow.hidden = !has;
      v.pinBtn.textContent = has ? tr("pinChange") : tr("pinSave");
      v.removeBtn.hidden = !has;
      v.lockBox.checked = !!settings().personalLock;
      // switching the lock is an admin action too (settings.go)
      v.lockBox.disabled = noEdit || (!has && !settings().personalLock);
      var unlocked = shared.unlock.unlocked && has;
      v.unlockLine.textContent = unlocked ? tr("unlockedHere") : !has ? tr("lockNeedsPin") : "";
      v.unlockLine.hidden = !v.unlockLine.textContent;
      v.lockNowBtn.hidden = !unlocked;
    };

    function clearPins() {
      v.oldIn.value = "";
      v.newIn.value = "";
      v.confIn.value = "";
    }
    function focusField(f) {
      var n = f === "old" ? v.oldIn : f === "confirm" ? v.confIn : v.newIn;
      try {
        n.focus();
      } catch (e) {
        /* ignore */
      }
    }

    function sendPin(req, okKey) {
      setMsg(v.pinMsg, "");
      busy([v.pinBtn, v.removeBtn], true);
      return fetchJSON("/api/integrations/lock", { method: "POST", body: req.body })
        .then(function () {
          clearPins();
          toast(tr(okKey), { type: "success", key: "int-pin" });
          if (okKey === "pinRemoved" && settings().personalLock && typeof api().saveSettings === "function") {
            // the server switched the lock off with the PIN; sync the app's copy
            return api()
              .saveSettings({ personalLock: false })
              .catch(function () {});
          }
          return null;
        })
        .catch(function (err) {
          var key = I.errorKey(err);
          // a wrong old PIN comes back as 400/403 without a known code
          if (key === "errGeneric" && (err.status === 400 || err.status === 401 || err.status === 403) && req.body.oldPin) key = "errPinWrong";
          setMsg(v.pinMsg, key === "errGeneric" ? errText(err) : tr(key), "warn");
        })
        .then(function () {
          busy([v.pinBtn, v.removeBtn], false);
          return loadAll(true);
        });
    }

    function submitPin() {
      var mode = pinSet() ? "change" : "set";
      var req = I.pinRequest(mode, { oldPin: v.oldIn.value, pin: v.newIn.value, confirm: v.confIn.value });
      if (!req.ok) {
        setMsg(v.pinMsg, tr(req.key), "warn");
        focusField(req.field);
        return;
      }
      sendPin(req, mode === "change" ? "pinChanged" : "pinSaved");
    }

    function removePin() {
      var req = I.pinRequest("remove", { oldPin: v.oldIn.value });
      if (!req.ok) {
        setMsg(v.pinMsg, tr(req.key), "warn");
        focusField(req.field);
        return;
      }
      if (!window.confirm(tr("pinRemoveConfirm"))) return;
      sendPin(req, "pinRemoved");
    }

    function saveLock(on, retried) {
      var a = api();
      if (typeof a.saveSettings !== "function") return Promise.reject(new Error("not ready"));
      return a.saveSettings({ personalLock: on }).catch(function (err) {
        // switching the lock with a PIN set needs this device unlocked
        if (!retried && err && err.status === 403 && I.errorKey(err) === "errLocked" && typeof a.requestUnlock === "function") {
          return a.requestUnlock().then(function (ok) {
            if (!ok) throw err;
            return saveLock(on, true);
          });
        }
        throw err;
      });
    }

    function toggleLock(on) {
      setMsg(v.lockMsg, "");
      if (on && !pinSet()) {
        v.lockBox.checked = false;
        setMsg(v.lockMsg, tr("lockNeedsPin"), "warn");
        return;
      }
      v.lockBox.disabled = true;
      var pre = Promise.resolve(true);
      // with a PIN, unlock first so the save does not bounce
      if (pinSet() && !shared.unlock.unlocked && typeof api().requestUnlock === "function") pre = api().requestUnlock();
      pre
        .then(function (ok) {
          if (!ok) throw Object.assign(new Error("locked"), { status: 403, code: "locked" });
          return saveLock(on, false);
        })
        .then(function (saved) {
          var now = saved && typeof saved === "object" ? !!saved.personalLock : on;
          toast(tr(now ? "lockOn" : "lockOff"), { type: "success", key: "int-lock" });
        })
        .catch(function (err) {
          setMsg(v.lockMsg, errText(err), "warn");
        })
        .then(function () {
          v.lockBox.disabled = false;
          return loadAll(true);
        });
    }

    function lockNow() {
      fetchJSON("/api/unlock", { method: "DELETE" })
        .then(function () {
          toast(tr("lockedNow"), { type: "success", key: "int-lock" });
        })
        .catch(function (err) {
          toast(errText(err), { type: "warn", key: "int-lock" });
        })
        .then(function () {
          return loadAll(true);
        });
    }

    v.render();
    loadAll(false);
  }

  // ---------------------------------------------------------------- check-ins section

  function mountCheckins(root) {
    root.classList.add("int", "int-checkins");
    var v = { root: root, lang: "" };
    views.checkins = v;

    v.render = function () {
      v.lang = lang();
      clear(root);
      root.appendChild(el("label", { class: "check" }, [el("input", { type: "checkbox", "data-setting": "checkinMeetings" }), " " + tr("meetings")]));
      root.appendChild(el("p", { class: "hint", text: tr("meetingsHint") }));
      v.noCal = el("p", { class: "hint warn", text: tr("meetingsNoCal"), hidden: true });
      root.appendChild(v.noCal);
      append(root, row(tr("lead"), el("input", { type: "number", "data-setting": "checkinMeetingLead", min: "1", max: "60", step: "1", inputmode: "numeric" })));

      v.sitBox = el("input", { type: "checkbox", "data-setting": "checkinSitting", id: "int_sitting" });
      v.sitBox.addEventListener("change", onSittingToggle);
      root.appendChild(el("label", { class: "check", for: "int_sitting" }, [v.sitBox, " " + tr("sitting")]));
      root.appendChild(el("p", { class: "hint", text: tr("sittingHint") }));
      v.sitStatus = el("p", { class: "int-status", role: "status" });
      v.sitAllow = el("button", { type: "button", class: "small", text: tr("sitAllow"), onclick: allowHere, hidden: true });
      v.sitResume = el("button", {
        type: "button",
        class: "small ghost",
        text: tr("sitResume"),
        hidden: true,
        onclick: function () {
          var ui = checkins();
          if (ui) ui.resume();
          v.update();
        },
      });
      root.appendChild(el("div", { class: "int-inline" }, [v.sitStatus, v.sitAllow, v.sitResume]));
      append(root, row(tr("sittingMin"), el("input", { type: "number", "data-setting": "checkinSittingMin", min: "20", max: "240", step: "5", inputmode: "numeric" })));

      root.appendChild(
        el("div", { class: "row" }, [
          el("label", null, [el("span", { text: tr("quietStart") }), el("input", { type: "time", "data-setting": "quietStart", step: "60" })]),
          el("label", null, [el("span", { text: tr("quietEnd") }), el("input", { type: "time", "data-setting": "quietEnd", step: "60" })]),
        ])
      );
      root.appendChild(el("p", { class: "hint", text: tr("quietHint") }));
      v.pause = el("p", { class: "int-status", hidden: true });
      root.appendChild(v.pause);
      fillSettings(root);
      v.update();
    };

    v.update = function () {
      if (v.lang !== lang()) return v.render();
      var s = settings();
      var now = Date.now();
      var tz = s.timezone || "";
      var calSt = I.calendarState(shared.cal);
      v.noCal.hidden = !(shared.calLoaded && !!s.checkinMeetings && calSt !== "connected");

      var ui = checkins();
      var st = ui ? ui.state() : { support: "unsupported" };
      var text = "";
      var showAllow = false;
      if (!s.checkinSitting) text = tr("sitOff");
      else if (st.support === "insecure") text = tr("sitInsecure");
      else if (st.support !== "ok") text = tr("sitUnsupported");
      else if (st.running) text = st.active ? tr("sitRunning", { n: st.minutes }) : tr("sitRunningIdle");
      else if (st.permission === "denied" || st.error === "denied") text = tr("sitDenied");
      else if (st.error) {
        text = tr("sitError");
        showAllow = true;
      } else {
        text = tr("sitAsk");
        showAllow = true;
      }
      v.sitStatus.textContent = text;
      v.sitAllow.hidden = !showAllow;
      var localOff = st.localPausedUntil || 0;
      if (localOff && s.checkinSitting) {
        v.sitStatus.textContent = text + " " + tr("sitLocalOff", { until: I.formatUntil(localOff, now, lang(), tz) });
      }
      v.sitResume.hidden = !(localOff && s.checkinSitting);

      var lines = [];
      var paused = shared.checkState.pausedUntil;
      var busyUntil = shared.checkState.busyUntil;
      if (paused > now) lines.push(tr("pausedUntil", { until: I.formatUntil(paused, now, lang(), tz) }));
      if (busyUntil > now && s.checkinSitting) lines.push(tr("busyUntil", { until: I.formatUntil(busyUntil, now, lang(), tz) }));
      v.pause.textContent = lines.join(" ");
      v.pause.hidden = !lines.length;
    };

    /* Toggle in a user gesture: ask for the permission right here. */
    function onSittingToggle() {
      var ui = checkins();
      if (!v.sitBox.checked) {
        if (ui) setTimeout(ui.sync, 0); // after app.js read the form
        v.update();
        return;
      }
      if (!ui || ui.support() !== "ok") {
        // the setting may still be on for other devices; this one cannot help
        v.update();
        return;
      }
      ui.enable().then(function (res) {
        if (res === "denied") {
          v.sitBox.checked = false;
          notifyForm(v.sitBox);
        }
        v.update();
      });
    }

    function allowHere() {
      var ui = checkins();
      if (!ui) return;
      ui.enable().then(function () {
        v.update();
      });
    }

    var ui = checkins();
    if (ui && typeof ui.onChange === "function") ui.onChange(function () {
      if (views.checkins) views.checkins.update();
    });
    v.render();
    loadAll(false);
  }

  // ---------------------------------------------------------------- smart home section

  function mountHome(root) {
    root.classList.add("int", "int-home");
    var v = { root: root, lang: "", url: null };
    views.home = v;

    v.render = function () {
      v.lang = lang();
      v.url = null;
      clear(root);
      v.status = el("p", { class: "int-status", role: "status", text: tr("haLoading") });
      root.appendChild(el("div", { class: "int-label", text: tr("haStatusLabel") }));
      root.appendChild(v.status);
      v.entLine = el("p", { class: "hint", hidden: true });
      root.appendChild(v.entLine);
      v.entList = el("ul", { class: "int-entities" });
      v.entBox = el("details", { class: "int-details", hidden: true }, [el("summary", { text: tr("haEntitiesShow") }), v.entList]);
      root.appendChild(v.entBox);

      // address + token (POST /api/integrations/ha, admin only)
      v.adminHint = el("p", { class: "hint warn", text: tr("haAdminOnly"), hidden: true });
      root.appendChild(v.adminHint);
      v.urlIn = el("input", {
        type: "text",
        inputmode: "url",
        maxlength: "512",
        autocomplete: "off",
        spellcheck: "false",
        placeholder: tr("haUrlPlaceholder"),
        id: "int_ha_url",
        "aria-describedby": "int_ha_bind",
      });
      v.tokenIn = el("input", { type: "password", maxlength: "2048", autocomplete: "new-password", spellcheck: "false", id: "int_ha_token" });
      v.urlState = el("span", { class: "int-cred" });
      v.tokenState = el("span", { class: "int-cred" });
      var form = el("form", { class: "int-ha", novalidate: true }, [
        el("label", { for: "int_ha_url" }, [el("span", null, [tr("haUrl") + " ", v.urlState]), v.urlIn]),
        el("label", { for: "int_ha_token" }, [el("span", null, [tr("haToken") + " ", v.tokenState]), v.tokenIn]),
      ]);
      // the binding of address and token, explained before anyone trips over it
      form.appendChild(el("p", { class: "hint", id: "int_ha_bind", text: tr("haBindHint") }));
      v.envHint = el("p", { class: "hint", text: tr("haEnvHint"), hidden: true });
      form.appendChild(v.envHint);
      v.saveBtn = el("button", { type: "submit", class: "small", text: tr("haSave") });
      v.testBtn = el("button", { type: "button", class: "small ghost", text: tr("haTest"), onclick: testConnection });
      v.clearBtn = el("button", { type: "button", class: "small ghost danger", text: tr("haClear"), onclick: clearHA });
      form.appendChild(el("div", { class: "int-actions" }, [v.saveBtn, v.testBtn, v.clearBtn]));
      v.msg = el("p", { class: "int-msg", role: "alert", hidden: true });
      form.appendChild(v.msg);
      form.addEventListener("submit", function (e) {
        e.preventDefault();
        saveHA();
      });
      // typing here is not a settings change; keep app.js's dirty state out of it
      [v.urlIn, v.tokenIn].forEach(function (n) {
        n.addEventListener("input", function (e) {
          e.stopPropagation();
        });
        n.addEventListener("change", function (e) {
          e.stopPropagation();
        });
      });
      root.appendChild(form);

      // setup help
      root.appendChild(
        el("details", { class: "int-details int-setup" }, [
          el("summary", { text: tr("haSetupTitle") }),
          el("ol", null, [el("li", { text: tr("haSetup1") }), el("li", { text: tr("haSetup2") }), el("li", { text: tr("haSetup3") })]),
        ])
      );

      // who may do what (plain settings, saved with the Save button)
      v.control = el("select", { "data-setting": "homeControl" }, [
        el("option", { value: "known", text: tr("hcKnown") }),
        el("option", { value: "everyone", text: tr("hcEveryone") }),
        el("option", { value: "off", text: tr("hcOff") }),
      ]);
      v.control.addEventListener("change", function () {
        v.offHint.hidden = v.control.value !== "off";
      });
      append(root, row(tr("homeControl"), v.control));
      v.offHint = el("p", { class: "hint warn", text: tr("homeOffHint"), hidden: true });
      root.appendChild(v.offHint);
      append(
        root,
        row(
          tr("homeQueries"),
          el("select", { "data-setting": "homeQueries" }, [el("option", { value: "known", text: tr("hqKnown") }), el("option", { value: "everyone", text: tr("hqEveryone") })]),
          tr("homeAccessHint")
        )
      );
      v.langSel = el("select", { "data-setting": "haLanguage" });
      append(root, row(tr("haLanguage"), v.langSel, tr("haLangHint")));
      fillLang();
      fillSettings(root);
      v.offHint.hidden = v.control.value !== "off";
      v.update();
    };

    /* The language options, keeping a stored code other than de/en. */
    function fillLang() {
      var cur = settings().haLanguage || "";
      clear(v.langSel);
      I.haLanguageOptions(cur, tr).forEach(function (o) {
        v.langSel.appendChild(el("option", { value: o.value, text: o.label }));
      });
    }

    v.update = function () {
      if (v.lang !== lang()) return v.render();
      var ha = shared.ha;
      var integOk = !!shared.integ;
      var admin = isAdmin();
      var h = ha || I.normalizeHA(null);

      // credential state (the token is never shown, only whether it is there)
      var uSt = I.credentialState(!!h.url, h.urlFromEnv);
      var tSt = I.credentialState(h.tokenSet, h.tokenFromEnv);
      v.urlState.textContent = shared.integLoaded && uSt === "env" ? "(" + tr("credEnv") + ")" : "";
      v.tokenState.textContent = shared.integLoaded ? "(" + tr(tSt === "env" ? "credEnv" : tSt === "set" ? "credSet" : "credUnset") + ")" : "";
      // prefill the address once per stored value; never overwrite what is being typed
      if (v.url !== h.url && (document.activeElement !== v.urlIn || v.urlIn.value === (v.url || ""))) {
        v.urlIn.value = h.url;
        v.url = h.url;
      }
      v.tokenIn.placeholder = tSt === "set" ? tr("haTokenPlaceholderSet") : tr("haTokenPlaceholder");
      var noEdit = !integOk || !admin;
      v.urlIn.disabled = noEdit || h.urlFromEnv;
      v.tokenIn.disabled = noEdit || h.tokenFromEnv;
      v.saveBtn.disabled = noEdit || (h.urlFromEnv && h.tokenFromEnv);
      v.clearBtn.hidden = noEdit || (h.urlFromEnv && h.tokenFromEnv) || !(h.url || h.tokenSet);
      v.envHint.hidden = !h.fromEnv;
      // HA_URL holds something the server cannot use (a path, credentials …)
      v.envHint.textContent = h.urlValid ? tr("haEnvHint") : tr("haEnvHint") + " " + tr("errHaUrl");
      v.envHint.className = h.urlValid ? "hint" : "hint warn";
      v.adminHint.hidden = !(integOk && !admin);
      if (shared.integLoaded && !integOk) setMsg(v.msg, tr("errUnavailable"), "warn");
      if (fillLangNeeded()) {
        fillLang();
        v.langSel.value = settings().haLanguage || "";
      }

      // status line
      var loaded = shared.homeLoaded && shared.integLoaded;
      var state = I.homeState(shared.home, ha, shared.homeMissing);
      var t = I.homeStateText(state, shared.home);
      v.status.textContent = loaded ? tr(t.key, t.vars) : tr("haLoading");
      v.status.setAttribute("data-state", loaded ? (state === "connected" ? "connected" : state === "unconfigured" || state === "unavailable" ? "off" : "error") : "loading");
      // anyone may look at the cached status; a live test is admin only (SPEC 3.8)
      v.testBtn.disabled = v.testing || state === "unconfigured" || state === "no_token" || shared.homeMissing;

      // entities
      var ent = shared.homeEntities;
      var n = ent ? ent.count : shared.home && shared.home.connected ? shared.home.entities : -1;
      if (state === "connected" && n >= 0) {
        v.entLine.textContent = ent && ent.blocked ? tr("haEntitiesBlocked", { n: n, b: ent.blocked }) : tr("haEntities", { n: n });
        v.entLine.hidden = false;
      } else v.entLine.hidden = true;
      clear(v.entList);
      if (state === "connected" && ent && ent.items.length) {
        ent.items.forEach(function (it) {
          var li = el("li", { class: it.blocked ? "blocked" : null }, [el("span", { class: "int-ent-name", text: it.name })]);
          var meta = [it.area, it.domain].filter(Boolean).join(" · ");
          if (meta) li.appendChild(el("span", { class: "int-ent-meta", text: meta }));
          if (it.blocked) li.appendChild(el("span", { class: "int-ent-flag", text: tr("haBlocked") }));
          v.entList.appendChild(li);
        });
        v.entBox.hidden = false;
      } else v.entBox.hidden = true;
    };

    function fillLangNeeded() {
      var cur = settings().haLanguage || "";
      for (var i = 0; i < v.langSel.options.length; i++) if (v.langSel.options[i].value === cur) return false;
      return true;
    }

    function focus(field) {
      try {
        (field === "url" ? v.urlIn : v.tokenIn).focus();
      } catch (e) {
        /* ignore */
      }
    }

    function post(body, okText) {
      setMsg(v.msg, "");
      busy([v.saveBtn, v.clearBtn, v.testBtn], true);
      return fetchJSON("/api/integrations/ha", { method: "POST", body: body })
        .then(function (j) {
          v.tokenIn.value = "";
          // the response is the masked view; show it right away
          if (j && typeof j === "object") {
            shared.integ = I.normalizeIntegrations(j);
            shared.ha = I.normalizeHA(j);
          }
          v.url = null; // re-prefill from the saved value
          toast(okText, { type: "success", key: "int-ha" });
          return true;
        })
        .catch(function (err) {
          setMsg(v.msg, errText(err), "warn");
          return false;
        })
        .then(function (ok) {
          busy([v.saveBtn, v.clearBtn, v.testBtn], false);
          updateAll();
          // the server re-checks HA after a change; test right away when complete
          if (ok && shared.ha && shared.ha.url && shared.ha.tokenSet) return runTest(true);
          return loadAll(true);
        });
    }

    function saveHA() {
      var req = I.haRequest({ url: v.urlIn.value, token: v.tokenIn.value }, shared.ha);
      if (!req.ok) {
        setMsg(v.msg, tr(req.key), "warn");
        focus(req.field);
        return;
      }
      if (req.action === "clear") return clearHA();
      if (req.clearsToken && !window.confirm(tr("haUrlChangeConfirm"))) return;
      post(req.body, req.clearsToken ? tr("haSavedNoToken") : tr("haSaved"));
    }

    function clearHA() {
      if (!window.confirm(tr("haClearConfirm"))) return;
      post({ url: "" }, tr("haCleared"));
    }

    /*
     * Admin: POST /api/home/test pings HA with the stored address and token.
     * Others, or a server without it: the cached GET /api/home/status.
     */
    function runTest(quiet) {
      v.testing = true;
      setMsg(v.msg, quiet ? "" : tr("haTesting"));
      v.testBtn.disabled = true;
      var useTest = !!shared.integ && isAdmin();
      var p = useTest
        ? fetchJSON("/api/home/test", { method: "POST", body: {} }).catch(function (err) {
            var st = (err && err.status) || 0;
            if (st === 404 || st === 405 || st === 403) return fetchJSON("/api/home/status");
            throw err;
          })
        : fetchJSON("/api/home/status");
      return p
        .then(function (j) {
          shared.homeMissing = false;
          shared.home = I.normalizeHomeStatus(j);
          shared.homeLoaded = true;
          var state = I.homeState(shared.home, shared.ha, false);
          if (state === "connected") {
            setMsg(v.msg, "");
            if (!quiet) toast(tr("haTestOk"), { type: "success", key: "int-ha" });
          } else {
            var t = I.homeStateText(state, shared.home);
            setMsg(v.msg, tr(t.key, t.vars), "warn");
          }
          return loadHomeEntities();
        })
        .catch(function (err) {
          var st = (err && err.status) || 0;
          if (st === 404 || st === 405) {
            shared.homeMissing = true;
            setMsg(v.msg, tr("haUnavailable"), "warn");
          } else setMsg(v.msg, errText(err), "warn");
        })
        .then(function () {
          v.testing = false;
          updateAll();
        });
    }

    function testConnection() {
      runTest(false);
    }

    v.render();
    loadAll(false);
  }

  // ---------------------------------------------------------------- OAuth return

  /* /?calendar=connected|error&reason=… after the Google redirect (SPEC 3.7). */
  function handleCalendarReturn() {
    var r;
    try {
      r = I.calendarReturn(window.location.search);
    } catch (e) {
      return;
    }
    if (!r.kind) return;
    try {
      if (window.history && typeof window.history.replaceState === "function") {
        window.history.replaceState(window.history.state, "", window.location.pathname + r.search + window.location.hash);
      }
    } catch (e) {
      /* ignore */
    }
    if (r.kind === "connected") toast(tr("connectedToast"), { type: "success", key: "int-cal", ms: 6000 });
    else toast(tr("connectFailed", { reason: tr(I.reasonKey(r.reason)) }), { type: "warn", key: "int-cal", ms: 10000 });
    try {
      if (typeof api().openSettings === "function") api().openSettings("calendar");
    } catch (e) {
      /* ignore */
    }
    loadAll(true);
  }

  // ---------------------------------------------------------------- wiring

  if (typeof H.registerSettingsSection === "function") {
    H.registerSettingsSection({ id: "calendar", order: 40, title: { en: I.STRINGS.en.secCalendar, de: I.STRINGS.de.secCalendar }, mount: mountCalendar });
    H.registerSettingsSection({ id: "privacy", order: 45, title: { en: I.STRINGS.en.secPrivacy, de: I.STRINGS.de.secPrivacy }, mount: mountPrivacy });
    H.registerSettingsSection({ id: "home", order: 50, title: { en: I.STRINGS.en.secHome, de: I.STRINGS.de.secHome }, mount: mountHome });
    H.registerSettingsSection({ id: "checkins", order: 60, title: { en: I.STRINGS.en.secCheckins, de: I.STRINGS.de.secCheckins }, mount: mountCheckins });
  }

  H.on("ready", handleCalendarReturn);
  H.on("settings", function () {
    updateAll();
    loadAll(false);
  });
  H.on("lang", updateAll);
  H.on("hub:calendar.changed", function () {
    loadAll(true);
  });
  // "Trag morgen um 15 Uhr Zahnarzt ein" → {kind:"calendar", data:{added:true, events:[ev]}}
  H.on("card", function (c) {
    var a = I.calendarAdded(c, lang());
    if (a) toast(tr("cardAdded", a), { type: "success", key: "int-cal-added", ms: 6000 });
  });
  // secrets.go: {what: "ha"|"google"|"lock"}, never secrets; re-read the masked view
  H.on("hub:integrations.changed", function () {
    loadAll(true);
  });
  H.on("hub:people.changed", function () {
    loadAll(true);
  });
  H.on("visible", function () {
    loadAll(false);
  });
  // the Settings panel was opened (or something in it got focus): refresh stale data
  document.addEventListener(
    "focusin",
    function (e) {
      var t = e.target;
      if (t && t.closest && t.closest("#settingsSections")) loadAll(false);
    },
    true
  );
})();
