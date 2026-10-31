// Shared script for every page: <body data-page="..."> picks what runs.
// config.js (generated at deploy, config.local.js locally) sets the API hosts,
// whether payments are in test mode, the packs, the contact address and
// whether prices include VAT. The session token lives in localStorage.
"use strict";

const API = trimSlash(window.ACCOUNT_API || "");
const PRODUCER = trimSlash(window.PRODUCER_API || "https://<producer host>");
const CONSUMER = trimSlash(window.CONSUMER_API || "https://<consumer host>");
const CONTACT_EMAIL = window.CONTACT_EMAIL || "";
const SESSION_KEY = "session";
// How long, and how often, the dashboard reloads the keys after a payment.
const PAID_POLL_MS = 60000;
const PAID_POLL_EVERY_MS = 5000;

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

// vatNote is how pack prices are labelled: Stripe adds VAT on top of the
// price, or the price includes it, depending on the Stripe Prices' tax
// behavior.
function vatNote() {
  return window.PRICES_INCLUDE_VAT === false ? "VAT added at checkout" : "incl. VAT";
}

// sortedPacks returns [name, pack] pairs, smallest first.
function sortedPacks(packs) {
  return Object.entries(packs || {}).sort((a, b) => a[1].credits - b[1].credits);
}

// setTestBanner shows or hides the test-mode banner.
function setTestBanner(on) {
  for (const node of document.querySelectorAll("[data-test-banner]")) node.hidden = !on;
}

// fillContacts turns every [data-contact] element into a mailto link.
function fillContacts() {
  for (const node of document.querySelectorAll("[data-contact]")) {
    if (!CONTACT_EMAIL) {
      node.textContent = "(contact address not configured)";
      continue;
    }
    node.replaceChildren(el("a", { href: "mailto:" + CONTACT_EMAIL }, CONTACT_EMAIL));
  }
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
    // The packs come from config.js; without it the static cards stay.
    const packs = sortedPacks(window.PACKS);
    if (packs.length > 0) {
      $("packs").replaceChildren(...packs.map(([name, p]) =>
        el("div", { class: "card" }, el("strong", {}, capitalize(name)),
          credits(p.credits) + " credits · " + eurFmt.format(p.price_eur))));
    }
    $("vat-note").textContent = "Prices " + (window.PRICES_INCLUDE_VAT === false ? "exclude VAT, added at checkout" : "include VAT") +
      ". Payments are processed by Stripe, through Link, as the merchant of record.";
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
  terms() {},
  refunds() {},
};

// --- dashboard

async function dashboard() {
  let me = null;
  let keys = [];
  // The key of the open checkout.
  let checkoutKey = null;

  const pageMessage = $("page-message");
  const createMessage = $("create-message");

  async function loadMe() {
    const r = await authed("GET", "/api/me");
    if (r.status !== 200) throw new Error(errorText(r, "Couldn't load your account"));
    me = r.data;
    setTestBanner(me.test_mode);
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

  // --- checkout

  const dialog = $("checkout");
  const checkoutMessage = $("checkout-message");

  function openCheckout(k) {
    checkoutKey = k;
    $("checkout-key").textContent = k.name + " (…" + k.hint + ")";
    $("checkout-test").hidden = !me.test_mode;
    renderPacks();
    show(checkoutMessage, "", "");
    dialog.showModal();
  }

  // renderPacks lists the packs to choose from, the first that fits checked.
  function renderPacks() {
    // An account holds at most me.max_credits, summed over its keys; packs
    // that would go over can't be bought (the server checks it too).
    const total = keys.reduce((sum, key) => sum + key.credits, 0);
    const fits = (p) => !(me.max_credits > 0) || total + p.credits <= me.max_credits;
    const packs = sortedPacks(me.packs);
    const first = packs.findIndex(([, p]) => fits(p));
    $("packs").replaceChildren(
      el("legend", { class: "small muted" }, "Pack"),
      ...packs.map(([name, p], i) =>
        el("label", { class: "pack" },
          el("input", { type: "radio", name: "pack", value: name, checked: i === first, disabled: !fits(p) }),
          el("span", {}, el("strong", {}, capitalize(name)),
            " · " + credits(p.credits) + " credits · " + eurFmt.format(p.price_eur) + " " + vatNote()))),
    );
    const capped = me.max_credits > 0 && packs.some(([, p]) => !fits(p));
    show($("checkout-cap"), capped
      ? "An account can hold at most " + credits(me.max_credits) + " credits; yours has " + credits(total) + "." +
        (first < 0 ? " Use some before buying more." : "")
      : "", "");
    $("checkout-pay").disabled = !canPay();
  }

  function canPay() {
    return $("checkout-form").querySelector('input[name="pack"]:checked:enabled') !== null;
  }

  $("checkout-close").addEventListener("click", () => dialog.close());

  // Back from Stripe without paying, the browser may restore this page as it
  // was left: with the button disabled.
  window.addEventListener("pageshow", (e) => {
    if (!e.persisted) return;
    $("checkout-pay").disabled = !canPay();
    show(checkoutMessage, "", "");
  });

  $("checkout-form").addEventListener("submit", async (e) => {
    e.preventDefault(); // keep the dialog open
    const pack = new FormData(e.target).get("pack");
    const pay = $("checkout-pay");
    pay.disabled = true;
    show(checkoutMessage, "Opening the payment page…", "");
    let leaving = false;
    try {
      const r = await authed("POST", "/api/checkout", { pack, key_id: checkoutKey.key_id });
      if (r.status === 200 && r.data && typeof r.data.url === "string" && r.data.url.startsWith("https://")) {
        // A top-level navigation to Stripe's page, so the CSP stays as is.
        leaving = true;
        location.href = r.data.url;
      } else if (r.status === 409) {
        // Over the account's credit cap, e.g. after a payment in another tab.
        show(checkoutMessage, errorText(r, "This pack would take your account over its credit limit."), "error");
        await loadKeys();
        renderPacks();
      } else if (r.status === 404) {
        show(checkoutMessage, "This key no longer exists.", "error");
        await loadKeys();
      } else if (r.status === 429) {
        show(checkoutMessage, "Too many payment attempts. Try again in an hour.", "error");
      } else if (r.status === 503) {
        show(checkoutMessage, "Payments aren't available right now. Try again later.", "error");
      } else if (r.status === 502) {
        show(checkoutMessage, "Couldn't open the payment page. Try again.", "error");
      } else {
        show(checkoutMessage, errorText(r, "Couldn't open the payment page"), "error");
      }
    } catch (err) {
      show(checkoutMessage, err.message, "error");
    } finally {
      pay.disabled = leaving || !canPay();
    }
  });

  // Back from Stripe after paying: the webhook credits the key within
  // moments, so reload the keys until a balance goes up, for up to a minute.
  async function afterPayment() {
    const params = new URLSearchParams(location.search);
    if (params.get("paid") !== "1") return;
    show(pageMessage, "Payment received. Your credits appear within a minute.", "ok");
    const before = new Map(keys.map((k) => [k.key_id, k.credits]));
    const raised = () => keys.some((k) => k.credits > (before.get(k.key_id) ?? Infinity));
    const deadline = Date.now() + PAID_POLL_MS;
    while (Date.now() < deadline && !raised()) {
      await new Promise((resolve) => setTimeout(resolve, PAID_POLL_EVERY_MS));
      try {
        await loadKeys();
      } catch {
        // Try again on the next round.
      }
    }
    if (raised()) {
      show(pageMessage, "Payment received. Your credits have been added.", "ok");
    } else if (me.max_credits > 0) {
      // Likely refunded: another payment took the account to its cap first.
      show(pageMessage, "Payment received, but no credits were added yet. A payment that would take your account over " +
        credits(me.max_credits) + " credits is refunded in full. Otherwise, reload the page in a few minutes.", "");
    }
    params.delete("paid");
    const query = params.toString();
    history.replaceState(null, "", location.pathname + (query ? "?" + query : ""));
  }

  await loadMe();
  await loadKeys();
  await afterPayment();
}

// --- start

document.addEventListener("DOMContentLoaded", () => {
  // The dashboard corrects this from /api/me.
  setTestBanner(window.TEST_MODE === true);
  fillContacts();
  const signedIn = Boolean(getSession());
  for (const node of document.querySelectorAll("[data-signed-in]")) node.hidden = !signedIn;
  for (const node of document.querySelectorAll("[data-signed-out]")) node.hidden = signedIn;
  const page = pages[document.body.dataset.page];
  if (page) page();
});
