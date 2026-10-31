// Shared script for every page: <body data-page="..."> picks what runs.
// config.js (generated at deploy, config.local.js locally) sets the API hosts.
// The session token lives in localStorage; the test-mode password only in
// memory, for as long as the dashboard is open.
"use strict";

const API = trimSlash(window.ACCOUNT_API || "");
const PRODUCER = trimSlash(window.PRODUCER_API || "https://<producer host>");
const CONSUMER = trimSlash(window.CONSUMER_API || "https://<consumer host>");
const SESSION_KEY = "session";

function trimSlash(s) {
  return s.replace(/\/+$/, "");
}

// --- session

function getSession() {
  try {
    return localStorage.getItem(SESSION_KEY);
  } catch {
    return null;
  }
}

function setSession(token) {
  try {
    localStorage.setItem(SESSION_KEY, token);
  } catch {
    // Without storage the session can't outlive this page.
  }
}

function clearSession() {
  try {
    localStorage.removeItem(SESSION_KEY);
  } catch {
    // Nothing stored then.
  }
}

// --- API

class NetworkError extends Error {}

// api calls the account app and returns {status, data}. It throws
// NetworkError when the server can't be reached.
async function api(method, path, body) {
  if (!API) {
    throw new NetworkError("This site isn't configured: config.js doesn't set ACCOUNT_API.");
  }
  const headers = {};
  const session = getSession();
  if (session) headers.Authorization = "Bearer " + session;
  if (body !== undefined) headers["Content-Type"] = "application/json";
  let resp;
  try {
    resp = await fetch(API + path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      cache: "no-store",
    });
  } catch {
    throw new NetworkError("Can't reach the server. Check your connection and try again.");
  }
  let data = null;
  const text = await resp.text();
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = null;
    }
  }
  return { status: resp.status, data };
}

// authed is api for signed-in pages: a 401 ends the session and goes to the
// sign-in page.
async function authed(method, path, body) {
  const r = await api(method, path, body);
  if (r.status === 401) {
    clearSession();
    location.replace("login.html");
    return new Promise(() => {}); // the page is going away
  }
  return r;
}

function errorText(r, fallback) {
  return (r.data && r.data.error) || fallback + " (HTTP " + r.status + ")";
}

// --- DOM helpers

function $(id) {
  return document.getElementById(id);
}

// el creates an element. props are set as properties, except "class" and
// data-/aria- names, which are attributes. Children are nodes or text.
function el(tag, props, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (k === "class") node.className = v;
    else if (k.startsWith("data-") || k.startsWith("aria-")) node.setAttribute(k, v);
    else node[k] = v;
  }
  for (const c of children) node.append(c);
  return node;
}

// show sets a message element's text and kind ("ok", "error" or "").
function show(node, text, kind) {
  node.textContent = text;
  node.classList.remove("ok", "error");
  if (kind) node.classList.add(kind);
  node.hidden = !text;
}

const numberFmt = new Intl.NumberFormat();
const eurFmt = new Intl.NumberFormat(undefined, { style: "currency", currency: "EUR", maximumFractionDigits: 2 });

function credits(n) {
  return numberFmt.format(n);
}

function capitalize(s) {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

// --- pages

const pages = {
  index() {
    $("curl-extract").textContent =
      "curl -X POST " + PRODUCER + "/api/extract \\\n" +
      '  -H "X-API-Key: $KEY" -H "Content-Type: application/pdf" \\\n' +
      "  --data-binary @document.pdf";
    $("curl-text").textContent =
      "curl -X POST " + CONSUMER + "/api/text \\\n" +
      '  -H "X-API-Key: $KEY" -H "Content-Type: application/json" \\\n' +
      "  -d '{\"request_id\": \"<request_id>\"}'";
  },

  login() {
    const form = $("login-form");
    const message = $("login-message");
    form.addEventListener("submit", async (e) => {
      e.preventDefault();
      const button = form.querySelector("button");
      button.disabled = true;
      try {
        const r = await api("POST", "/api/auth/start", { email: $("email").value });
        if (r.status === 202) {
          show(message, "Check your inbox for a sign-in link (valid 15 minutes).", "ok");
        } else if (r.status === 400) {
          show(message, "That doesn't look like an email address.", "error");
        } else {
          show(message, errorText(r, "Something went wrong, try again"), "error");
        }
      } catch (err) {
        show(message, err.message, "error");
      } finally {
        button.disabled = false;
      }
    });
  },

  async auth() {
    const message = $("auth-message");
    const token = new URLSearchParams(location.hash.slice(1)).get("token");
    // Drop the token from the address bar and history.
    history.replaceState(null, "", location.pathname + location.search);
    const fail = (text) => {
      document.querySelector("h1").textContent = "Couldn't sign you in";
      show(message, text, "error");
      $("auth-retry").hidden = false;
    };
    if (!token) {
      fail("This link has expired or was already used.");
      return;
    }
    try {
      const r = await api("POST", "/api/auth/verify", { token });
      if (r.status === 200 && r.data && r.data.session) {
        setSession(r.data.session);
        location.replace("dashboard.html");
      } else if (r.status === 401) {
        fail("This link has expired or was already used.");
      } else {
        fail(errorText(r, "Something went wrong"));
      }
    } catch (err) {
      fail(err.message);
    }
  },

  dashboard() {
    if (!getSession()) {
      location.replace("login.html");
      return;
    }
    dashboard().catch((err) => show($("page-message"), err.message, "error"));
  },

  privacy() {},
};

// --- dashboard

async function dashboard() {
  let me = null;
  let keys = [];
  // The test-mode password, kept for this page only.
  let password = "";
  // The key and order of the open checkout. The order id is reused when a
  // payment is retried, so a retry can't credit twice.
  let checkoutKey = null;
  let orderId = null;

  const pageMessage = $("page-message");
  const createMessage = $("create-message");

  async function loadMe() {
    const r = await authed("GET", "/api/me");
    if (r.status !== 200) throw new Error(errorText(r, "Couldn't load your account"));
    me = r.data;
    $("me-email").textContent = me.email;
    $("price-text").textContent = credits(me.prices.text_page);
    $("price-ocr").textContent = credits(me.prices.ocr_page);
  }

  async function loadKeys() {
    const r = await authed("GET", "/api/keys");
    if (r.status !== 200) throw new Error(errorText(r, "Couldn't load your keys"));
    keys = r.data;
    renderKeys();
  }

  function renderKeys() {
    const rows = keys.map((k) => {
      const buy = el("button", { class: "secondary small", type: "button", textContent: "Buy credits" });
      buy.addEventListener("click", () => openCheckout(k));
      buy.hidden = me.payments !== "mock";
      const revoke = el("button", { class: "danger small", type: "button", textContent: "Revoke" });
      revoke.addEventListener("click", () => revokeKey(k));
      return el("tr", {},
        el("td", {}, k.name),
        el("td", {}, el("code", {}, "…" + k.hint)),
        el("td", { class: "num", "data-credits": k.key_id }, credits(k.credits)),
        el("td", {}, new Date(k.created_at).toLocaleDateString()),
        el("td", { class: "actions" }, buy, " ", revoke),
      );
    });
    $("keys").replaceChildren(...rows);
    $("no-keys").hidden = keys.length > 0;

    const full = keys.length >= me.max_keys;
    for (const node of $("create-form").elements) node.disabled = full;
    show($("max-keys"), full ? "You have the maximum of " + me.max_keys + " keys. Revoke one to create another." : "", "");
    show($("free-credits"),
      !full && me.free_credits_available > 0 ? "Your first key comes with " + credits(me.free_credits_available) + " free credits." : "",
      "ok");
  }

  function setBalance(keyID, balance) {
    const k = keys.find((k) => k.key_id === keyID);
    if (k) k.credits = balance;
    const cell = document.querySelector('[data-credits="' + keyID + '"]');
    if (cell) cell.textContent = credits(balance);
  }

  // Create a key.
  $("create-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    show(createMessage, "", "");
    const button = e.target.querySelector("button");
    button.disabled = true;
    try {
      const r = await authed("POST", "/api/keys", { name: $("key-name").value });
      if (r.status === 201) {
        $("key-name").value = "";
        $("new-key-value").textContent = r.data.key;
        $("copy-key").textContent = "Copy";
        $("new-key").hidden = false;
        me.free_credits_available = 0;
        await loadKeys();
      } else if (r.status === 409) {
        show(createMessage, "You already have the maximum number of keys.", "error");
        await loadKeys();
      } else {
        show(createMessage, errorText(r, "Couldn't create the key"), "error");
      }
    } catch (err) {
      show(createMessage, err.message, "error");
    } finally {
      button.disabled = keys.length >= me.max_keys;
    }
  });

  $("copy-key").addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText($("new-key-value").textContent);
      $("copy-key").textContent = "Copied";
    } catch {
      // No clipboard access: select the key for a manual copy.
      getSelection().selectAllChildren($("new-key-value"));
    }
  });

  $("hide-key").addEventListener("click", () => {
    $("new-key-value").textContent = "";
    $("new-key").hidden = true;
  });

  async function revokeKey(k) {
    const ok = confirm(
      'Revoke "' + k.name + '" (…' + k.hint + ")?\n\n" +
      "It stops working at once and its " + credits(k.credits) + " credits are lost. This can't be undone.");
    if (!ok) return;
    try {
      const r = await authed("DELETE", "/api/keys?key_id=" + encodeURIComponent(k.key_id));
      if (r.status !== 204 && r.status !== 404) {
        show(pageMessage, errorText(r, "Couldn't revoke the key"), "error");
      }
      await loadKeys();
    } catch (err) {
      show(pageMessage, err.message, "error");
    }
  }

  $("logout").addEventListener("click", async () => {
    try {
      await api("POST", "/api/auth/logout");
    } catch {
      // Signed out locally either way.
    }
    clearSession();
    location.replace("index.html");
  });

  $("delete-account").addEventListener("click", async () => {
    const ok = confirm(
      "Delete your account?\n\nYour keys are revoked and their credits are lost. This can't be undone.");
    if (!ok) return;
    try {
      const r = await authed("DELETE", "/api/me");
      if (r.status !== 204) {
        show(pageMessage, errorText(r, "Couldn't delete the account"), "error");
        return;
      }
      clearSession();
      location.replace("index.html");
    } catch (err) {
      show(pageMessage, err.message, "error");
    }
  });

  // --- mock checkout

  const dialog = $("checkout");
  const checkoutMessage = $("checkout-message");

  function openCheckout(k) {
    checkoutKey = k;
    orderId = crypto.randomUUID();
    $("checkout-key").textContent = k.name + " (…" + k.hint + ")";
    const packs = Object.entries(me.packs).sort((a, b) => a[1].credits - b[1].credits);
    $("packs").replaceChildren(
      el("legend", { class: "small muted" }, "Pack"),
      ...packs.map(([name, p], i) => {
        const radio = el("input", { type: "radio", name: "pack", value: name, checked: i === 0 });
        // A different pack is a different order.
        radio.addEventListener("change", () => { orderId = crypto.randomUUID(); });
        return el("label", { class: "pack" }, radio,
          el("span", {}, el("strong", {}, capitalize(name)), " · " + credits(p.credits) + " credits · " + eurFmt.format(p.price_eur)));
      }),
    );
    $("mock-password").value = password;
    show(checkoutMessage, "", "");
    $("checkout-pay").disabled = false;
    dialog.showModal();
  }

  $("checkout-close").addEventListener("click", () => dialog.close());

  $("checkout-form").addEventListener("submit", async (e) => {
    e.preventDefault(); // keep the dialog open
    const pack = new FormData(e.target).get("pack");
    password = $("mock-password").value;
    const pay = $("checkout-pay");
    pay.disabled = true;
    show(checkoutMessage, "Processing…", "");
    try {
      const r = await authed("POST", "/api/purchases", {
        pack, key_id: checkoutKey.key_id, order_id: orderId, password,
      });
      if (r.status === 200) {
        setBalance(checkoutKey.key_id, r.data.balance);
        show(checkoutMessage,
          "Added " + credits(r.data.credits) + " credits. Balance: " + credits(r.data.balance) + ".", "ok");
        orderId = crypto.randomUUID(); // the next payment is a new order
      } else if (r.status === 403) {
        password = "";
        const off = r.data && r.data.error === "test-mode purchases are disabled";
        show(checkoutMessage, off ? "Test-mode purchases are turned off." : "Wrong test-mode password.", "error");
      } else if (r.status === 429) {
        show(checkoutMessage, "Too many wrong passwords. Try again later.", "error");
      } else if (r.status === 404) {
        show(checkoutMessage, "This key no longer exists.", "error");
        await loadKeys();
      } else {
        show(checkoutMessage, errorText(r, "The payment failed, try again"), "error");
      }
    } catch (err) {
      // The same order id is sent again on retry.
      show(checkoutMessage, err.message, "error");
    } finally {
      pay.disabled = false;
    }
  });

  await loadMe();
  await loadKeys();
}

// --- start

document.addEventListener("DOMContentLoaded", () => {
  const signedIn = Boolean(getSession());
  for (const node of document.querySelectorAll("[data-signed-in]")) node.hidden = !signedIn;
  for (const node of document.querySelectorAll("[data-signed-out]")) node.hidden = signedIn;
  const page = pages[document.body.dataset.page];
  if (page) page();
});
