/*
 * HERMES people UI (W1-F, SPEC 5.4).
 *
 * Part 1 (UMD, no DOM): pure helpers for the browser (window.HermesPeople)
 * and the node tests (require("../../static/people.js")):
 *
 *   shouldStick, payloadSpeaker, normalizeManual, validateName, nameTaken,
 *   personBadges, deleteSummary, formatLastSeen, formatExpiry, formatScore,
 *   levelToPercent, errorKey, runEnrolment, runIdentify, SENTENCES, STRINGS
 *
 * Part 2 (browser only, needs window.Hermes from hermes.js): Settings →
 * People, Settings → Voice ID tuning, the enrolment wizard, "test
 * recognition", the speaker chip with the `payload` hook, the person toast
 * ("Anna angelegt · Behalten · Entfernen"), the first-person migration
 * dialog and the shared PIN prompt for the personal lock.
 *
 * Voiceprints never reach this file: the server never returns embeddings,
 * and the wizard only forwards the voice object VoiceID.record() produced.
 */
(function (root, factory) {
  var api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root && typeof root === "object") root.HermesPeople = api;
})(typeof window !== "undefined" ? window : typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  var STICKY_MS = 120000; // SPEC 3.1: last voice match on this client within 120 s
  var RECORD_MS = 7000; // SPEC 5.4: VoiceID.record(7000) per take
  var IDENTIFY_MS = 6000;
  var MIN_TAKE_SEC = 2.0; // SPEC 4.6: anchors need ≥ 2.0 s voiced audio
  var TAKES = 3;
  var MAX_INCONSISTENT = 2; // per take; then the wizard starts over
  var NAME_MAX = 40;

  // Neutral, phonetically varied; no wake word, no command verbs, no
  // list/calendar/device nouns (SPEC 5.4). Copied verbatim.
  var SENTENCES = {
    de: [
      "Am frühen Morgen liegt noch leichter Nebel über den Wiesen am Fluss.",
      "Zwölf große Boxkämpfer jagen Viktor quer über den sandigen Deich.",
      "Im Herbst färben sich die Blätter gelb und rot, und die Luft riecht nach Regen.",
    ],
    en: [
      "In the early morning a light mist still lies over the meadows by the river.",
      "The quick brown fox jumps over the lazy dog near the quiet harbour.",
      "In autumn the leaves turn yellow and red, and the air smells of rain.",
    ],
  };

  /*
   * UI strings. Terminology (SPEC 4.7): "Erinnerung" means a reminder only;
   * stored memories are "Gedächtnis" / "Eintrag" / "gemerkt".
   */
  var STRINGS = {
    en: {
      sectionPeople: "People",
      sectionVoice: "Voice recognition",
      disclaimer:
        "Voice recognition is not a security feature. Anyone in the room or with access to this device can pretend to be someone else. For more privacy, turn on the PIN lock.",
      insecure: "Voice recognition only works on this device via http://localhost.",
      noAssets: "Voice recognition files are missing. Run “sh scripts/fetch-assets.sh” and reload.",
      voiceOff: "Voice recognition is turned off (Settings → Voice recognition).",
      voiceStatus: "{enrolled} of {people} people have a voice profile.",
      empty: "No people yet. Add someone here, or say “Hermes, I'm …”.",
      nameLabel: "Name",
      namePlaceholder: "e.g. Anna",
      add: "Add",
      adding: "Adding …",
      badgeProvisional: "provisional",
      badgeUnconfirmed: "unconfirmed",
      badgeOldModel: "voice profile outdated",
      samples: "{n} recordings",
      samples1: "1 recording",
      noVoice: "no voice yet",
      memories: "{n} memory entries",
      memories1: "1 memory entry",
      lastSeen: "last heard {when}",
      neverSeen: "not recognised yet",
      expires: "removed {when} unless kept",
      rename: "Rename",
      save: "Save",
      cancel: "Cancel",
      close: "Close",
      del: "Delete",
      recordVoice: "Record voice",
      rerecord: "Record again",
      deleteVoice: "Delete voice profile",
      keep: "Keep",
      remove: "Remove",
      testRecognition: "Test recognition",
      oldModelHint: "Recorded with another voice model. Record again so this person is recognised.",
      unconfirmedHint: "Created by voice. Keep it, or record the three sentences, or it is removed after 7 days.",
      deleteTitle: "Delete {name}?",
      deleteSummary: "{name}, the voice profile and {n} memory entries will be deleted.",
      deleteSummary1: "{name}, the voice profile and 1 memory entry will be deleted.",
      deleteSummaryNoVoice: "{name} and {n} memory entries will be deleted.",
      deleteSummaryNoVoice1: "{name} and 1 memory entry will be deleted.",
      deleteLead: "This deletes:",
      deleteItemVoice: "the voice profile ({n} recordings)",
      deleteItemMemories: "{n} memory entries about {name}",
      deleteItemDocs: "documents that belong only to {name}",
      deleteItemEvents: "pending announcements for {name}",
      deleteItemBackups: "backup copies of damaged data files (they may still contain old voice profiles)",
      deleteKeeps: "Reminders stay, but are no longer announced with the name.",
      deleteFinal: "This cannot be undone.",
      deleteConfirm: "Delete for good",
      deletedToast: "{name} deleted.",
      deleteVoiceTitle: "Delete the voice profile of {name}?",
      deleteVoiceBody:
        "The {n} recordings and backup copies of damaged people files are deleted. {name} and their memory entries stay.",
      deleteVoiceConfirm: "Delete voice profile",
      voiceDeletedToast: "Voice profile of {name} deleted.",
      renamedToast: "Renamed to {name}.",
      keptToast: "{name} stays saved.",
      removedToast: "{name} removed.",
      createdToast: "{name} created",
      namePendingToast: "Name: “{name}”",
      err_empty: "Please enter a name.",
      err_long: "At most 40 characters.",
      err_chars: "Only letters, spaces, - and ' are allowed.",
      err_exists: "This name already exists.",
      err_limit: "No more people can be added (12 at most).",
      err_locked: "Locked. Please enter the PIN.",
      err_readonly: "The data file was written by a newer HERMES. Changes are blocked.",
      err_network: "The server cannot be reached.",
      err_notfound: "This person no longer exists.",
      err_model: "The voice model does not match the server. Please reload the page.",
      err_generic: "That did not work: {msg}",
      wizTitle: "Record voice: {name}",
      wizIntro:
        "Read three short sentences aloud at your normal volume. While you record, HERMES ignores commands and stays quiet.",
      wizRoom: "Record in the room and with the microphone you normally use with HERMES.",
      wizReplace: "The existing voice profile is replaced by the new recordings.",
      wizStep: "Sentence {i} of {n}",
      wizStart: "Start recording",
      wizAgain: "Record again",
      wizRecording: "Listening … read the sentence now.",
      wizSaving: "Checking …",
      wizTakeOk: "Got it.",
      wizShort: "That was too short. Please read the whole sentence again.",
      wizInconsistent: "That sounded different from the first recording. Please try again at the same distance from the microphone.",
      wizRestarted: "The recordings did not match. Let's start again from the first sentence.",
      wizDone: "Done. HERMES now recognises {name} by voice.",
      wizDoneProvisional: "Saved. One more successful recording is needed before the profile is final.",
      wizCancelled: "Recording cancelled.",
      wizUnavailable: "Voice recording is not available here.",
      wizMicDenied: "No access to the microphone. Allow it in the browser and try again.",
      wizLevel: "Input level",
      idTitle: "Test recognition",
      idIntro: "Press start and say a sentence at your normal volume.",
      idStart: "Start",
      idListening: "Listening …",
      idResult: "Recognised: {name}",
      idGuest: "Not recognised (guest)",
      idUncertain: "Not sure who that was",
      idShort: "I heard too little speech. Please speak a little longer.",
      idScores: "Scores",
      idThreshold: "Threshold {th} · margin {margin}",
      idNoPeople: "Nobody has a voice profile yet.",
      chipAria: "Who is speaking?",
      chipAuto: "Automatic (voice)",
      chipAutoShort: "Auto",
      chipGuest: "Guest",
      chipUncertain: "Not sure",
      chipManual: "chosen by hand",
      chipLast: "Automatic. Last recognised: {name}",
      unlockTitle: "Enter PIN",
      unlockBody: "Personal data is locked on this device.",
      unlockPin: "PIN",
      unlockGo: "Unlock",
      unlockWrong: "Wrong PIN.",
      unlockWait: "Too many attempts. Please wait a minute.",
      lockedBar: "Personal data is locked.",
      lockNow: "Lock",
      unlockBtn: "Unlock",
      lockedToast: "Locked again.",
      migTitle: "Who do these entries belong to?",
      migBody: "Before I knew any people, I remembered {n} things. Who do they belong to?",
      migBody1: "Before I knew any people, I remembered 1 thing. Who does it belong to?",
      migHousehold: "Household*",
      migLater: "Later",
      migNote: "* Entries about health, relationships and identity still go to {name}, never to the household.",
      migDone: "{n} entries assigned.",
      vEnabled: "Use voice recognition",
      vThreshold: "Recognition threshold",
      vThresholdHint: "Higher means fewer mix-ups but more often “guest”. Default 0.60.",
      vMargin: "Lead over the second-best person",
      vMarginHint: "How clearly the best match must win. Default 0.05.",
      vMinSec: "Minimum speech (seconds)",
      vMinSecHint: "Shorter utterances count as “not sure”. Default 1.0.",
      vAdapt: "Keep learning from very confident matches",
      vAdaptHint: "Adds at most 7 extra samples per person. Only the recording wizard makes a profile final.",
      vDefaults: "Default values",
      vDefaultsToast: "Voice recognition defaults loaded. Press Save to keep them.",
    },
    de: {
      sectionPeople: "Personen",
      sectionVoice: "Stimmerkennung",
      disclaimer:
        "Stimmerkennung ist kein Schutz. Wer im Raum ist oder Zugriff auf dieses Gerät hat, kann sich als jemand anderes ausgeben. Für mehr Privatsphäre aktiviere die PIN-Sperre.",
      insecure: "Stimmerkennung nur auf diesem Gerät über http://localhost.",
      noAssets: "Die Dateien für die Stimmerkennung fehlen. Führe „sh scripts/fetch-assets.sh“ aus und lade die Seite neu.",
      voiceOff: "Die Stimmerkennung ist ausgeschaltet (Einstellungen → Stimmerkennung).",
      voiceStatus: "{enrolled} von {people} Personen haben ein Stimmprofil.",
      empty: "Noch keine Personen. Lege hier jemanden an oder sag „Hermes, ich bin …“.",
      nameLabel: "Name",
      namePlaceholder: "z. B. Anna",
      add: "Hinzufügen",
      adding: "Lege an …",
      badgeProvisional: "vorläufig",
      badgeUnconfirmed: "unbestätigt",
      badgeOldModel: "Stimmprofil veraltet",
      samples: "{n} Aufnahmen",
      samples1: "1 Aufnahme",
      noVoice: "noch keine Stimme",
      memories: "{n} Gedächtniseinträge",
      memories1: "1 Gedächtniseintrag",
      lastSeen: "zuletzt gehört {when}",
      neverSeen: "noch nicht erkannt",
      expires: "wird {when} entfernt, wenn nicht behalten",
      rename: "Umbenennen",
      save: "Speichern",
      cancel: "Abbrechen",
      close: "Schließen",
      del: "Löschen",
      recordVoice: "Stimme aufnehmen",
      rerecord: "Neu aufnehmen",
      deleteVoice: "Stimmprofil löschen",
      keep: "Behalten",
      remove: "Entfernen",
      testRecognition: "Erkennung testen",
      oldModelHint: "Mit einem anderen Stimmmodell aufgenommen. Nimm neu auf, damit die Person erkannt wird.",
      unconfirmedHint: "Per Sprache angelegt. Behalte die Person oder nimm die drei Sätze auf, sonst wird sie nach 7 Tagen entfernt.",
      deleteTitle: "{name} löschen?",
      deleteSummary: "{name}, das Stimmprofil und {n} Gedächtniseinträge werden gelöscht.",
      deleteSummary1: "{name}, das Stimmprofil und 1 Gedächtniseintrag werden gelöscht.",
      deleteSummaryNoVoice: "{name} und {n} Gedächtniseinträge werden gelöscht.",
      deleteSummaryNoVoice1: "{name} und 1 Gedächtniseintrag werden gelöscht.",
      deleteLead: "Gelöscht werden:",
      deleteItemVoice: "das Stimmprofil ({n} Aufnahmen)",
      deleteItemMemories: "{n} Gedächtniseinträge über {name}",
      deleteItemDocs: "Dokumente, die nur {name} gehören",
      deleteItemEvents: "noch ausstehende Ansagen für {name}",
      deleteItemBackups: "Sicherungskopien beschädigter Datendateien (sie können alte Stimmprofile enthalten)",
      deleteKeeps: "Erinnerungen bleiben erhalten, werden aber ohne Namen angesagt.",
      deleteFinal: "Das kann nicht rückgängig gemacht werden.",
      deleteConfirm: "Endgültig löschen",
      deletedToast: "{name} gelöscht.",
      deleteVoiceTitle: "Stimmprofil von {name} löschen?",
      deleteVoiceBody:
        "Die {n} Aufnahmen und Sicherungskopien beschädigter Personendateien werden gelöscht. {name} und die Gedächtniseinträge bleiben.",
      deleteVoiceConfirm: "Stimmprofil löschen",
      voiceDeletedToast: "Stimmprofil von {name} gelöscht.",
      renamedToast: "Umbenannt in {name}.",
      keptToast: "{name} bleibt gespeichert.",
      removedToast: "{name} entfernt.",
      createdToast: "{name} angelegt",
      namePendingToast: "Name: „{name}“",
      err_empty: "Bitte gib einen Namen ein.",
      err_long: "Höchstens 40 Zeichen.",
      err_chars: "Nur Buchstaben, Leerzeichen, - und ' sind erlaubt.",
      err_exists: "Diesen Namen gibt es schon.",
      err_limit: "Mehr Personen gehen nicht (höchstens 12).",
      err_locked: "Gesperrt. Bitte gib die PIN ein.",
      err_readonly: "Die Datendatei stammt von einer neueren HERMES-Version. Änderungen sind gesperrt.",
      err_network: "Der Server ist nicht erreichbar.",
      err_notfound: "Diese Person gibt es nicht mehr.",
      err_model: "Das Stimmmodell passt nicht zum Server. Bitte lade die Seite neu.",
      err_generic: "Das hat nicht geklappt: {msg}",
      wizTitle: "Stimme aufnehmen: {name}",
      wizIntro:
        "Lies drei kurze Sätze in normaler Lautstärke vor. Während der Aufnahme reagiert HERMES auf keine Befehle und bleibt still.",
      wizRoom: "Nimm in dem Raum und mit dem Mikrofon auf, die du sonst mit HERMES nutzt.",
      wizReplace: "Das bisherige Stimmprofil wird durch die neuen Aufnahmen ersetzt.",
      wizStep: "Satz {i} von {n}",
      wizStart: "Aufnahme starten",
      wizAgain: "Nochmal aufnehmen",
      wizRecording: "Ich höre zu … lies jetzt den Satz vor.",
      wizSaving: "Prüfe …",
      wizTakeOk: "Passt.",
      wizShort: "Das war zu kurz. Lies bitte den ganzen Satz noch einmal.",
      wizInconsistent: "Das klang anders als die erste Aufnahme. Bitte noch einmal, im selben Abstand zum Mikrofon.",
      wizRestarted: "Die Aufnahmen passten nicht zusammen. Wir fangen noch einmal beim ersten Satz an.",
      wizDone: "Fertig. HERMES erkennt {name} jetzt an der Stimme.",
      wizDoneProvisional: "Gespeichert. Für ein endgültiges Profil fehlt noch eine gelungene Aufnahme.",
      wizCancelled: "Aufnahme abgebrochen.",
      wizUnavailable: "Sprachaufnahme ist hier nicht verfügbar.",
      wizMicDenied: "Kein Zugriff aufs Mikrofon. Erlaube ihn im Browser und versuche es noch einmal.",
      wizLevel: "Eingangspegel",
      idTitle: "Erkennung testen",
      idIntro: "Drück auf Start und sag einen Satz in normaler Lautstärke.",
      idStart: "Start",
      idListening: "Ich höre zu …",
      idResult: "Erkannt: {name}",
      idGuest: "Nicht erkannt (Gast)",
      idUncertain: "Nicht sicher, wer das war",
      idShort: "Ich habe zu wenig Sprache gehört. Sprich bitte etwas länger.",
      idScores: "Werte",
      idThreshold: "Schwelle {th} · Abstand {margin}",
      idNoPeople: "Noch niemand hat ein Stimmprofil.",
      chipAria: "Wer spricht?",
      chipAuto: "Automatisch (Stimme)",
      chipAutoShort: "Auto",
      chipGuest: "Gast",
      chipUncertain: "Unsicher",
      chipManual: "von Hand gewählt",
      chipLast: "Automatisch. Zuletzt erkannt: {name}",
      unlockTitle: "PIN eingeben",
      unlockBody: "Persönliches ist auf diesem Gerät gesperrt.",
      unlockPin: "PIN",
      unlockGo: "Entsperren",
      unlockWrong: "Falsche PIN.",
      unlockWait: "Zu viele Versuche. Bitte warte eine Minute.",
      lockedBar: "Persönliches ist gesperrt.",
      lockNow: "Sperren",
      unlockBtn: "Entsperren",
      lockedToast: "Wieder gesperrt.",
      migTitle: "Wem gehören diese Einträge?",
      migBody: "Bevor ich Personen kannte, habe ich mir {n} Dinge gemerkt. Wem gehören sie?",
      migBody1: "Bevor ich Personen kannte, habe ich mir 1 Sache gemerkt. Wem gehört sie?",
      migHousehold: "Haushalt*",
      migLater: "Später",
      migNote: "* Einträge über Gesundheit, Beziehungen und Identität gehen auch dann an {name}, nie an den Haushalt.",
      migDone: "{n} Einträge zugeordnet.",
      vEnabled: "Stimmerkennung verwenden",
      vThreshold: "Erkennungsschwelle",
      vThresholdHint: "Höher heißt weniger Verwechslungen, aber öfter „Gast“. Standard 0,60.",
      vMargin: "Vorsprung vor der zweitbesten Person",
      vMarginHint: "Wie deutlich der beste Treffer gewinnen muss. Standard 0,05.",
      vMinSec: "Mindestdauer Sprache (Sekunden)",
      vMinSecHint: "Kürzere Äußerungen gelten als „unsicher“. Standard 1,0.",
      vAdapt: "Bei sehr sicheren Treffern weiterlernen",
      vAdaptHint: "Höchstens 7 zusätzliche Proben pro Person. Endgültig wird ein Profil nur über die Aufnahme.",
      vDefaults: "Standardwerte",
      vDefaultsToast: "Standardwerte der Stimmerkennung geladen. Zum Behalten auf Speichern drücken.",
    },
  };

  // Voice-ID defaults and clamps (SPEC 2.4); used by the tuning section.
  var VOICE_SETTINGS = [
    { key: "voiceIdThreshold", def: 0.6, min: 0.45, max: 0.85, step: 0.01, digits: 2 },
    { key: "voiceIdMargin", def: 0.05, min: 0, max: 0.3, step: 0.01, digits: 2 },
    { key: "voiceIdMinSec", def: 1.0, min: 0.5, max: 4, step: 0.1, digits: 1 },
  ];
  var VOICE_TOGGLES = [
    { key: "voiceIdEnabled", def: true },
    { key: "voiceIdAdapt", def: true },
  ];

  // ------------------------------------------------------------ strings

  function format(str, vars) {
    if (typeof str !== "string") return "";
    if (!vars) return str;
    return str.replace(/\{(\w+)\}/g, function (m, k) {
      return Object.prototype.hasOwnProperty.call(vars, k) && vars[k] != null ? String(vars[k]) : m;
    });
  }

  function makeTr(dict, langFn) {
    return function (key, vars) {
      var lang = (langFn && langFn()) === "de" ? "de" : "en";
      var s = (dict[lang] && dict[lang][key]) || (dict.en && dict.en[key]) || key;
      return format(s, vars);
    };
  }

  function fmtNum(n, digits, lang) {
    if (typeof n !== "number" || !isFinite(n)) return "–";
    var s = n.toFixed(digits);
    return lang === "de" ? s.replace(".", ",") : s;
  }

  // ------------------------------------------------------------ speaker

  function isPersonId(id) {
    return typeof id === "string" && /^p_[0-9a-z]+$/i.test(id);
  }

  /*
   * shouldStick(lastEvent, now): true when the last `speaker` event on this
   * client was a voice match of a known person no more than 120 s ago.
   * lastEvent = {id, source, at (ms)}.
   */
  function shouldStick(lastEvent, now) {
    if (!lastEvent || lastEvent.source !== "voice" || !isPersonId(lastEvent.id)) return false;
    var at = Number(lastEvent.at);
    if (!isFinite(at) || !isFinite(now)) return false;
    var age = now - at;
    return age >= 0 && age <= STICKY_MS;
  }

  /*
   * The speaker hint for /api/chat (SPEC 5.4 payload hook).
   *   manual: "" (automatic) | "_guest" | "p_…" (stored choice)
   *   knownIds: ids of existing people (null = unknown, trust the id)
   * Returns {id, source} or null.
   */
  function payloadSpeaker(manual, lastEvent, now, knownIds) {
    var known = function (id) {
      return !knownIds || knownIds.indexOf(id) >= 0;
    };
    if (isPersonId(manual)) return known(manual) ? { id: manual, source: "manual" } : null;
    if (manual === "_guest") return null; // explicit guest: no person hint at all
    if (shouldStick(lastEvent, now) && known(lastEvent.id)) return { id: lastEvent.id, source: "sticky" };
    return null;
  }

  /* A stored manual choice that no longer names an existing person falls back to automatic. */
  function normalizeManual(stored, people) {
    if (stored === "_guest") return "_guest";
    if (!isPersonId(stored)) return "";
    var list = people || [];
    for (var i = 0; i < list.length; i++) if (list[i] && list[i].id === stored) return stored;
    return "";
  }

  // ------------------------------------------------------------ names

  var NAME_RE;
  try {
    NAME_RE = new RegExp("^\\p{L}(?:[\\p{L}\\p{M}]|[ '\\-](?=\\p{L}))*$", "u");
  } catch (e) {
    NAME_RE = /^[A-Za-zÀ-ÖØ-öø-ÿĀ-žḀ-ỿ](?:[A-Za-zÀ-ÖØ-öø-ÿĀ-žḀ-ỿ]|[ '\-](?=[A-Za-zÀ-ÖØ-öø-ÿĀ-žḀ-ỿ]))*$/;
  }

  function runeLen(s) {
    var n = 0;
    for (var i = 0; i < s.length; i++) {
      var c = s.charCodeAt(i);
      if (c < 0xdc00 || c > 0xdfff) n++;
    }
    return n;
  }

  /*
   * validateName(raw) → {ok, name, error}. Mirrors the server rule
   * (SPEC 2.3: 1–40 runes, letters / space / - / '): trims, collapses
   * whitespace, turns typographic apostrophes into ', NFC-normalises.
   * error ∈ "empty" | "long" | "chars".
   */
  function validateName(raw) {
    var s = raw == null ? "" : String(raw);
    if (s.normalize) s = s.normalize("NFC");
    s = s
      .replace(/[‘’ʼ`´]/g, "'")
      .replace(/[‐-–]/g, "-")
      .replace(/\s+/g, " ")
      .trim();
    if (!s) return { ok: false, name: "", error: "empty" };
    if (runeLen(s) > NAME_MAX) return { ok: false, name: s, error: "long" };
    if (!NAME_RE.test(s)) return { ok: false, name: s, error: "chars" };
    return { ok: true, name: s, error: "" };
  }

  function foldName(s) {
    s = String(s || "");
    if (s.normalize) s = s.normalize("NFC");
    return s.toLocaleLowerCase().replace(/\s+/g, " ").trim();
  }

  /* Case-insensitive duplicate check (the server answers 409 "exists" too). */
  function nameTaken(name, people, exceptId) {
    var f = foldName(name);
    if (!f) return false;
    var list = people || [];
    for (var i = 0; i < list.length; i++) {
      var p = list[i];
      if (p && p.id !== exceptId && foldName(p.name) === f) return true;
    }
    return false;
  }

  // ------------------------------------------------------------ people view

  /* Badges for a PersonView, in display order. */
  function personBadges(p) {
    var out = [];
    if (!p) return out;
    if (p.origin === "voice" && !p.confirmed) out.push("unconfirmed");
    if (p.provisional) out.push("provisional");
    if ((p.samples || 0) > 0 && p.voiceModelOk === false) out.push("oldModel");
    return out;
  }

  /*
   * deleteSummary(person, tr) → {title, summary, items[], keeps, final}
   * for the delete dialog: it names exactly what is deleted (SPEC 5.4, 2.1).
   */
  function deleteSummary(p, tr) {
    var name = (p && p.name) || "?";
    var n = Math.max(0, (p && p.memories) | 0);
    var samples = Math.max(0, (p && p.samples) | 0);
    var key = samples > 0 ? "deleteSummary" : "deleteSummaryNoVoice";
    var items = [];
    if (samples > 0) items.push(tr("deleteItemVoice", { n: samples }));
    if (n > 0) items.push(tr("deleteItemMemories", { n: n, name: name }));
    items.push(tr("deleteItemDocs", { name: name }));
    items.push(tr("deleteItemEvents", { name: name }));
    items.push(tr("deleteItemBackups"));
    return {
      title: tr("deleteTitle", { name: name }),
      summary: tr(n === 1 ? key + "1" : key, { name: name, n: n }),
      items: items,
      keeps: tr("deleteKeeps"),
      final: tr("deleteFinal"),
    };
  }

  function parseTime(v) {
    if (v == null || v === "") return NaN;
    var t = typeof v === "number" ? v : Date.parse(v);
    if (!isFinite(t)) return NaN;
    // Go encodes the zero time as 0001-01-01T00:00:00Z (omitempty does not drop it)
    if (new Date(t).getUTCFullYear() < 2000) return NaN;
    return t;
  }

  var MONTHS = {
    en: ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"],
    de: ["Jan.", "Feb.", "März", "Apr.", "Mai", "Juni", "Juli", "Aug.", "Sep.", "Okt.", "Nov.", "Dez."],
  };

  function absDate(t, lang) {
    var d = new Date(t);
    var m = MONTHS[lang === "de" ? "de" : "en"][d.getMonth()];
    return lang === "de" ? d.getDate() + ". " + m + " " + d.getFullYear() : d.getDate() + " " + m + " " + d.getFullYear();
  }

  /* Relative "last heard" text; "" for never (missing or Go zero time). */
  function formatLastSeen(v, now, lang) {
    var t = parseTime(v);
    if (!isFinite(t)) return "";
    var de = lang === "de";
    var s = Math.max(0, Math.round((now - t) / 1000));
    if (s < 60) return de ? "gerade eben" : "just now";
    var m = Math.floor(s / 60);
    if (m < 60) return de ? "vor " + m + " Min." : m + " min ago";
    var h = Math.floor(m / 60);
    if (h < 24) return de ? "vor " + h + " Std." : h + " h ago";
    var d = Math.floor(h / 24);
    if (d === 1) return de ? "gestern" : "yesterday";
    if (d < 7) return de ? "vor " + d + " Tagen" : d + " days ago";
    return de ? "am " + absDate(t, lang) : "on " + absDate(t, lang);
  }

  /* Expiry of an unconfirmed voice person: "in 3 Tagen" / "in 3 days" / "heute". */
  function formatExpiry(v, now, lang) {
    var t = parseTime(v);
    if (!isFinite(t)) return "";
    var de = lang === "de";
    var h = (t - now) / 3600000;
    if (h <= 24) return de ? "heute" : "today";
    var d = Math.ceil(h / 24);
    if (d === 1) return de ? "morgen" : "tomorrow";
    return de ? "in " + d + " Tagen" : "in " + d + " days";
  }

  function formatScore(score, lang) {
    return fmtNum(score, 2, lang);
  }

  /*
   * VoiceID.record's onLevel may report dBFS (≤ 0) or a linear 0..1 value.
   * Map either to 0..100 for the meter (-60 dB → 0, 0 dB → 100).
   */
  function levelToPercent(level) {
    var v = Number(level);
    if (!isFinite(v)) return 0;
    if (v > 0 && v <= 1) return Math.round(v * 100);
    if (v > 1) return 100;
    if (v === 0) return 0;
    var p = ((v + 60) / 60) * 100;
    return Math.round(p < 0 ? 0 : p > 100 ? 100 : p);
  }

  /* Map an API error ({status, code}) to a string key. */
  function errorKey(err) {
    if (!err) return "err_generic";
    var code = err.code || "";
    if (code === "exists" || code === "limit" || code === "locked" || code === "model") return "err_" + code;
    if (code === "short") return "wizShort";
    if (code === "inconsistent") return "wizInconsistent";
    if (err.status === 503 && /read-?only|newer/i.test(String(err.message || "") + code)) return "err_readonly";
    if (code === "readonly" || code === "read_only") return "err_readonly";
    if (err.status === 404) return "err_notfound";
    if (err.status === 403 && code === "") return "err_locked";
    if (!err.status || err.name === "TypeError") return "err_network";
    return "err_generic";
  }

  // ------------------------------------------------------------ enrolment

  function voiceSec(rec) {
    var v = rec && rec.voice;
    var s = v && typeof v.sec === "number" ? v.sec : rec && typeof rec.sec === "number" ? rec.sec : 0;
    return isFinite(s) ? s : 0;
  }

  /*
   * runEnrolment(deps, person, ui) drives the 3-take wizard (SPEC 5.4, 4.6).
   *
   * deps: {
   *   fetchJSON(url, {method, body}) → Promise (rejects with {status, code}),
   *   record(ms, onLevel) → Promise<{voice, sec, level}>,
   *   setEnrolling(bool),            // held true for the whole wizard
   *   stopSpeech() → Promise|void,   // stop TTS before the first take
   *   lang: "de"|"en"
   * }
   * person: PersonView ({id, samples, origin, confirmed})
   * ui: {
   *   awaitTake({index, total, sentence, error}) → Promise<bool> (false = cancel),
   *   onLevel(percent), onPhase(phase, info), cancelled() → bool
   * }
   * Resolves {ok, cancelled, samples, provisional, consistency}; rejects
   * with the API error for anything a retry cannot fix (model, limit, locked…).
   * setEnrolling(false) always runs.
   */
  async function runEnrolment(deps, person, ui) {
    var lang = deps.lang === "de" ? "de" : "en";
    var sentences = SENTENCES[lang];
    var base = "/api/people/" + encodeURIComponent(person.id);
    var hasOld = (person.samples || 0) > 0;
    var result = { ok: false, cancelled: false, samples: 0, provisional: true, consistency: null };
    var phase = ui.onPhase || function () {};
    var onLevel = function (l) {
      if (ui.onLevel) ui.onLevel(levelToPercent(l));
    };
    var cancelled = function () {
      return !!(ui.cancelled && ui.cancelled());
    };
    deps.setEnrolling(true);
    try {
      if (deps.stopSpeech) await deps.stopSpeech();
      var index = 0;
      var error = "";
      var inconsistent = 0;
      while (index < TAKES) {
        var go = await ui.awaitTake({ index: index, total: TAKES, sentence: sentences[index], error: error });
        if (!go || cancelled()) {
          result.cancelled = true;
          return result;
        }
        phase("recording", { index: index });
        var rec = null;
        try {
          rec = await deps.record(RECORD_MS, onLevel);
        } finally {
          onLevel(-100);
        }
        if (cancelled()) {
          result.cancelled = true;
          return result;
        }
        var voice = rec && rec.voice;
        if (!voice || !voice.emb || voice.short || voiceSec(rec) < MIN_TAKE_SEC) {
          error = "short";
          continue;
        }
        phase("saving", { index: index });
        if (hasOld) {
          // "Neu aufnehmen": a new enrolment first removes the old profile (SPEC 4.6)
          await deps.fetchJSON(base + "/voiceprints", { method: "DELETE" });
          hasOld = false;
        }
        var res;
        try {
          res = await deps.fetchJSON(base + "/voiceprints", {
            method: "POST",
            body: { model: voice.model, ort: voice.ort, emb: voice.emb, sec: voiceSec(rec), kind: "anchor" },
          });
        } catch (err) {
          if (err && err.status === 422 && err.code === "short") {
            error = "short";
            continue;
          }
          if (err && err.status === 422 && err.code === "inconsistent") {
            inconsistent++;
            if (inconsistent >= MAX_INCONSISTENT) {
              // the first take was probably the odd one: start over cleanly
              await deps.fetchJSON(base + "/voiceprints", { method: "DELETE" });
              index = 0;
              inconsistent = 0;
              error = "restarted";
            } else {
              error = "inconsistent";
            }
            continue;
          }
          throw err;
        }
        result.samples = res && typeof res.samples === "number" ? res.samples : index + 1;
        result.provisional = !!(res && res.provisional);
        if (res && typeof res.consistency === "number") result.consistency = res.consistency;
        phase("took", { index: index, response: res });
        index++;
        error = "";
        inconsistent = 0;
      }
      if (person.origin === "voice" && !person.confirmed) {
        // a wizard enrolment counts as "Behalten" (SPEC 5.4)
        await deps.fetchJSON(base + "/confirm", { method: "POST", body: { keep: true } });
      }
      result.ok = true;
      return result;
    } finally {
      deps.setEnrolling(false);
    }
  }

  /*
   * runIdentify(deps, onLevel) → {short:true} | the /api/voiceid/identify
   * response. Diagnostics only; also holds setEnrolling so the test sentence
   * is not sent to chat.
   */
  async function runIdentify(deps, onLevel) {
    deps.setEnrolling(true);
    try {
      if (deps.stopSpeech) await deps.stopSpeech();
      var rec = await deps.record(IDENTIFY_MS, function (l) {
        if (onLevel) onLevel(levelToPercent(l));
      });
      var voice = rec && rec.voice;
      if (!voice || !voice.emb || voice.short) return { short: true, sec: voiceSec(rec) };
      return await deps.fetchJSON("/api/voiceid/identify", { method: "POST", body: { voice: voice } });
    } finally {
      if (onLevel) onLevel(0);
      deps.setEnrolling(false);
    }
  }

  return {
    STICKY_MS: STICKY_MS,
    RECORD_MS: RECORD_MS,
    MIN_TAKE_SEC: MIN_TAKE_SEC,
    SENTENCES: SENTENCES,
    STRINGS: STRINGS,
    VOICE_SETTINGS: VOICE_SETTINGS,
    VOICE_TOGGLES: VOICE_TOGGLES,
    format: format,
    makeTr: makeTr,
    fmtNum: fmtNum,
    isPersonId: isPersonId,
    shouldStick: shouldStick,
    payloadSpeaker: payloadSpeaker,
    normalizeManual: normalizeManual,
    validateName: validateName,
    nameTaken: nameTaken,
    personBadges: personBadges,
    deleteSummary: deleteSummary,
    parseTime: parseTime,
    formatLastSeen: formatLastSeen,
    formatExpiry: formatExpiry,
    formatScore: formatScore,
    levelToPercent: levelToPercent,
    errorKey: errorKey,
    runEnrolment: runEnrolment,
    runIdentify: runIdentify,
  };
});

/* ====================================================================== */
/* Browser UI                                                              */
/* ====================================================================== */
(function () {
  "use strict";
  if (typeof window === "undefined" || typeof document === "undefined") return;
  var H = window.Hermes;
  var P = window.HermesPeople;
  if (!H || !P) return; // hermes.js missing: the page works without people features

  var MANUAL_KEY = "hermes-speaker-manual";
  var MIGRATION_LATER_KEY = "hermes-migration-later";
  var UNLOCK_TTL = 9.5 * 60 * 1000; // server cookie: 10 min sliding

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
  var tr = typeof H.strings === "function" ? H.strings(P.STRINGS) : P.makeTr(P.STRINGS, lang);

  // ---------------------------------------------------------------- state

  var state = {
    ready: false,
    people: [],
    peopleLoaded: false,
    voice: null, // {model, ort, dim, threshold, margin, minSec}
    manual: "",
    last: null, // last speaker event {id, name, source, score, at}
    unlockedAt: 0,
    migrationAsked: false,
    loadSeq: 0,
  };
  var views = { people: null, voice: null, chip: null };

  function lsGet(key) {
    try {
      return window.localStorage.getItem(key) || "";
    } catch (e) {
      return "";
    }
  }
  function lsSet(key, v) {
    try {
      if (v) window.localStorage.setItem(key, v);
      else window.localStorage.removeItem(key);
    } catch (e) {
      /* private mode: the choice lives for this page only */
    }
  }
  function ssGet(key) {
    try {
      return window.sessionStorage.getItem(key) || "";
    } catch (e) {
      return "";
    }
  }
  function ssSet(key, v) {
    try {
      window.sessionStorage.setItem(key, v);
    } catch (e) {
      /* ignore */
    }
  }

  state.manual = lsGet(MANUAL_KEY);

  function settings() {
    try {
      return (api().settings && api().settings()) || {};
    } catch (e) {
      return {};
    }
  }
  function lockOn() {
    return !!settings().personalLock;
  }
  function knownIds() {
    return state.peopleLoaded
      ? state.people.map(function (p) {
          return p.id;
        })
      : null;
  }
  function personById(id) {
    for (var i = 0; i < state.people.length; i++) if (state.people[i].id === id) return state.people[i];
    return null;
  }

  function whenReady(fn) {
    if (H.ready) fn();
    else
      H.on("ready", function once() {
        if (H.off) H.off("ready", once);
        fn();
      });
  }

  function toast(msg, opts) {
    opts = opts || {};
    if (opts.ms == null && opts.timeout != null) opts.ms = opts.timeout; // SPEC 5.2 name
    if (api().toast) return api().toast(msg, opts);
    return { close: function () {} };
  }

  function errText(err) {
    var key = P.errorKey(err);
    return tr(key, { msg: (err && err.message) || "?" });
  }

  // ---------------------------------------------------------------- DOM helper

  function el(tag, attrs, kids) {
    var e = document.createElement(tag);
    if (attrs) {
      for (var k in attrs) {
        if (!Object.prototype.hasOwnProperty.call(attrs, k)) continue;
        var v = attrs[k];
        if (v == null || v === false) continue;
        if (k === "class") e.className = v;
        else if (k === "text") e.textContent = v;
        else if (k === "html") e.innerHTML = v; // only for our own static SVG
        else if (k.slice(0, 2) === "on" && typeof v === "function") e.addEventListener(k.slice(2), v);
        else if (k === "hidden") e.hidden = !!v;
        else if (k === "disabled") e.disabled = !!v;
        else if (k === "checked") e.checked = !!v;
        else if (k === "value") e.value = v;
        else e.setAttribute(k, v === true ? "" : v);
      }
    }
    append(e, kids);
    return e;
  }
  function append(parent, kids) {
    if (kids == null) return;
    if (!Array.isArray(kids)) kids = [kids];
    for (var i = 0; i < kids.length; i++) {
      var c = kids[i];
      if (c == null || c === false) continue;
      parent.appendChild(typeof c === "string" ? document.createTextNode(c) : c);
    }
  }
  function clear(node) {
    while (node && node.firstChild) node.removeChild(node.firstChild);
  }

  /*
   * Inputs in our settings sections that are not settings (name field, PIN)
   * must not trigger app.js's live preview / unsaved badge.
   */
  function isolateInputs(root) {
    ["input", "change"].forEach(function (type) {
      root.addEventListener(type, function (e) {
        var t = e.target;
        if (t && t.closest && !t.closest("[data-setting]")) e.stopPropagation();
      });
    });
  }

  // ---------------------------------------------------------------- dialogs

  /* Keep Esc inside our dialogs: app.js would otherwise also close the settings panel. */
  function guardEsc(dlg) {
    dlg.addEventListener("keydown", function (e) {
      if (e.key === "Escape") e.stopPropagation();
    });
  }

  function makeDialog(cls, labelId) {
    var d = el("dialog", { class: "pp-dialog " + (cls || ""), "aria-labelledby": labelId });
    guardEsc(d);
    document.body.appendChild(d);
    return d;
  }

  function closeDialog(d) {
    try {
      if (d.open) d.close();
    } catch (e) {
      /* ignore */
    }
    if (d.parentNode) d.parentNode.removeChild(d);
  }

  function showModal(d) {
    try {
      d.showModal();
    } catch (e) {
      d.setAttribute("open", "");
    }
  }

  /*
   * confirmDialog({title, body:[nodes], buttons:[{label, value, kind, focus}]})
   * → Promise<value|null>. Esc / backdrop cancel resolve null.
   */
  var dlgSeq = 0;
  function choiceDialog(opts) {
    return new Promise(function (resolve) {
      var id = "pp-dlg-" + ++dlgSeq;
      var d = makeDialog(opts.cls, id);
      var done = false;
      function finish(v) {
        if (done) return;
        done = true;
        closeDialog(d);
        resolve(v);
      }
      var focusBtn = null;
      var btns = (opts.buttons || []).map(function (b) {
        var cls = b.kind === "primary" ? "primary" : b.kind === "danger" ? "pp-danger" : b.kind === "ghost" ? "ghost" : "";
        var btn = el("button", {
          type: "button",
          class: cls,
          text: b.label,
          onclick: function () {
            finish(b.value);
          },
        });
        if (b.focus) focusBtn = btn;
        return btn;
      });
      append(d, [
        el("h2", { id: id, class: "pp-dlg-title", text: opts.title }),
        el("div", { class: "pp-dlg-body" }, opts.body || []),
        el("div", { class: "pp-dlg-actions" }, btns),
      ]);
      d.addEventListener("cancel", function (e) {
        e.preventDefault();
        finish(null);
      });
      showModal(d);
      if (focusBtn) focusBtn.focus();
    });
  }

  // ---------------------------------------------------------------- API + lock

  function markUnlocked() {
    if (lockOn()) state.unlockedAt = Date.now();
  }
  function probablyUnlocked() {
    return !lockOn() || Date.now() - state.unlockedAt < UNLOCK_TTL;
  }

  var unlockPending = null;

  /*
   * requestUnlock() → Promise<bool>. Shared PIN prompt for the personal
   * lock (SPEC 4.10): POST /api/unlock sets the HttpOnly cookie.
   */
  function requestUnlock() {
    if (unlockPending) return unlockPending;
    unlockPending = new Promise(function (resolve) {
      var id = "pp-unlock-" + ++dlgSeq;
      var d = makeDialog("pp-unlock", id);
      var input = el("input", {
        type: "password",
        inputmode: "numeric",
        autocomplete: "off",
        maxlength: "12",
        class: "pp-pin",
        "aria-label": tr("unlockPin"),
      });
      var msg = el("p", { class: "pp-dlg-error", role: "alert" });
      var go = el("button", { type: "submit", class: "primary", text: tr("unlockGo") });
      var form = el("form", { class: "pp-unlock-form" }, [
        el("h2", { id: id, class: "pp-dlg-title", text: tr("unlockTitle") }),
        el("p", { class: "pp-dlg-text", text: tr("unlockBody") }),
        el("label", { class: "pp-field" }, [tr("unlockPin"), input]),
        msg,
        el("div", { class: "pp-dlg-actions" }, [
          el("button", {
            type: "button",
            class: "ghost",
            text: tr("cancel"),
            onclick: function () {
              finish(false);
            },
          }),
          go,
        ]),
      ]);
      d.appendChild(form);
      var done = false;
      function finish(ok) {
        if (done) return;
        done = true;
        closeDialog(d);
        unlockPending = null;
        if (ok) {
          state.unlockedAt = Date.now();
          H.emit("people:unlocked", {});
        }
        resolve(ok);
      }
      form.addEventListener("submit", function (e) {
        e.preventDefault();
        var pin = String(input.value || "").replace(/\s+/g, "");
        if (!/^\d{4,12}$/.test(pin)) {
          msg.textContent = tr("unlockWrong");
          input.select();
          return;
        }
        go.disabled = true;
        msg.textContent = "";
        api()
          .fetchJSON("/api/unlock", { method: "POST", body: { pin: pin } })
          .then(
            function () {
              finish(true);
            },
            function (err) {
              go.disabled = false;
              var wait = err && (err.status === 429 || /lockout|too_many|wait/.test(err.code || ""));
              msg.textContent = wait ? tr("unlockWait") : err && !err.status ? tr("err_network") : tr("unlockWrong");
              input.value = "";
              input.focus();
            }
          );
      });
      d.addEventListener("cancel", function (e) {
        e.preventDefault();
        finish(false);
      });
      showModal(d);
      input.focus();
    });
    return unlockPending;
  }

  function lockNow() {
    return api()
      .fetchJSON("/api/unlock", { method: "DELETE" })
      .catch(function () {
        /* already locked */
      })
      .then(function () {
        state.unlockedAt = 0;
        toast(tr("lockedToast"), { key: "pp-lock" });
        H.emit("people:locked", {});
      });
  }

  /* fetchJSON with one PIN prompt + retry on 403 locked. */
  function call(url, opts, retried) {
    return api()
      .fetchJSON(url, opts || {})
      .then(
        function (r) {
          markUnlocked();
          return r;
        },
        function (err) {
          if (err && err.code === "locked" && !retried) {
            state.unlockedAt = 0;
            return requestUnlock().then(function (ok) {
              if (!ok) throw err;
              return call(url, opts, true);
            });
          }
          throw err;
        }
      );
  }

  // ---------------------------------------------------------------- data

  function loadPeople() {
    if (!api().fetchJSON) return Promise.resolve();
    var seq = ++state.loadSeq;
    return api()
      .fetchJSON("/api/people")
      .then(
        function (res) {
          if (seq !== state.loadSeq) return;
          state.people = (res && Array.isArray(res.people) ? res.people : []).filter(function (p) {
            return p && P.isPersonId(p.id);
          });
          state.voice = (res && res.voice) || null;
          state.peopleLoaded = true;
          var m = P.normalizeManual(state.manual, state.people);
          if (m !== state.manual) {
            state.manual = m;
            lsSet(MANUAL_KEY, m);
          }
          H.emit("people:list", state.people.slice());
          renderAll();
          maybeAskMigration();
        },
        function (err) {
          if (seq !== state.loadSeq) return;
          if (views.people) views.people.setError(errText(err));
        }
      );
  }

  // ---------------------------------------------------------------- speech guard

  /* Stop TTS before recording (SPEC 5.4). */
  function stopSpeech() {
    var a = api();
    // app.js provides Hermes.api.stopSpeaking (an extra beyond SPEC 5.2).
    if (typeof a.stopSpeaking === "function") {
      try {
        a.stopSpeaking();
      } catch (e) {
        /* ignore */
      }
    } else if (typeof a.stop === "function") {
      try {
        a.stop();
      } catch (e) {
        /* ignore */
      }
    }
    if (typeof a.isSpeaking !== "function") return Promise.resolve();
    return new Promise(function (resolve) {
      var start = Date.now();
      (function poll() {
        var busy = false;
        try {
          busy = !!a.isSpeaking();
        } catch (e) {
          busy = false;
        }
        if (!busy || Date.now() - start > 10000) resolve();
        else setTimeout(poll, 150);
      })();
    });
  }

  function setEnrolling(on) {
    var a = api();
    if (typeof a.setEnrolling === "function") {
      try {
        a.setEnrolling(!!on);
      } catch (e) {
        /* ignore */
      }
    }
  }

  function voiceIdObj() {
    return window.VoiceID || null;
  }

  function voiceAvailability() {
    var V = voiceIdObj();
    var secure = typeof window.isSecureContext === "boolean" ? window.isSecureContext : true;
    try {
      if (api().isSecure) secure = !!api().isSecure();
    } catch (e) {
      /* keep */
    }
    if (!secure) return "insecure";
    var st = null;
    try {
      st = api().status && api().status();
    } catch (e) {
      st = null;
    }
    var vf = st && st.features && st.features.voiceId;
    if (vf && vf.assets === false) return "noAssets";
    if (!V || typeof V.available !== "function") return "noAssets";
    try {
      if (!V.available()) return vf ? "noAssets" : "unavailable";
    } catch (e) {
      return "unavailable";
    }
    if (settings().voiceIdEnabled === false) return "off";
    return "ok";
  }

  function canRecord() {
    var a = voiceAvailability();
    return a === "ok" || a === "off"; // recording a profile works even with matching turned off
  }

  function enableVoice() {
    var V = voiceIdObj();
    if (!V || typeof V.enable !== "function") return Promise.reject(new Error("unavailable"));
    return Promise.resolve(V.enable());
  }

  // ---------------------------------------------------------------- level meter

  function meter() {
    var bar = el("span", { class: "pp-meter-bar" });
    var root = el(
      "div",
      { class: "pp-meter", role: "meter", "aria-label": tr("wizLevel"), "aria-valuemin": "0", "aria-valuemax": "100", "aria-valuenow": "0" },
      bar
    );
    return {
      el: root,
      set: function (pct) {
        var p = Math.max(0, Math.min(100, pct | 0));
        bar.style.transform = "scaleX(" + p / 100 + ")";
        root.setAttribute("aria-valuenow", String(p));
        root.classList.toggle("is-hot", p > 85);
      },
    };
  }

  // ---------------------------------------------------------------- enrolment wizard

  var wizardOpen = false;

  function openWizard(person) {
    if (wizardOpen || !person) return;
    if (!canRecord()) {
      var why = voiceAvailability();
      toast(tr(why === "insecure" ? "insecure" : why === "noAssets" ? "noAssets" : "wizUnavailable"), { type: "warn", key: "pp-wiz" });
      return;
    }
    wizardOpen = true;
    var name = person.name;
    var id = "pp-wiz-" + ++dlgSeq;
    var d = makeDialog("pp-wizard", id);
    var dots = el(
      "ol",
      { class: "pp-steps", "aria-hidden": "true" },
      [0, 1, 2].map(function () {
        return el("li");
      })
    );
    var stepLabel = el("p", { class: "pp-step-label" });
    var sentence = el("blockquote", { class: "pp-sentence", lang: lang() });
    var m = meter();
    var status = el("p", { class: "pp-wiz-status", role: "status", "aria-live": "polite" });
    var errLine = el("p", { class: "pp-dlg-error", role: "alert" });
    var recordBtn = el("button", { type: "button", class: "primary pp-record", text: tr("wizStart") });
    var cancelBtn = el("button", { type: "button", class: "ghost", text: tr("cancel") });
    var intro = el("div", { class: "pp-wiz-intro" }, [
      el("p", { class: "pp-dlg-text", text: tr("wizIntro") }),
      el("p", { class: "pp-hint-strong", text: tr("wizRoom") }),
      (person.samples || 0) > 0 ? el("p", { class: "pp-dlg-text pp-warn", text: tr("wizReplace") }) : null,
    ]);
    append(d, [
      el("h2", { id: id, class: "pp-dlg-title", text: tr("wizTitle", { name: name }) }),
      intro,
      el("div", { class: "pp-take" }, [dots, stepLabel, sentence, m.el, status, errLine]),
      el("div", { class: "pp-dlg-actions" }, [cancelBtn, recordBtn]),
    ]);

    var cancelled = false;
    var waiter = null;
    function setDots(index, doneUpTo) {
      var lis = dots.children;
      for (var i = 0; i < lis.length; i++) {
        lis[i].className = i < doneUpTo ? "done" : i === index ? "current" : "";
      }
    }
    var doneCount = 0;
    cancelBtn.addEventListener("click", function () {
      cancelled = true;
      if (waiter) waiter(false);
      else finishUI(tr("wizCancelled"));
    });
    recordBtn.addEventListener("click", function () {
      if (waiter) waiter(true);
    });
    d.addEventListener("cancel", function (e) {
      e.preventDefault();
      cancelBtn.click();
    });

    var finished = false;
    function finishUI(text, ok) {
      if (finished) return;
      finished = true;
      status.textContent = text || "";
      errLine.textContent = "";
      recordBtn.hidden = true;
      cancelBtn.textContent = tr("close");
      cancelBtn.className = ok ? "primary" : "";
      cancelBtn.onclick = function () {
        closeDialog(d);
      };
      d.addEventListener("cancel", function () {
        closeDialog(d);
      });
      cancelBtn.focus();
      wizardOpen = false;
      loadPeople();
    }

    var ui = {
      awaitTake: function (info) {
        setDots(info.index, info.error === "restarted" ? 0 : doneCount);
        if (info.error === "restarted") doneCount = 0;
        stepLabel.textContent = tr("wizStep", { i: info.index + 1, n: info.total });
        sentence.textContent = lang() === "de" ? "„" + info.sentence + "“" : "“" + info.sentence + "”";
        status.textContent = "";
        errLine.textContent =
          info.error === "short"
            ? tr("wizShort")
            : info.error === "inconsistent"
            ? tr("wizInconsistent")
            : info.error === "restarted"
            ? tr("wizRestarted")
            : "";
        recordBtn.textContent = info.error ? tr("wizAgain") : tr("wizStart");
        recordBtn.disabled = false;
        recordBtn.focus();
        return new Promise(function (resolve) {
          waiter = function (v) {
            waiter = null;
            resolve(v && !cancelled);
          };
        });
      },
      onLevel: function (pct) {
        m.set(pct);
      },
      onPhase: function (phase, info) {
        if (phase === "recording") {
          recordBtn.disabled = true;
          errLine.textContent = "";
          status.textContent = tr("wizRecording");
          d.classList.add("is-recording");
        } else {
          d.classList.remove("is-recording");
        }
        if (phase === "saving") status.textContent = tr("wizSaving");
        if (phase === "took") {
          doneCount = info.index + 1;
          setDots(info.index + 1, doneCount);
          status.textContent = tr("wizTakeOk");
        }
      },
      cancelled: function () {
        return cancelled;
      },
    };

    showModal(d);
    recordBtn.disabled = true;
    status.textContent = "";
    enableVoice()
      .then(function () {
        var V = voiceIdObj();
        return P.runEnrolment(
          {
            fetchJSON: function (url, opts) {
              return call(url, opts);
            },
            record: function (ms, onLevel) {
              return V.record(ms, onLevel);
            },
            setEnrolling: setEnrolling,
            stopSpeech: stopSpeech,
            lang: lang(),
          },
          person,
          ui
        );
      })
      .then(
        function (res) {
          d.classList.remove("is-recording");
          if (!res || res.cancelled) {
            finishUI(tr("wizCancelled"));
            if (cancelled) closeDialog(d);
            return;
          }
          setDots(3, 3);
          sentence.textContent = "";
          m.el.hidden = true;
          stepLabel.textContent = "";
          finishUI(res.provisional ? tr("wizDoneProvisional") : tr("wizDone", { name: name }), true);
          H.emit("people:enrolled", { id: person.id, samples: res.samples });
        },
        function (err) {
          d.classList.remove("is-recording");
          var text;
          if (err && err.name === "NotAllowedError") text = tr("wizMicDenied");
          else if (err && err.message === "unavailable") text = tr("wizUnavailable");
          else if (err && (err.status || err.code)) text = errText(err);
          else if (err && /permission|denied|notallowed/i.test(String(err.message || err))) text = tr("wizMicDenied");
          else text = tr("wizUnavailable");
          finishUI("");
          errLine.textContent = text;
        }
      );
  }

  // ---------------------------------------------------------------- identify

  function openIdentify() {
    if (wizardOpen) return;
    if (!canRecord()) {
      var why = voiceAvailability();
      toast(tr(why === "insecure" ? "insecure" : why === "noAssets" ? "noAssets" : "wizUnavailable"), { type: "warn", key: "pp-id" });
      return;
    }
    wizardOpen = true;
    var id = "pp-id-" + ++dlgSeq;
    var d = makeDialog("pp-identify", id);
    var m = meter();
    var status = el("p", { class: "pp-wiz-status", role: "status", "aria-live": "polite", text: tr("idIntro") });
    var result = el("div", { class: "pp-id-result" });
    var startBtn = el("button", { type: "button", class: "primary", text: tr("idStart") });
    var closeBtn = el("button", { type: "button", class: "ghost", text: tr("close") });
    var busy = false;
    append(d, [
      el("h2", { id: id, class: "pp-dlg-title", text: tr("idTitle") }),
      status,
      m.el,
      result,
      el("div", { class: "pp-dlg-actions" }, [closeBtn, startBtn]),
    ]);
    function close() {
      if (busy) return;
      wizardOpen = false;
      closeDialog(d);
    }
    closeBtn.addEventListener("click", close);
    d.addEventListener("cancel", function (e) {
      e.preventDefault();
      close();
    });
    startBtn.addEventListener("click", function () {
      if (busy) return;
      busy = true;
      startBtn.disabled = true;
      closeBtn.disabled = true;
      clear(result);
      status.textContent = tr("idListening");
      d.classList.add("is-recording");
      enableVoice()
        .then(function () {
          var V = voiceIdObj();
          return P.runIdentify(
            {
              fetchJSON: function (url, opts) {
                return call(url, opts);
              },
              record: function (ms, onLevel) {
                return V.record(ms, onLevel);
              },
              setEnrolling: setEnrolling,
              stopSpeech: stopSpeech,
            },
            m.set
          );
        })
        .then(
          function (res) {
            renderIdentify(res, status, result);
          },
          function (err) {
            status.textContent = err && (err.status || err.code) ? errText(err) : tr("wizMicDenied");
          }
        )
        .then(function () {
          busy = false;
          d.classList.remove("is-recording");
          startBtn.disabled = false;
          closeBtn.disabled = false;
          startBtn.textContent = tr("wizAgain");
          startBtn.focus();
        });
    });
    showModal(d);
    startBtn.focus();
  }

  function renderIdentify(res, status, box) {
    var L = lang();
    clear(box);
    if (!res || res.short) {
      status.textContent = tr("idShort");
      return;
    }
    var sp = res.speaker || {};
    if (sp.id) status.textContent = tr("idResult", { name: sp.name || sp.id }) + " (" + P.formatScore(sp.score, L) + ")";
    else if (sp.source === "uncertain") status.textContent = tr("idUncertain");
    else status.textContent = tr("idGuest") + (typeof sp.score === "number" ? " (" + P.formatScore(sp.score, L) + ")" : "");
    var scores = Array.isArray(res.scores) ? res.scores.slice() : [];
    if (!scores.length) {
      box.appendChild(el("p", { class: "pp-dlg-text", text: tr("idNoPeople") }));
      return;
    }
    scores.sort(function (a, b) {
      return (b.score || 0) - (a.score || 0);
    });
    var th = state.voice && typeof state.voice.threshold === "number" ? state.voice.threshold : settings().voiceIdThreshold || 0.6;
    var margin = state.voice && typeof state.voice.margin === "number" ? state.voice.margin : settings().voiceIdMargin || 0.05;
    var list = el("ul", { class: "pp-scores", "aria-label": tr("idScores") });
    scores.forEach(function (s) {
      var v = Math.max(0, Math.min(1, Number(s.score) || 0));
      var bar = el("span", { class: "pp-score-bar" + (s.id && s.id === sp.id ? " is-match" : "") });
      bar.style.width = (v * 100).toFixed(1) + "%";
      var track = el("span", { class: "pp-score-track" }, [bar, el("span", { class: "pp-score-th" })]);
      track.lastChild.style.left = (th * 100).toFixed(1) + "%";
      list.appendChild(
        el("li", null, [
          el("span", { class: "pp-score-name", text: s.name || s.id }),
          track,
          el("span", { class: "pp-score-val", text: P.formatScore(s.score, L) }),
        ])
      );
    });
    box.appendChild(list);
    box.appendChild(
      el("p", { class: "pp-dlg-text pp-muted", text: tr("idThreshold", { th: P.fmtNum(th, 2, L), margin: P.fmtNum(margin, 2, L) }) })
    );
  }

  // ---------------------------------------------------------------- people actions

  function doDelete(p) {
    var s = P.deleteSummary(p, tr);
    return choiceDialog({
      cls: "pp-confirm",
      title: s.title,
      body: [
        el("p", { class: "pp-dlg-text pp-strong", text: s.summary }),
        el("p", { class: "pp-dlg-text", text: tr("deleteLead") }),
        el(
          "ul",
          { class: "pp-dlg-list" },
          s.items.map(function (t) {
            return el("li", { text: t });
          })
        ),
        el("p", { class: "pp-dlg-text pp-muted", text: s.keeps }),
        el("p", { class: "pp-dlg-text pp-warn", text: s.final }),
      ],
      buttons: [
        { label: tr("cancel"), value: false, kind: "ghost", focus: true },
        { label: tr("deleteConfirm"), value: true, kind: "danger" },
      ],
    }).then(function (ok) {
      if (!ok) return;
      return call("/api/people/" + encodeURIComponent(p.id), { method: "DELETE" }).then(
        function () {
          if (state.manual === p.id) setManual("");
          toast(tr("deletedToast", { name: p.name }), { type: "success", key: "pp-del" });
          return loadPeople();
        },
        function (err) {
          toast(errText(err), { type: "error", key: "pp-err" });
        }
      );
    });
  }

  function doDeleteVoice(p) {
    return choiceDialog({
      cls: "pp-confirm",
      title: tr("deleteVoiceTitle", { name: p.name }),
      body: [el("p", { class: "pp-dlg-text", text: tr("deleteVoiceBody", { name: p.name, n: p.samples || 0 }) })],
      buttons: [
        { label: tr("cancel"), value: false, kind: "ghost", focus: true },
        { label: tr("deleteVoiceConfirm"), value: true, kind: "danger" },
      ],
    }).then(function (ok) {
      if (!ok) return;
      return call("/api/people/" + encodeURIComponent(p.id) + "/voiceprints", { method: "DELETE" }).then(
        function () {
          toast(tr("voiceDeletedToast", { name: p.name }), { type: "success", key: "pp-del" });
          return loadPeople();
        },
        function (err) {
          toast(errText(err), { type: "error", key: "pp-err" });
        }
      );
    });
  }

  function confirmPerson(p, keep) {
    return call("/api/people/" + encodeURIComponent(p.id) + "/confirm", { method: "POST", body: { keep: !!keep } }).then(
      function () {
        toast(tr(keep ? "keptToast" : "removedToast", { name: p.name }), { type: "success", key: "pp-confirm-" + p.id });
        return loadPeople();
      },
      function (err) {
        toast(errText(err), { type: "error", key: "pp-err" });
      }
    );
  }

  // ---------------------------------------------------------------- People section

  function mountPeople(root) {
    root.classList.add("pp");
    isolateInputs(root);
    var editing = ""; // id being renamed
    var errorText = "";

    var view = {
      render: render,
      setError: function (t) {
        errorText = t || "";
        render();
      },
    };
    views.people = view;

    function addForm() {
      var input = el("input", {
        type: "text",
        class: "pp-name-input",
        maxlength: "60",
        autocomplete: "off",
        spellcheck: "false",
        placeholder: tr("namePlaceholder"),
        "aria-label": tr("nameLabel"),
      });
      var btn = el("button", { type: "submit", class: "small", text: tr("add") });
      var form = el("form", { class: "pp-add" }, [input, btn]);
      form.addEventListener("submit", function (e) {
        e.preventDefault();
        var v = P.validateName(input.value);
        if (!v.ok) return view.setError(tr("err_" + v.error));
        if (P.nameTaken(v.name, state.people)) return view.setError(tr("err_exists"));
        btn.disabled = true;
        btn.textContent = tr("adding");
        call("/api/people", { method: "POST", body: { name: v.name } }).then(
          function () {
            errorText = "";
            input.value = "";
            return loadPeople();
          },
          function (err) {
            btn.disabled = false;
            btn.textContent = tr("add");
            view.setError(errText(err));
          }
        );
      });
      return form;
    }

    function renameForm(p) {
      var input = el("input", {
        type: "text",
        class: "pp-name-input",
        maxlength: "60",
        value: p.name,
        autocomplete: "off",
        spellcheck: "false",
        "aria-label": tr("nameLabel"),
      });
      var form = el("form", { class: "pp-rename" }, [
        input,
        el("button", { type: "submit", class: "small primary", text: tr("save") }),
        el("button", {
          type: "button",
          class: "small ghost",
          text: tr("cancel"),
          onclick: function () {
            editing = "";
            errorText = "";
            render();
          },
        }),
      ]);
      input.addEventListener("keydown", function (e) {
        if (e.key === "Escape") {
          e.stopPropagation();
          e.preventDefault();
          editing = "";
          render();
        }
      });
      form.addEventListener("submit", function (e) {
        e.preventDefault();
        var v = P.validateName(input.value);
        if (!v.ok) return view.setError(tr("err_" + v.error));
        if (v.name === p.name) {
          editing = "";
          return render();
        }
        if (P.nameTaken(v.name, state.people, p.id)) return view.setError(tr("err_exists"));
        call("/api/people/" + encodeURIComponent(p.id), { method: "PATCH", body: { name: v.name } }).then(
          function () {
            editing = "";
            errorText = "";
            toast(tr("renamedToast", { name: v.name }), { type: "success", key: "pp-rename" });
            return loadPeople();
          },
          function (err) {
            view.setError(errText(err));
          }
        );
      });
      setTimeout(function () {
        input.focus();
        input.select();
      }, 0);
      return form;
    }

    function personItem(p, now, L) {
      var badges = P.personBadges(p).map(function (b) {
        var key = b === "unconfirmed" ? "badgeUnconfirmed" : b === "provisional" ? "badgeProvisional" : "badgeOldModel";
        return el("span", { class: "pp-badge pp-badge-" + b, text: tr(key) });
      });
      var samples = p.samples || 0;
      var meta = [];
      meta.push(samples === 0 ? tr("noVoice") : samples === 1 ? tr("samples1") : tr("samples", { n: samples }));
      var mem = p.memories || 0;
      if (mem > 0) meta.push(mem === 1 ? tr("memories1") : tr("memories", { n: mem }));
      var seen = P.formatLastSeen(p.lastSeen, now, L);
      meta.push(seen ? tr("lastSeen", { when: seen }) : tr("neverSeen"));
      if (p.origin === "voice" && !p.confirmed) {
        var exp = P.formatExpiry(p.expiresAt, now, L);
        if (exp) meta.push(tr("expires", { when: exp }));
      }

      var actions = [];
      if (p.origin === "voice" && !p.confirmed) {
        actions.push(
          el("button", {
            type: "button",
            class: "small",
            text: tr("keep"),
            onclick: function () {
              confirmPerson(p, true);
            },
          })
        );
      }
      actions.push(
        el("button", {
          type: "button",
          class: "small" + (samples === 0 || p.voiceModelOk === false || p.provisional ? " pp-accent" : ""),
          text: samples > 0 ? tr("rerecord") : tr("recordVoice"),
          disabled: !canRecord(),
          onclick: function () {
            openWizard(p);
          },
        })
      );
      actions.push(
        el("button", {
          type: "button",
          class: "small ghost",
          text: tr("rename"),
          onclick: function () {
            editing = p.id;
            errorText = "";
            render();
          },
        })
      );
      if (samples > 0) {
        actions.push(
          el("button", {
            type: "button",
            class: "small ghost",
            text: tr("deleteVoice"),
            onclick: function () {
              doDeleteVoice(p);
            },
          })
        );
      }
      actions.push(
        el("button", {
          type: "button",
          class: "small ghost pp-danger-text",
          text: tr("del"),
          onclick: function () {
            doDelete(p);
          },
        })
      );

      var hint = null;
      if (samples > 0 && p.voiceModelOk === false) hint = el("p", { class: "pp-item-hint", text: tr("oldModelHint") });
      else if (p.origin === "voice" && !p.confirmed) hint = el("p", { class: "pp-item-hint", text: tr("unconfirmedHint") });

      var initial = (p.name || "?").charAt(0).toLocaleUpperCase();
      return el("li", { class: "pp-person" + (p.origin === "voice" && !p.confirmed ? " is-unconfirmed" : "") }, [
        el("div", { class: "pp-head" }, [
          el("span", { class: "pp-avatar" + (samples > 0 ? " has-voice" : ""), "aria-hidden": "true", text: initial }),
          editing === p.id ? renameForm(p) : el("span", { class: "pp-name", text: p.name }),
          el("span", { class: "pp-badges" }, badges),
        ]),
        el("p", { class: "pp-meta", text: meta.join(" · ") }),
        hint,
        el("div", { class: "pp-actions" }, actions),
      ]);
    }

    function render() {
      clear(root);
      var L = lang();
      var now = Date.now();
      if (lockOn()) {
        var unlocked = probablyUnlocked() && state.unlockedAt > 0;
        root.appendChild(
          el("div", { class: "pp-lockbar" + (unlocked ? " is-open" : "") }, [
            el("span", { class: "pp-lock-icon", "aria-hidden": "true", html: unlocked ? ICON_UNLOCK : ICON_LOCK }),
            el("span", { class: "pp-lock-text", text: unlocked ? "" : tr("lockedBar") }),
            unlocked
              ? el("button", { type: "button", class: "small ghost", text: tr("lockNow"), onclick: lockNow })
              : el("button", {
                  type: "button",
                  class: "small",
                  text: tr("unlockBtn"),
                  onclick: function () {
                    requestUnlock();
                  },
                }),
          ])
        );
      }
      root.appendChild(el("p", { class: "pp-disclaimer", text: tr("disclaimer") }));
      var av = voiceAvailability();
      var statusText = "";
      if (av === "insecure") statusText = tr("insecure");
      else if (av === "noAssets") statusText = tr("noAssets");
      else if (av === "off") statusText = tr("voiceOff");
      else if (state.people.length) {
        var enrolled = state.people.filter(function (p) {
          return (p.samples || 0) > 0 && p.voiceModelOk !== false;
        }).length;
        statusText = tr("voiceStatus", { enrolled: enrolled, people: state.people.length });
      }
      if (statusText) root.appendChild(el("p", { class: "hint pp-status" + (av !== "ok" ? " warn" : ""), text: statusText }));

      if (!state.peopleLoaded) {
        root.appendChild(el("p", { class: "pp-empty pp-loading", text: "…" }));
      } else if (!state.people.length) {
        root.appendChild(el("p", { class: "pp-empty", text: tr("empty") }));
      } else {
        var list = el("ul", { class: "pp-list" });
        state.people.forEach(function (p) {
          list.appendChild(personItem(p, now, L));
        });
        root.appendChild(list);
      }
      root.appendChild(addForm());
      if (errorText) root.appendChild(el("p", { class: "pp-error", role: "alert", text: errorText }));
      var withVoice = state.people.some(function (p) {
        return (p.samples || 0) > 0;
      });
      if (withVoice) {
        root.appendChild(
          el("div", { class: "pp-tools" }, [
            el("button", { type: "button", class: "small", text: tr("testRecognition"), disabled: !canRecord(), onclick: openIdentify }),
          ])
        );
      }
    }

    render();
    whenReady(loadPeople);
  }

  // ---------------------------------------------------------------- Voice ID tuning section

  function mountVoiceTuning(root) {
    root.classList.add("pp", "pp-voice");
    var outputs = {};
    var inputs = {};

    function fill() {
      var s = settings();
      P.VOICE_TOGGLES.forEach(function (t) {
        var i = inputs[t.key];
        if (i) i.checked = s[t.key] == null ? t.def : !!s[t.key];
      });
      P.VOICE_SETTINGS.forEach(function (r) {
        var i = inputs[r.key];
        if (!i) return;
        var v = typeof s[r.key] === "number" && isFinite(s[r.key]) ? s[r.key] : r.def;
        i.value = String(v);
        showValue(r);
      });
    }
    function showValue(r) {
      var i = inputs[r.key];
      var o = outputs[r.key];
      if (i && o) o.textContent = P.fmtNum(Number(i.value), r.digits, lang());
    }

    function render() {
      clear(root);
      outputs = {};
      inputs = {};
      var labels = {
        voiceIdEnabled: ["vEnabled", ""],
        voiceIdThreshold: ["vThreshold", "vThresholdHint"],
        voiceIdMargin: ["vMargin", "vMarginHint"],
        voiceIdMinSec: ["vMinSec", "vMinSecHint"],
        voiceIdAdapt: ["vAdapt", "vAdaptHint"],
      };
      function toggle(t) {
        var input = el("input", { type: "checkbox", "data-setting": t.key, id: "s_" + t.key });
        inputs[t.key] = input;
        var out = [el("label", { class: "check" }, [input, " " + tr(labels[t.key][0])])];
        if (labels[t.key][1]) out.push(el("p", { class: "hint", text: tr(labels[t.key][1]) }));
        return out;
      }
      function range(r) {
        var id = "s_" + r.key;
        var input = el("input", {
          type: "range",
          id: id,
          "data-setting": r.key,
          min: String(r.min),
          max: String(r.max),
          step: String(r.step),
        });
        var out = el("output", { for: id });
        inputs[r.key] = input;
        outputs[r.key] = out;
        input.addEventListener("input", function () {
          showValue(r);
        });
        return [
          el("div", { class: "range" }, [el("label", { for: id, text: tr(labels[r.key][0]) }), out, input]),
          el("p", { class: "hint", text: tr(labels[r.key][1]) }),
        ];
      }
      append(root, toggle(P.VOICE_TOGGLES[0]));
      P.VOICE_SETTINGS.forEach(function (r) {
        append(root, range(r));
      });
      append(root, toggle(P.VOICE_TOGGLES[1]));
      root.appendChild(
        el("button", {
          type: "button",
          class: "small ghost",
          text: tr("vDefaults"),
          onclick: function () {
            P.VOICE_TOGGLES.forEach(function (t) {
              inputs[t.key].checked = t.def;
            });
            P.VOICE_SETTINGS.forEach(function (r) {
              inputs[r.key].value = String(r.def);
              showValue(r);
            });
            // let app.js notice (live preview + unsaved badge)
            inputs.voiceIdThreshold.dispatchEvent(new Event("input", { bubbles: true }));
            inputs.voiceIdThreshold.dispatchEvent(new Event("change", { bubbles: true }));
            toast(tr("vDefaultsToast"), { key: "pp-vdef" });
          },
        })
      );
      fill();
    }

    views.voice = { render: render, fill: fill };
    render();
  }

  // ---------------------------------------------------------------- speaker chip

  var ICON_CARET = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M7 10l5 5 5-5"/></svg>';
  var ICON_LOCK =
    '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="5" y="11" width="14" height="9" rx="2"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/></svg>';
  var ICON_UNLOCK =
    '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="5" y="11" width="14" height="9" rx="2"/><path d="M8 11V8a4 4 0 0 1 7.5-2"/></svg>';

  function setManual(v) {
    state.manual = v || "";
    lsSet(MANUAL_KEY, state.manual);
    renderChip();
    H.emit("people:manual", { id: state.manual });
  }

  function chipText() {
    if (P.isPersonId(state.manual)) {
      var p = personById(state.manual);
      return { label: p ? p.name : tr("chipAutoShort"), mode: "manual", title: (p ? p.name : "") + " · " + tr("chipManual") };
    }
    if (state.manual === "_guest") return { label: tr("chipGuest"), mode: "manual guest", title: tr("chipGuest") + " · " + tr("chipManual") };
    var last = state.last;
    if (!last) return { label: tr("chipAutoShort"), mode: "auto", title: tr("chipAuto") };
    var name = last.id ? last.name || (personById(last.id) || {}).name || "?" : last.source === "uncertain" ? tr("chipUncertain") : tr("chipGuest");
    return {
      label: name,
      mode: "auto " + (last.id ? "known" : "guest"),
      title: tr("chipLast", { name: name }) + (typeof last.score === "number" ? " (" + P.formatScore(last.score, lang()) + ")" : ""),
    };
  }

  function mountChip() {
    var host = document.getElementById("speakerChip");
    if (!host || views.chip) return !!views.chip;
    host.classList.add("spk");
    var btn = el("button", {
      type: "button",
      class: "spk-chip",
      "aria-haspopup": "menu",
      "aria-expanded": "false",
    });
    var menu = el("div", { class: "spk-menu", role: "menu", hidden: true });
    host.appendChild(btn);
    host.appendChild(menu);

    function items() {
      return Array.prototype.slice.call(menu.querySelectorAll("[role=menuitemradio]"));
    }
    function openMenu() {
      buildMenu();
      menu.hidden = false;
      btn.setAttribute("aria-expanded", "true");
      var sel = menu.querySelector("[aria-checked=true]") || items()[0];
      if (sel) sel.focus();
      document.addEventListener("pointerdown", outside, true);
    }
    function closeMenu(focusBtn) {
      if (menu.hidden) return;
      menu.hidden = true;
      btn.setAttribute("aria-expanded", "false");
      document.removeEventListener("pointerdown", outside, true);
      if (focusBtn) btn.focus();
    }
    function outside(e) {
      if (!host.contains(e.target)) closeMenu(false);
    }
    function choose(value) {
      closeMenu(true);
      if (value === state.manual) return;
      if (P.isPersonId(value) && lockOn()) {
        // with the personal lock, picking a person needs the PIN (SPEC 4.10)
        requestUnlock().then(function (ok) {
          if (ok) setManual(value);
        });
        return;
      }
      setManual(value);
    }
    function item(value, label, extraCls) {
      var checked = state.manual === value;
      return el("button", {
        type: "button",
        role: "menuitemradio",
        class: "spk-item" + (extraCls ? " " + extraCls : ""),
        "aria-checked": checked ? "true" : "false",
        tabindex: "-1",
        text: label,
        onclick: function () {
          choose(value);
        },
      });
    }
    function buildMenu() {
      clear(menu);
      menu.appendChild(item("", tr("chipAuto"), "spk-auto"));
      menu.appendChild(el("div", { class: "spk-sep", role: "separator" }));
      state.people.forEach(function (p) {
        var unconfirmed = p.origin === "voice" && !p.confirmed;
        menu.appendChild(item(p.id, p.name, unconfirmed ? "is-unconfirmed" : ""));
      });
      menu.appendChild(el("div", { class: "spk-sep", role: "separator" }));
      menu.appendChild(item("_guest", tr("chipGuest"), "spk-guest"));
    }

    btn.addEventListener("click", function () {
      if (menu.hidden) openMenu();
      else closeMenu(true);
    });
    btn.addEventListener("keydown", function (e) {
      if (e.key === "ArrowUp" || e.key === "ArrowDown") {
        e.preventDefault();
        openMenu();
      }
    });
    menu.addEventListener("keydown", function (e) {
      var list = items();
      var i = list.indexOf(document.activeElement);
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation(); // do not stop speech / close panels
        closeMenu(true);
      } else if (e.key === "ArrowDown") {
        e.preventDefault();
        list[(i + 1) % list.length].focus();
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        list[(i - 1 + list.length) % list.length].focus();
      } else if (e.key === "Home") {
        e.preventDefault();
        list[0].focus();
      } else if (e.key === "End") {
        e.preventDefault();
        list[list.length - 1].focus();
      } else if (e.key === "Tab") {
        closeMenu(false);
      }
    });

    views.chip = { host: host, btn: btn, menu: menu, close: closeMenu };
    renderChip();
    return true;
  }

  function renderChip() {
    var v = views.chip;
    if (!v) return;
    // hidden while nobody is known (SPEC 5.4)
    var show = state.peopleLoaded && state.people.length > 0;
    v.host.hidden = !show;
    if (!show) {
      v.close(false);
      return;
    }
    var c = chipText();
    v.btn.className = "spk-chip " + c.mode.split(" ").map(function (m) {
      return "is-" + m;
    }).join(" ");
    v.btn.title = c.title;
    v.btn.setAttribute("aria-label", tr("chipAria") + " " + c.title);
    clear(v.btn);
    append(v.btn, [
      el("span", { class: "spk-dot", "aria-hidden": "true" }),
      el("span", { class: "spk-name", text: c.label }),
      el("span", { class: "spk-caret", "aria-hidden": "true", html: ICON_CARET }),
    ]);
    if (!v.menu.hidden) {
      // keep the open menu in sync (names, checked state)
      v.close(false);
    }
  }

  // ---------------------------------------------------------------- person toast (card)

  function richToast(msg, actions, ms) {
    var host = document.getElementById("toasts");
    if (!host) {
      // fall back to the single-action toast of the contract
      var a = actions && actions[0];
      return toast(msg, { ms: ms, timeout: ms, action: a ? { label: a.label, fn: a.fn } : undefined });
    }
    var node = el("div", { class: "toast info pp-toast", role: "status" });
    var closed = false;
    var timer = 0;
    function close() {
      if (closed) return;
      closed = true;
      clearTimeout(timer);
      node.classList.add("leaving");
      setTimeout(function () {
        if (node.parentNode) node.parentNode.removeChild(node);
      }, 180);
    }
    node.appendChild(el("span", { class: "toast-msg", text: msg }));
    (actions || []).forEach(function (a, i) {
      node.appendChild(
        el("button", {
          type: "button",
          class: i === 0 ? "toast-action" : "pp-toast-secondary",
          text: a.label,
          onclick: function () {
            close();
            try {
              a.fn();
            } catch (e) {
              /* ignore */
            }
          },
        })
      );
    });
    node.appendChild(el("button", { type: "button", class: "toast-close", "aria-label": tr("close"), text: "✕", onclick: close }));
    host.appendChild(node);
    if (ms) timer = setTimeout(close, ms);
    return { close: close };
  }

  var cardToasts = {};

  function onPersonCard(data) {
    if (!data) return;
    if (data.pending && data.name) {
      toast(tr("namePendingToast", { name: data.name }), { key: "pp-name-pending", ms: 8000 });
      return;
    }
    if (data.confirmable && P.isPersonId(data.id)) {
      if (cardToasts[data.id]) cardToasts[data.id].close();
      var p = { id: data.id, name: data.name || "?" };
      cardToasts[data.id] = richToast(
        tr("createdToast", { name: p.name }),
        [
          {
            label: tr("keep"),
            fn: function () {
              confirmPerson(p, true);
            },
          },
          {
            label: tr("remove"),
            fn: function () {
              confirmPerson(p, false);
            },
          },
        ],
        30000
      );
    }
    loadPeople();
  }

  // ---------------------------------------------------------------- migration dialog

  function maybeAskMigration() {
    if (state.migrationAsked || !state.people.length || !state.ready) return;
    if (ssGet(MIGRATION_LATER_KEY)) return;
    if (lockOn() && !probablyUnlocked()) return; // never pop a PIN prompt unasked
    state.migrationAsked = true;
    api()
      .fetchJSON("/api/people/migration")
      .then(
        function (res) {
          if (res && res.pending && res.count > 0) openMigration(res);
        },
        function () {
          state.migrationAsked = false; // locked or not there yet: ask again later
        }
      );
  }

  function openMigration(info) {
    var people = state.people.filter(function (p) {
      return !(p.origin === "voice" && !p.confirmed);
    });
    if (!people.length) people = state.people.slice();
    if (!people.length) return Promise.resolve();
    var first = people
      .slice()
      .sort(function (a, b) {
        return (P.parseTime(a.createdAt) || 0) - (P.parseTime(b.createdAt) || 0);
      })[0];
    var n = (info && info.count) || 0;
    var buttons = [{ label: tr("migLater"), value: "later", kind: "ghost" }, { label: tr("migHousehold"), value: "household" }];
    people.forEach(function (p) {
      buttons.push({ label: p.name, value: p.id, kind: p.id === first.id ? "primary" : "", focus: p.id === first.id });
    });
    return choiceDialog({
      cls: "pp-migration",
      title: tr("migTitle"),
      body: [
        el("p", { class: "pp-dlg-text", text: n === 1 ? tr("migBody1") : tr("migBody", { n: n }) }),
        el("p", { class: "pp-dlg-text pp-muted", text: tr("migNote", { name: first.name }) }),
      ],
      buttons: buttons,
    }).then(function (choice) {
      if (!choice || choice === "later") {
        ssSet(MIGRATION_LATER_KEY, "1");
        return;
      }
      return call("/api/people/migration", { method: "POST", body: { to: choice } }).then(
        function (res) {
          toast(tr("migDone", { n: (res && res.moved) || 0 }), { type: "success", key: "pp-mig" });
          H.emit("people:migrated", res || {});
          loadPeople();
        },
        function (err) {
          state.migrationAsked = false;
          toast(errText(err), { type: "error", key: "pp-err" });
        }
      );
    });
  }

  // ---------------------------------------------------------------- render all

  function renderAll() {
    if (views.people) views.people.render();
    renderChip();
  }

  // ---------------------------------------------------------------- wiring

  if (typeof H.registerSettingsSection === "function") {
    H.registerSettingsSection({ id: "people", order: 10, title: { en: P.STRINGS.en.sectionPeople, de: P.STRINGS.de.sectionPeople }, mount: mountPeople });
    H.registerSettingsSection({ id: "voiceid", order: 15, title: { en: P.STRINGS.en.sectionVoice, de: P.STRINGS.de.sectionVoice }, mount: mountVoiceTuning });
  }

  if (typeof H.hook === "function") {
    // SPEC 5.2/5.4: manual choice → manual; recent voice match → sticky; else nothing
    H.hook("payload", function (payload) {
      if (!payload || typeof payload !== "object") return;
      if (!state.peopleLoaded || !state.people.length) return;
      var sp = P.payloadSpeaker(state.manual, state.last, Date.now(), knownIds());
      if (sp) payload.speaker = sp;
    });
  }

  H.on("speaker", function (d) {
    if (!d || typeof d !== "object") return;
    state.last = { id: d.id || "", name: d.name || "", source: d.source || "", score: d.score, at: Date.now() };
    if (d.id && !personById(d.id) && state.peopleLoaded) loadPeople(); // a person created by voice
    renderChip();
  });

  H.on("card", function (c) {
    if (c && c.kind === "person") onPersonCard(c.data || {});
  });

  H.on("hub:people.changed", function () {
    loadPeople();
  });

  H.on("settings", function () {
    if (views.voice) views.voice.fill();
    renderAll();
  });

  H.on("status", function () {
    if (views.people) views.people.render();
  });

  H.on("lang", function () {
    if (views.voice) views.voice.render();
    renderAll();
  });

  H.on("people:unlocked", function () {
    renderAll();
    maybeAskMigration();
  });
  H.on("people:locked", renderAll);

  whenReady(function () {
    state.ready = true;
    if (!mountChip()) {
      // W1-E adds #speakerChip; tolerate it appearing a little later
      setTimeout(mountChip, 0);
    }
    loadPeople();
  });

  // Browser-only API for memory.js and later modules.
  P.ui = {
    dialog: choiceDialog,
    lockOn: lockOn,
    requestUnlock: requestUnlock,
    lockNow: lockNow,
    call: call,
    people: function () {
      return state.people.slice();
    },
    reload: loadPeople,
    openMigration: function () {
      return api()
        .fetchJSON("/api/people/migration")
        .catch(function (err) {
          if (err && err.code === "locked") return call("/api/people/migration");
          throw err;
        })
        .then(function (res) {
          if (res && res.pending && res.count > 0) return openMigration(res);
        });
    },
    openWizard: function (id) {
      var p = personById(id);
      if (p) openWizard(p);
    },
  };
})();
