/*
 * HERMES module bus (window.Hermes), shared by app.js and the feature
 * modules (voiceid.js, people.js, memory.js, …). Loaded before every feature
 * script and before app.js; app.js fills Hermes.api and then emits "ready".
 *
 *   Hermes.on(name, fn) / off / emit         events (see SPEC 5.2)
 *   Hermes.hook(name, fn) / hooks(name)      hooks app.js runs
 *   Hermes.registerTab(def)                  hub panel tabs
 *   Hermes.registerSettingsSection(def)      sections in the Settings panel
 *   Hermes.strings({en:{}, de:{}})           per-module i18n -> tr(key, vars)
 *   Hermes.events                            hub client for GET /api/events
 *
 * UMD: the browser gets window.Hermes; node tests require() this file and
 * get the factory plus the pure helpers.
 */
(function (root, factory) {
  var lib = factory();
  if (typeof module === "object" && module.exports) module.exports = lib;
  if (root && typeof root === "object" && root.document && !root.Hermes) root.Hermes = lib.createHermes(root);
})(typeof window !== "undefined" ? window : typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  // Event types the hub sends as named SSE events (SPEC 3.2). EventSource
  // only delivers named events to listeners registered for that name, so
  // modules that need another type call Hermes.events.listen(type).
  var HUB_TYPES = [
    "hello",
    "reminder.due",
    "reminder.acked",
    "checkin.meeting",
    "memory.saved",
    "memory.offer",
    "people.changed",
    "list.changed",
    "reminders.changed",
    "docs.changed",
    "calendar.changed",
    "settings.changed",
  ];

  var LOCK_NAME = "hermes-events";
  var CHANNEL_NAME = "hermes-events";
  var SPOKEN_KEY = "hermes-spoken-ids";
  var SPEAK_HERE_KEY = "hermes-speak-reminders";
  var SPOKEN_MAX = 50;
  var RETRY_MIN_MS = 3000;
  var RETRY_MAX_MS = 30000;

  // ------------------------------------------------------------ pure helpers

  /*
   * Remember `id` in a bounded list. Returns {list, fresh}: fresh is false
   * when the id was already in the list (another tab spoke it).
   */
  function dedupeIds(list, id, max) {
    max = max || SPOKEN_MAX;
    var out = Array.isArray(list)
      ? list.filter(function (x) {
          return typeof x === "string" || typeof x === "number";
        })
      : [];
    var key = String(id);
    for (var i = 0; i < out.length; i++) {
      if (String(out[i]) === key) return { list: out, fresh: false };
    }
    out.push(key);
    if (out.length > max) out = out.slice(out.length - max);
    return { list: out, fresh: true };
  }

  /* Parse one SSE data payload into an event object (null when unusable). */
  function parseEventData(type, data, lastEventId) {
    var ev;
    try {
      ev = typeof data === "string" ? JSON.parse(data) : data;
    } catch (e) {
      return null;
    }
    if (!ev || typeof ev !== "object" || Array.isArray(ev)) return null;
    if (typeof ev.type !== "string" || !ev.type) ev.type = type && type !== "message" ? type : "";
    if (!ev.type) return null;
    if (ev.id == null && lastEventId && /^\d+$/.test(String(lastEventId))) ev.id = Number(lastEventId);
    return ev;
  }

  /* The URL the leader opens: speak flag and replay position. */
  function eventsUrl(speakHere, since) {
    var q = [];
    if (speakHere) q.push("speak=1");
    if (since > 0) q.push("since=" + Math.floor(since));
    return "/api/events" + (q.length ? "?" + q.join("&") : "");
  }

  /* Interpolate "{name}" placeholders. Unknown placeholders stay as they are. */
  function format(str, vars) {
    str = String(str == null ? "" : str);
    if (!vars) return str;
    return str.replace(/\{(\w+)\}/g, function (all, k) {
      return Object.prototype.hasOwnProperty.call(vars, k) && vars[k] != null ? String(vars[k]) : all;
    });
  }

  /* Insert `def` into `list` (replacing the same id), sorted by order then id. */
  function upsertSorted(list, def) {
    var out = list.filter(function (x) {
      return x.id !== def.id;
    });
    out.push(def);
    out.sort(function (a, b) {
      var d = (a.order || 0) - (b.order || 0);
      return d !== 0 ? d : a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
    });
    return out;
  }

  /* location.host of a loopback address ("localhost:8080", "127.0.0.1", "[::1]:8080"). */
  function isLocalhostHost(host) {
    return /^(?:localhost|127(?:\.\d{1,3}){3}|\[::1\])(?::\d+)?$/i.test(String(host || ""));
  }

  /* "speak here" preference: stored value wins, else on for localhost only. */
  function speakHerePref(raw, localhost) {
    if (raw === "true" || raw === "1") return true;
    if (raw === "false" || raw === "0") return false;
    return !!localhost;
  }

  // ------------------------------------------------------------ the bus

  function createHermes(env) {
    env = env || {};
    var listeners = {};
    var hookTable = {};
    var tabList = [];
    var sectionList = [];
    var logError = function (where, err) {
      if (env.console && env.console.error) env.console.error("[Hermes] " + where + ":", err);
    };

    var H = {
      v: 1,
      ready: false,
      api: {},

      on: function (name, fn) {
        if (typeof fn !== "function") return H;
        (listeners[name] = listeners[name] || []).push(fn);
        // late subscribers to "ready" still run (asynchronously, like an event)
        if (name === "ready" && H.ready) {
          setTimeout(function () {
            if ((listeners.ready || []).indexOf(fn) >= 0) safeCall("ready", fn, undefined);
          }, 0);
        }
        return H;
      },

      off: function (name, fn) {
        var list = listeners[name];
        if (!list) return H;
        var i = list.indexOf(fn);
        if (i >= 0) list.splice(i, 1);
        return H;
      },

      emit: function (name, detail) {
        if (name === "ready") H.ready = true;
        var list = (listeners[name] || []).slice();
        for (var i = 0; i < list.length; i++) safeCall(name, list[i], detail);
        return H;
      },

      /* Register a hook; returns a function that removes it again. */
      hook: function (name, fn) {
        if (typeof fn !== "function") return function () {};
        (hookTable[name] = hookTable[name] || []).push(fn);
        return function () {
          var list = hookTable[name] || [];
          var i = list.indexOf(fn);
          if (i >= 0) list.splice(i, 1);
        };
      },

      hooks: function (name) {
        return (hookTable[name] || []).slice();
      },

      registerTab: function (def) {
        if (!def || typeof def.id !== "string" || !def.id) throw new Error("Hermes.registerTab: id required");
        tabList = upsertSorted(tabList, def);
        H.emit("hermes:tabs", tabList.slice());
        return H;
      },

      registerSettingsSection: function (def) {
        if (!def || typeof def.id !== "string" || !def.id) throw new Error("Hermes.registerSettingsSection: id required");
        sectionList = upsertSorted(sectionList, def);
        H.emit("hermes:sections", sectionList.slice());
        return H;
      },

      /* Registered tabs / sections, sorted by order (used by app.js). */
      tabs: function () {
        return tabList.slice();
      },
      sections: function () {
        return sectionList.slice();
      },

      /* Current UI language: app.js decides once it is up. */
      lang: function () {
        try {
          if (typeof H.api.lang === "function") return H.api.lang() === "de" ? "de" : "en";
        } catch (e) {
          /* fall through */
        }
        var doc = env.document && env.document.documentElement;
        var l = (doc && doc.lang) || (env.navigator && env.navigator.language) || "en";
        return /^de\b/i.test(l) ? "de" : "en";
      },

      /*
       * Per-module strings: tr(key, vars). Looks the key up in the current
       * language, then English, then returns the key itself.
       */
      strings: function (dict) {
        dict = dict || {};
        var tr = function (key, vars) {
          var lang = H.lang();
          var table = dict[lang] || {};
          var s = Object.prototype.hasOwnProperty.call(table, key) ? table[key] : dict.en && Object.prototype.hasOwnProperty.call(dict.en, key) ? dict.en[key] : key;
          return format(s, vars);
        };
        return tr;
      },
    };

    function safeCall(name, fn, detail) {
      try {
        fn(detail);
      } catch (e) {
        logError("listener for " + name, e);
      }
    }

    H.events = createHubClient(H, env);
    return H;
  }

  // ------------------------------------------------------------ hub client

  /*
   * One EventSource per browser. With Web Locks + BroadcastChannel the tab
   * holding the "hermes-events" lock is the leader: it connects and relays
   * every event to the other tabs. Every tab re-emits each event as
   * Hermes.emit("hub:" + type, ev). Without Web Locks (plain http on the LAN
   * is not a secure context) every tab connects, and claim() dedupes speech
   * through localStorage.
   */
  function createHubClient(H, env) {
    var nav = env.navigator || {};
    var ES = env.EventSource || null;
    var BC = env.BroadcastChannel || null;
    var timers = {
      set: env.setTimeout ? env.setTimeout.bind(env) : setTimeout,
      clear: env.clearTimeout ? env.clearTimeout.bind(env) : clearTimeout,
    };
    var useLocks = !!(nav.locks && typeof nav.locks.request === "function" && BC);

    var state = {
      started: false,
      es: null,
      channel: null,
      lastId: 0,
      retryMs: RETRY_MIN_MS,
      retryTimer: 0,
      seen: [], // "id:repeat" keys already emitted in this tab
      extraTypes: [],
      releaseLock: null,
      connectedSpeak: null,
    };

    var events = {
      leader: false,
      active: false,

      start: function () {
        if (state.started) return events;
        state.started = true;
        var doc = env.document;
        if (doc && typeof doc.addEventListener === "function") {
          var lead = function () {
            events.takeLead();
          };
          doc.addEventListener("pointerdown", lead, true);
          doc.addEventListener("keydown", lead, true);
        }
        if (!ES) return events; // no EventSource (very old browser / node)
        if (useLocks) {
          try {
            state.channel = new BC(CHANNEL_NAME);
            state.channel.onmessage = onChannelMessage;
            events.active = true; // followers receive through the channel
          } catch (e) {
            state.channel = null;
          }
        }
        if (state.channel) {
          requestLead(false);
        } else {
          useLocks = false;
          events.leader = true;
          connect();
        }
        return events;
      },

      /*
       * Make this tab the leader now (the user just clicked or typed here).
       * The leader is the tab that announces reminders, and only a tab the
       * user interacted with may play sound: an untouched first tab would
       * otherwise keep the lead and stay silent. The old leader is told to
       * step back and queues for the lock again.
       */
      takeLead: function () {
        if (!state.started || !useLocks || !state.channel || events.leader) return false;
        try {
          state.channel.postMessage({ kind: "lead" });
        } catch (e) {
          /* ignore */
        }
        requestLead(true);
        return true;
      },

      /* Close the connection and give up leadership (tests, page unload). */
      stop: function () {
        state.started = false;
        timers.clear(state.retryTimer);
        state.retryTimer = 0;
        closeSource();
        if (state.channel) {
          try {
            state.channel.close();
          } catch (e) {
            /* ignore */
          }
          state.channel = null;
        }
        if (state.releaseLock) state.releaseLock();
        state.releaseLock = null;
        events.leader = false;
        events.active = false;
      },

      /*
       * Reconnect now (e.g. after "speak here" changed). A follower asks the
       * leader to do it.
       */
      reconnect: function () {
        if (!state.started) return;
        if (events.leader) {
          closeSource();
          connect();
        } else if (state.channel) {
          try {
            state.channel.postMessage({ kind: "reconnect" });
          } catch (e) {
            /* ignore */
          }
        }
      },

      /* Also deliver events of this (not built-in) type. */
      listen: function (type) {
        if (typeof type !== "string" || !type || HUB_TYPES.indexOf(type) >= 0 || state.extraTypes.indexOf(type) >= 0) return;
        state.extraTypes.push(type);
        if (state.es) state.es.addEventListener(type, sourceHandler(type));
      },

      /*
       * Should this tab act on (speak) the event with this id? With Web Locks
       * only the leader does; without, the first tab to claim the id does.
       */
      claim: function (id) {
        if (useLocks && !events.leader) return false;
        var store = env.localStorage;
        if (!store) return true;
        try {
          var raw = store.getItem(SPOKEN_KEY);
          var list = raw ? JSON.parse(raw) : [];
          var r = dedupeIds(list, id, SPOKEN_MAX);
          if (!r.fresh) return false;
          store.setItem(SPOKEN_KEY, JSON.stringify(r.list));
        } catch (e) {
          /* storage blocked: speak anyway */
        }
        return true;
      },

      /* The speak-here preference of this device. */
      speakHere: function () {
        var raw = null;
        try {
          raw = env.localStorage ? env.localStorage.getItem(SPEAK_HERE_KEY) : null;
        } catch (e) {
          raw = null;
        }
        var host = env.location ? env.location.host : "";
        return speakHerePref(raw, isLocalhostHost(host));
      },

      lastId: function () {
        return state.lastId;
      },
    };

    function onChannelMessage(e) {
      var msg = e && e.data;
      if (!msg || typeof msg !== "object") return;
      if (msg.kind === "event" && !events.leader) deliver(msg.ev, false);
      else if (msg.kind === "reconnect" && events.leader) events.reconnect();
      else if (msg.kind === "lead" && events.leader) stepDown();
    }

    /*
     * Ask for the leader lock. steal=true takes it from the current holder
     * (see takeLead); otherwise wait in line until the leader tab goes away.
     */
    function requestLead(steal) {
      var released = new Promise(function (resolve) {
        if (state.releaseLock) state.releaseLock(); // drop a lock this tab still holds
        state.releaseLock = resolve;
      });
      var req;
      try {
        req = nav.locks.request(LOCK_NAME, steal ? { mode: "exclusive", steal: true } : { mode: "exclusive" }, function () {
          if (!state.started) return null;
          events.leader = true;
          H.emit("hub:leader", { leader: true });
          connect();
          return released; // held for the life of the tab (or until stolen)
        });
      } catch (e) {
        req = Promise.reject(e);
      }
      if (req && req.catch) {
        req.catch(function (err) {
          // stolen by another tab: stepDown() already handled it
          if (err && err.name === "AbortError") return;
          // locks unusable after all: behave like a browser without them
          if (!state.started || events.leader) return;
          useLocks = false;
          events.leader = true;
          connect();
        });
      }
    }

    /* Another tab took the lead: stop relaying and queue for the lock again. */
    function stepDown() {
      events.leader = false;
      timers.clear(state.retryTimer);
      state.retryTimer = 0;
      closeSource();
      H.emit("hub:leader", { leader: false });
      if (state.started && state.channel) requestLead(false);
    }

    function sourceHandler(type) {
      return function (e) {
        var ev = parseEventData(type, e && e.data, e && e.lastEventId);
        if (ev) deliver(ev, true);
      };
    }

    function connect() {
      if (!state.started || !events.leader || !ES) return;
      timers.clear(state.retryTimer);
      state.retryTimer = 0;
      var speak = events.speakHere();
      var es;
      try {
        es = new ES(eventsUrl(speak, state.lastId));
      } catch (e) {
        scheduleRetry();
        return;
      }
      state.es = es;
      state.connectedSpeak = speak;
      HUB_TYPES.concat(state.extraTypes).forEach(function (type) {
        es.addEventListener(type, sourceHandler(type));
      });
      es.onmessage = sourceHandler("message");
      es.onopen = function () {
        if (es !== state.es) return;
        state.retryMs = RETRY_MIN_MS;
        events.active = true;
        H.emit("hub:open", { leader: true });
      };
      es.onerror = function () {
        if (es !== state.es) return;
        // CONNECTING (0): the browser retries itself, sending Last-Event-ID.
        // CLOSED (2): it gave up (e.g. 503 subscriber cap): retry ourselves.
        if (es.readyState === 2) {
          events.active = !!state.channel && !events.leader;
          closeSource();
          scheduleRetry();
        }
      };
    }

    function scheduleRetry() {
      if (!state.started) return;
      timers.clear(state.retryTimer);
      var wait = state.retryMs;
      state.retryMs = Math.min(RETRY_MAX_MS, state.retryMs * 2);
      state.retryTimer = timers.set(function () {
        state.retryTimer = 0;
        connect();
      }, wait);
    }

    function closeSource() {
      var es = state.es;
      state.es = null;
      if (es) {
        try {
          es.close();
        } catch (e) {
          /* ignore */
        }
      }
    }

    function deliver(ev, fromSource) {
      if (!ev || typeof ev !== "object" || typeof ev.type !== "string" || !ev.type) return;
      if (typeof ev.id === "number" && ev.id > 0) {
        var repeat = ev.data && typeof ev.data.repeat === "number" ? ev.data.repeat : 0;
        var key = ev.id + ":" + repeat;
        if (state.seen.indexOf(key) >= 0) return; // replayed after a reconnect
        state.seen.push(key);
        if (state.seen.length > 200) state.seen = state.seen.slice(-200);
        if (ev.id > state.lastId) state.lastId = ev.id;
      }
      if (fromSource && events.leader && state.channel) {
        try {
          state.channel.postMessage({ kind: "event", ev: ev });
        } catch (e) {
          /* ignore */
        }
      }
      H.emit("hub:" + ev.type, ev);
    }

    // a tab coming back from the background reconnects at once instead of
    // waiting for the back-off
    H.on("visible", function () {
      if (state.started && events.leader && !state.es) connect();
    });

    return events;
  }

  return {
    createHermes: createHermes,
    dedupeIds: dedupeIds,
    parseEventData: parseEventData,
    eventsUrl: eventsUrl,
    format: format,
    upsertSorted: upsertSorted,
    speakHerePref: speakHerePref,
    isLocalhostHost: isLocalhostHost,
    HUB_TYPES: HUB_TYPES,
  };
});
