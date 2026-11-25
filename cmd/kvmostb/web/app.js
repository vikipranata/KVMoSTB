/*
	KVMoSTB web console logic.

	  - video : MJPEG multipart via <img src="/api/video/stream">
	  - input : WebSocket /ws/hid, fire-and-forget; when the server queue is
	            full it drops the oldest event (event numbers 0-5, JSON fields
	            keycode, mouse_x/y, mouse_scroll)
	  - auth  : kvmostb_session cookie (HttpOnly) set by the /login form;
	            any API 401 sends the browser back to /login
*/

"use strict";

const $ = (id) => document.getElementById(id);

async function api(path, opts = {}) {
	const res = await fetch(path, opts);
	if (res.status === 401) {
		// Session expired/missing: go to the login page (guard: no-op
		// when already there so the redirect can't loop).
		if (location.pathname !== "/login") location.href = "/login";
		throw new Error("session expired, please log in");
	}
	if (!res.ok) {
		const body = await res.json().catch(() => ({}));
		throw new Error(body.error || `${res.status} ${res.statusText}`);
	}
	return res.json();
}

function setChip(el, ok, label) {
	el.style.display = "";
	el.textContent = label;
	el.classList.toggle("chip--info", !ok);
	el.classList.toggle("chip--success", ok);
}

function showError(el, msg) {
	if (msg) {
		el.textContent = msg;
		el.style.display = "";
	} else {
		el.style.display = "none";
	}
}

async function pollStatus() {
	try {
		const s = await api("/api/status");
		const videoUp = s.capturing && s.width > 0;
		$("kpi-res").textContent = videoUp ? `${s.width}x${s.height}` : "offline";
		$("kpi-fps").textContent = videoUp ? `${s.fps} fps` : "offline";
		$("kpi-bitrate").textContent = videoUp ? fmtBitrate(s.bitrate_bps) : "—";
		$("kpi-udc").textContent = s.udc_state || "unknown";
		for (const id of ["sel-res", "sel-fps", "btn-apply", "btn-screenshot"]) $(id).disabled = !videoUp;

		$("iso-current").textContent = s.iso_file || "(none)";
		setISOButton();
	} catch (e) {
		/* status poll error: api() handles 401 redirect; ignore otherwise */
	}
}

function fmtBitrate(bps) {
	if (!bps) return "0.0 Mbps";
	return `${(bps / 1e6).toFixed(1)} Mbps`;
}

setInterval(pollStatus, 2000);
pollStatus();

let streamMenu = [];

async function loadStreamMenu() {
	try {
		streamMenu = await api("/api/video/resolutions");
	} catch (e) {
		return;
	}
	const selRes = $("sel-res");
	const selFps = $("sel-fps");
	selRes.innerHTML = "";
	for (const m of streamMenu) {
		const opt = document.createElement("option");
		opt.value = `${m.width}x${m.height}`;
		opt.textContent = `${m.width}x${m.height}`;
		selRes.appendChild(opt);
	}
	selRes.onchange = fillFps;
	fillFps();

	// preselect the current capture
	const s = await api("/api/status").catch(() => null);
	if (s) {
		const hit = streamMenu.find((m) => m.width === s.width && m.height === s.height);
		if (hit) {
			selRes.value = `${hit.width}x${hit.height}`;
			fillFps();
			selFps.value = String(s.fps);
		}
	}
}

function fillFps() {
	const selFps = $("sel-fps");
	const cur = $("sel-res").value;
	const hit = streamMenu.find((m) => `${m.width}x${m.height}` === cur);
	selFps.innerHTML = "";
	for (const f of (hit ? hit.fps : [])) {
		const opt = document.createElement("option");
		opt.value = String(f);
		opt.textContent = `${f} fps`;
		selFps.appendChild(opt);
	}
}

$("btn-apply").addEventListener("click", async () => {
	const [w, h] = $("sel-res").value.split("x").map(Number);
	const fps = Number($("sel-fps").value);
	try {
		await api(`/api/video/resolution?width=${w}&height=${h}&fps=${fps}`, { method: "POST" });
		reloadStream(); // soft-reload: fresh MJPEG connection at the new mode
		pollStatus();
	} catch (e) {
		alert(`Apply failed: ${e.message}`);
	}
});

loadStreamMenu();

const videoImg = $("video");
const stage = $("stage");

// The cache-busting stamp makes img.src abort the old multipart connection; the
// server's single-client takeover then hands over to the new one.
function streamURL() {
	return "/api/video/stream?_=" + Date.now();
}

// Reload the stream after Apply so the new mode shows without a page refresh.
function reloadStream() {
	videoImg.src = streamURL();
}

videoImg.src = streamURL();

// Re-attach on stream error, at most one retry per 1500 ms.
let lastStreamReattach = 0;
videoImg.onerror = () => {
	const now = Date.now();
	if (now - lastStreamReattach < 1500) return;
	lastStreamReattach = now;
	setTimeout(() => {
		videoImg.src = streamURL();
	}, 1500);
};

/* Fullscreen stretches the stream to the viewport through the :fullscreen CSS rules. */
$("btn-fullscreen").addEventListener("click", () => {
	if (document.fullscreenElement) document.exitFullscreen();
	else stage.requestFullscreen().catch(() => {});
});

$("btn-screenshot").addEventListener("click", async () => {
	try {
		const res = await fetch("/api/video/screenshot");
		if (res.status === 401 && location.pathname !== "/login") {
			location.href = "/login";
			throw new Error("session expired, please log in");
		}
		const blob = await res.blob();
		const a = document.createElement("a");
		a.href = URL.createObjectURL(blob);
		a.download = `kvmostb-${Date.now()}.jpg`;
		a.click();
		URL.revokeObjectURL(a.href);
	} catch (e) {
		alert(`Screenshot failed: ${e.message}`);
	}
});

const EVT = {
	KEY_PRESS: 0,
	KEY_RELEASE: 1,
	MOUSE_MOVE: 2,
	MOUSE_PRESS: 3,
	MOUSE_RELEASE: 4,
	MOUSE_SCROLL: 5,
};

let hidSocket = null;
let hidReconnectDelay = 500;

function hidConnect() {
	const proto = location.protocol === "https:" ? "wss:" : "ws:";
	hidSocket = new WebSocket(`${proto}//${location.host}/ws/hid`);

	hidSocket.onopen = () => {
		hidReconnectDelay = 500;
		setChip($("chip-hid"), true, "online");
	};
	hidSocket.onclose = () => {
		setChip($("chip-hid"), false, "offline");
		setTimeout(hidConnect, hidReconnectDelay);
		hidReconnectDelay = Math.min(hidReconnectDelay * 2, 5000);
	};
	hidSocket.onerror = () => hidSocket.close();
}

function sendHID(payload) {
	if (hidSocket && hidSocket.readyState === WebSocket.OPEN) {
		hidSocket.send(JSON.stringify(payload));
	}
}

hidConnect();

// Mouse button mask carried on move events: bit0 left, bit1 right, bit2 middle.
const BTN = { LEFT: 0x01, RIGHT: 0x02, MIDDLE: 0x04 };

const mouse = {
	buttons: 0x00,
	x: 2048,
	y: 2048,
};

function isTypingInInput(e) {
	const t = e.target;
	return t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" ||
		t.tagName === "SELECT" || t.isContentEditable);
}

window.addEventListener("keydown", (e) => {
	if (isTypingInInput(e)) return;
	// Escape stays with the browser so fullscreen can be exited.
	if (e.key === "Escape") return;
	e.preventDefault();
	const isRight = e.location === KeyboardEvent.DOM_KEY_LOCATION_RIGHT ||
		e.location === KeyboardEvent.DOM_KEY_LOCATION_NUMPAD;
	if (e.key === "Shift") { shiftHeld = true; applyKbdCase(); }
	if (e.key === "CapsLock" && !e.repeat) { capsOn = !capsOn; applyKbdCase(); }
	sendHID({
		event: EVT.KEY_PRESS,
		keycode: e.keyCode,
		is_right_modifier_key: isRight,
	});
});

window.addEventListener("keyup", (e) => {
	if (isTypingInInput(e)) return;
	e.preventDefault();
	const isRight = e.location === KeyboardEvent.DOM_KEY_LOCATION_RIGHT ||
		e.location === KeyboardEvent.DOM_KEY_LOCATION_NUMPAD;
	if (e.key === "Shift") { shiftHeld = false; applyKbdCase(); }
	sendHID({
		event: EVT.KEY_RELEASE,
		keycode: e.keyCode,
		is_right_modifier_key: isRight,
	});
});

// Virtual keyboard. Key codes are the legacy DOM keyCodes the physical-key
// handler sends, so the backend cannot tell the two apart.

// [label, keyCode, widthUnits?, isRightModifier?]
// An empty label is a transparent spacer; code 0 is a no-op key (Fn has no HID report).
// Rows total 18.3 key units so they share the left edge. Row 0 groups the F keys
// (Esc | F1-F4 | F5-F8 | F9-F12) with 0.4u spacers, and F12 keeps the normal key
// gap to Prnt so the cluster starts exactly above the edit block.
const KBD_ROWS = [
	[["Esc",27,1.2],["",0,0.4],["F1",112,1.05],["F2",113,1.05],["F3",114,1.05],["F4",115,1.05],
	 ["",0,0.4],["F5",116,1.05],["F6",117,1.05],["F7",118,1.05],["F8",119,1.05],
	 ["",0,0.4],["F9",120,1.05],["F10",121,1.05],["F11",122,1.05],["F12",123,1.05],
	 ["Prnt",44,1.1],["ScrLk",145,1.1],["Pause",19,1.1]],
	[["`",192],["1",49],["2",50],["3",51],["4",52],["5",53],["6",54],["7",55],
	 ["8",56],["9",57],["0",48],["-",189],["=",187],["Backspace",8,2],
	 ["Ins",45,1.1],["Home",36,1.1],["PgUp",33,1.1]],
	[["Tab",9,1.5],["Q",81],["W",87],["E",69],["R",82],["T",84],["Y",89],["U",85],
	 ["I",73],["O",79],["P",80],["[",219],["]",221],["\\",220,1.5],
	 ["Del",46,1.1],["End",35,1.1],["PgDn",34,1.1]],
	[["Caps",20,1.8],["A",65],["S",83],["D",68],["F",70],["G",71],["H",72],
	 ["J",74],["K",75],["L",76],[";",186],["'",222],["Enter",13,2.2],["",0,3.3]],
	[["Shift",16,2.5],["Z",90],["X",88],["C",67],["V",86],["B",66],["N",78],
	 ["M",77],[",",188],[".",190],["/",191],["Shift",16,2.5,1],["",0,1.1],["\u2191",38,1.1],["",0,1.1]],
	[["Ctrl",17,1.25],["\u229E",91,1.25],["Alt",18,1.25],["Space",32,7.5],
	 ["Alt",18,1.25,1],["Fn",0,1.25],["Ctrl",17,1.25,1],
	 ["\u2190",37,1.1],["\u2193",40,1.1],["\u2192",39,1.1]]
];


/* Letter-key case display: uppercase when (Shift held) XOR (CapsLock on)
   - mirrors a real keyboard. The guest's caps state is mirrored locally:
   virtual Caps press and host CapsLock press each toggle both sides. */
let shiftHeld = false;
let capsOn = false;
// Ctrl and Alt are sticky: a tap holds the key on the guest, a second tap or the
// next normal key releases it, so Ctrl+Alt+Del works as three taps.
const stickyMods = {};
const STICKY_CODES = new Set([17, 18]);
function releaseStickyMods() {
	for (const code in stickyMods) {
		const m = stickyMods[code];
		delete stickyMods[code];
		m.key.classList.remove("is-down");
		m.key.setAttribute("aria-pressed", "false");
		sendHID({ event: EVT.KEY_RELEASE, keycode: Number(code), is_right_modifier_key: m.right });
	}
}
function applyKbdCase() {
	const upper = shiftHeld !== capsOn;
	document.querySelectorAll("#kbd .kbd-key[data-case]").forEach((k) => {
		k.textContent = upper ? k.dataset.upper : k.dataset.lower;
	});
}

function buildVirtualKeyboard() {
	const wrap = $("kbd");
	if (!wrap) return;
	for (const row of KBD_ROWS) {
		const rowEl = document.createElement("div");
		rowEl.className = "kbd-row";
		for (const [label, code, units, right] of row) {
			const key = label
				? document.createElement("button")
				: document.createElement("div"); // "" = layout spacer
			key.className = label ? "kbd-key" : "kbd-key kbd-key--spacer";
			if (label) {
				key.type = "button";
				key.textContent = label;
				key.setAttribute("aria-label", "Virtual key " + label);
				if (/^[A-Z]$/.test(label)) {
					key.dataset.case = "1";
					key.dataset.upper = label;
					key.dataset.lower = label.toLowerCase();
				}
			}
			if (units && units !== 1) {
				key.style.setProperty(
					"--kbd-w",
					"calc(var(--kbd-u) * " + units + " + var(--kbd-g) * " + (units - 1) + ")");
			}
			if (!label || !code) {
				rowEl.appendChild(key);
				continue; // spacer or no-op key: no HID frame
			}
			key.addEventListener("pointerdown", (e) => {
				e.preventDefault();
				if (STICKY_CODES.has(code)) {
						const held = key.classList.contains("is-down");
					key.classList.toggle("is-down", !held);
					key.setAttribute("aria-pressed", String(!held));
					sendHID({
						event: held ? EVT.KEY_RELEASE : EVT.KEY_PRESS,
						keycode: code,
						is_right_modifier_key: !!right,
					});
					if (held) delete stickyMods[code];
					else stickyMods[code] = { key, right: !!right };
					return;
				}
				key.setPointerCapture(e.pointerId);
				key.classList.add("is-down");
				sendHID({ event: EVT.KEY_PRESS, keycode: code, is_right_modifier_key: !!right });
				if (code === 16) { shiftHeld = true; applyKbdCase(); }
				else if (code === 20) { capsOn = !capsOn; applyKbdCase(); }
				releaseStickyMods(); // a normal key consumes Ctrl/Alt (Ctrl+Alt+Del)
			});
			const release = () => {
				if (!key.classList.contains("is-down")) return;
				// pointerup must not release sticky modifiers: a click is down plus up and
				// has to leave them held.
				if (STICKY_CODES.has(code)) return;
				key.classList.remove("is-down");
				sendHID({ event: EVT.KEY_RELEASE, keycode: code, is_right_modifier_key: !!right });
				if (code === 16) { shiftHeld = false; applyKbdCase(); }
			};
			key.addEventListener("pointerup", release);
			key.addEventListener("pointercancel", release);
			rowEl.appendChild(key);
		}
		wrap.appendChild(rowEl);
	}
	applyKbdCase();
}

buildVirtualKeyboard();

// KEYBOARD dropdown: the panel only renders while the dropdown is open
// (panel CSS: .kbd display:none, .keyboard-card.is-open .kbd display:flex).
const kbdToggle = $("kbd-toggle");
const keyboardCard = $("keyboard-card");
if (kbdToggle && keyboardCard) {
	kbdToggle.addEventListener("click", () => {
		const open = keyboardCard.classList.toggle("is-open");
		kbdToggle.setAttribute("aria-expanded", String(open));
	});
}

// CLIPBOARD dropdown: same collapse pattern as the keyboard card.
const clipToggle = $("clip-toggle");
const clipboardCard = $("clipboard-card");
if (clipToggle && clipboardCard) {
	clipToggle.addEventListener("click", () => {
		const open = clipboardCard.classList.toggle("is-open");
		clipToggle.setAttribute("aria-expanded", String(open));
	});
}

// Stuck-key recovery when the tab loses focus
window.addEventListener("blur", () => {
	releaseStickyMods();
	mouse.buttons = 0x00;
	sendHID({ event: EVT.MOUSE_MOVE, mouse_x: mouse.x, mouse_y: mouse.y, mouse_move_button_state: 0 });
	api("/api/hid/reset", { method: "POST" }).catch(() => {});
});

// char -> [legacy keyCode, needsShift] for the US base layout.
const CHAR_CODES = {
	"a":[65,0],"b":[66,0],"c":[67,0],"d":[68,0],"e":[69,0],"f":[70,0],"g":[71,0],
	"h":[72,0],"i":[73,0],"j":[74,0],"k":[75,0],"l":[76,0],"m":[77,0],"n":[78,0],
	"o":[79,0],"p":[80,0],"q":[81,0],"r":[82,0],"s":[83,0],"t":[84,0],"u":[85,0],
	"v":[86,0],"w":[87,0],"x":[88,0],"y":[89,0],"z":[90,0],
	"0":[48,0],"1":[49,0],"2":[50,0],"3":[51,0],"4":[52,0],"5":[53,0],
	"6":[54,0],"7":[55,0],"8":[56,0],"9":[57,0],
	" ": [32,0], "\t": [9,0], "\n": [13,0],
	"`":[192,0],"-":[189,0],"=":[187,0],"\\":[220,0],"[": [219,0],
	"]":[221,0],";":[186,0],"/":[191,0],",":[188,0],".":[190,0],
	"~":[192,1],"_":[189,1],"+":[187,1],"|":[220,1],"{":[219,1],
	"}":[221,1],":":[186,1],'"': [222,1],">":[190,1],"?":[191,1],"<":[188,1],
	"!":[49,1],"@":[50,1],"#":[51,1],"$":[52,1],"%":[53,1],"^":[54,1],
	"&":[55,1],"*":[56,1],"(":[57,1],")":[48,1],
};
// Uppercase A-Z always needs a shift frame on this client (caps state is
// mirrored in capsOn, same rule as the virtual keyboard).
async function typeText(text) {
	// The guest polls the gadget at a fixed rate, one report per poll; a faster burst
	// overflows the 16-slot writer queue and drops frames, so pace at 15 ms.
	const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
	let sent = 0, skipped = 0;
	for (const ch of text) {
		const low = ch.toLowerCase();
		const up = ch.toUpperCase();
		let spec = CHAR_CODES[low] || CHAR_CODES[ch];
		if (!spec) { skipped++; continue; }
		let code = spec[0], shift = spec[1];
		if (ch === up && ch !== low) shift = 1; // explicit uppercase letter
		shift = shift || (capsOn && !spec[1] && /[a-z0-9]/.test(ch) && up === ch);
		// caps on inverts the case of letters/digits: shift to cancel it
		if (capsOn && /^[a-z]$/.test(ch)) shift = !shift;
		if (shift) {
			sendHID({ event: EVT.KEY_PRESS, keycode: 16, is_right_modifier_key: false });
			await sleep(15);
		}
		sendHID({ event: EVT.KEY_PRESS, keycode: code, is_right_modifier_key: false });
		await sleep(15);
		sendHID({ event: EVT.KEY_RELEASE, keycode: code, is_right_modifier_key: false });
		await sleep(15);
		if (shift) {
			sendHID({ event: EVT.KEY_RELEASE, keycode: 16, is_right_modifier_key: false });
			await sleep(15);
		}
		sent++;
	}
	return { sent, skipped };
}

const clipText = $("clip-text");
const clipStatus = $("clip-status");
const btnClipPaste = $("btn-clip-paste");
if (btnClipPaste) {
	btnClipPaste.addEventListener("click", async () => {
		let text = "";
		// The Clipboard API needs a secure context; on plain HTTP fall back to the textarea.
		try { text = await navigator.clipboard.readText(); } catch (e) { text = ""; }
		if (!text) text = clipText ? clipText.value : "";
		if (!text) {
			if (clipStatus) clipStatus.textContent = "clipboard is empty";
			return;
		}
		if (clipStatus) clipStatus.textContent = "typing...";
		const { sent, skipped } = await typeText(text);
		if (clipStatus) {
			clipStatus.textContent = sent + " char" + (sent === 1 ? "" : "s") + " sent" +
				(skipped ? " (" + skipped + " unsupported skipped)" : "");
		}
	});
}

// Content rectangle of the video, accounting for object-fit:contain
// letterboxing so the cursor maps 1:1 to the target screen.
function contentRect() {
	const r = videoImg.getBoundingClientRect();
	const iw = videoImg.naturalWidth;
	const ih = videoImg.naturalHeight;
	if (videoImg.classList.contains("is-stretch") || !iw || !ih) return r;
	const scale = Math.min(r.width / iw, r.height / ih);
	const w = iw * scale;
	const h = ih * scale;
	return {
		left: r.left + (r.width - w) / 2,
		top: r.top + (r.height - h) / 2,
		width: w,
		height: h,
	};
}

function toWebCoords(e) {
	const r = contentRect();
	const x = Math.max(1, Math.min(4095, Math.round(((e.clientX - r.left) / r.width) * 4095)));
	const y = Math.max(1, Math.min(4095, Math.round(((e.clientY - r.top) / r.height) * 4095)));
	return { x, y };
}

const MOUSE_MOVE_INTERVAL_MS = 20;
let lastMouseMoveSent = 0;
let pendingMove = null;
let pendingMoveTimer = null;

function queueMouseMove(x, y) {
	mouse.x = x;
	mouse.y = y;
	const now = Date.now();
	if (now - lastMouseMoveSent >= MOUSE_MOVE_INTERVAL_MS) {
		lastMouseMoveSent = now;
		sendHID({ event: EVT.MOUSE_MOVE, mouse_x: x, mouse_y: y, mouse_move_button_state: mouse.buttons });
		pendingMove = null;
	} else {
		pendingMove = { x, y };
		if (!pendingMoveTimer) {
			pendingMoveTimer = setTimeout(flushPendingMove, MOUSE_MOVE_INTERVAL_MS);
		}
	}
}

function flushPendingMove() {
	pendingMoveTimer = null;
	if (!pendingMove) return;
	lastMouseMoveSent = Date.now();
	sendHID({ event: EVT.MOUSE_MOVE, mouse_x: pendingMove.x, mouse_y: pendingMove.y, mouse_move_button_state: mouse.buttons });
	pendingMove = null;
}

// A click is a move frame carrying the button mask; the backend re-syncs its
// button state from every move.
function sendMoveWithButtons() {
	sendHID({ event: EVT.MOUSE_MOVE, mouse_x: mouse.x, mouse_y: mouse.y, mouse_move_button_state: mouse.buttons });
}

stage.addEventListener("mousemove", (e) => {
	const { x, y } = toWebCoords(e);
	queueMouseMove(x, y);
});

stage.addEventListener("mousedown", (e) => {
	e.preventDefault();
	flushPendingMove();
	if (e.button === 0) mouse.buttons |= BTN.LEFT;
	else if (e.button === 2) mouse.buttons |= BTN.RIGHT;
	else if (e.button === 1) mouse.buttons |= BTN.MIDDLE;
	const { x, y } = toWebCoords(e);
	mouse.x = x;
	mouse.y = y;
	sendMoveWithButtons();
});

window.addEventListener("mouseup", (e) => {
	if (e.button === 0) mouse.buttons &= ~BTN.LEFT;
	else if (e.button === 2) mouse.buttons &= ~BTN.RIGHT;
	else if (e.button === 1) mouse.buttons &= ~BTN.MIDDLE;
	sendMoveWithButtons();
});

stage.addEventListener("contextmenu", (e) => e.preventDefault());

// Wheel: natural direction (up = positive HID tilt); the backend applies
// the wheel sensitivity step.
const SCROLL_STEP = 2;
stage.addEventListener("wheel", (e) => {
	e.preventDefault();
	const tilt = e.deltaY < 0 ? SCROLL_STEP : -SCROLL_STEP;
	sendHID({ event: EVT.MOUSE_SCROLL, mouse_scroll: tilt });
}, { passive: false });

$("btn-reset").addEventListener("click", async () => {
	try {
		await api("/api/hid/reset", { method: "POST" });
		mouse.buttons = 0x00;
		sendMoveWithButtons();
		showError($("input-error"), null);
	} catch (e) {
		showError($("input-error"), `Refresh: ${e.message}`);
	}
});

async function loadISOList() {
	try {
		const list = await api("/api/iso/list");
		const sel = $("sel-iso");
		sel.innerHTML = "";
		if (!Array.isArray(list) || list.length === 0) {
			const opt = document.createElement("option");
			opt.value = "";
			opt.textContent = "(no ISO files found)";
			sel.appendChild(opt);
			return;
		}
		for (const iso of list) {
			const opt = document.createElement("option");
			opt.value = iso.path;
			opt.textContent = `${iso.name}  [${iso.source}, ${(iso.size / 1048576).toFixed(1)} MB]`;
			sel.appendChild(opt);
		}
	} catch (e) {
		showError($("iso-error"), e.message);
	}
}

$("btn-iso-refresh").addEventListener("click", () => loadISOList());
loadISOList();
setInterval(loadISOList, 30000);

/* One toggle button: the label follows the mount state from /api/status. */
function isoMounted() {
	const cur = $("iso-current").textContent;
	return cur !== "" && cur !== "(none)";
}

function setISOButton() {
	const label = isoMounted() ? "Unmount" : "Mount";
	$("iso-switch-label").textContent = label;
	$("iso-switch").setAttribute(
		"aria-label", "Virtual media: " + label + " (click to toggle mount/unmount)");
}

$("iso-switch").addEventListener("click", async () => {
	if (isoMounted()) {
		try {
			await api("/api/iso/mount", { method: "POST" });
			showError($("iso-error"), null);
		} catch (e) {
			showError($("iso-error"), `Unmount: ${e.message}`);
		}
	} else {
		const file = $("sel-iso").value;
		if (!file) {
			showError($("iso-error"), "No ISO selected");
			return;
		}
		try {
			await api(`/api/iso/mount?file=${encodeURIComponent(file)}`, { method: "POST" });
			showError($("iso-error"), null);
		} catch (e) {
			showError($("iso-error"), `Mount: ${e.message}`);
		}
	}
	pollStatus();
});

$("btn-logout").addEventListener("click", async () => {
	// Best-effort: drop the session server-side, then to the login page
	// either way (the redirect is what matters even if the request fails).
	await fetch("/api/logout", { method: "POST" }).catch(() => {});
	location.href = "/login";
});

const themeToggle = $("theme-toggle");
function syncThemeUI() {
	const dark = document.documentElement.getAttribute("data-theme") === "dark";
	themeToggle.setAttribute("aria-checked", String(dark));
}
themeToggle.addEventListener("click", () => {
	const dark = document.documentElement.getAttribute("data-theme") === "dark";
	if (dark) {
		document.documentElement.removeAttribute("data-theme");
		localStorage.setItem("vq-theme", "light");
	} else {
		document.documentElement.setAttribute("data-theme", "dark");
		localStorage.setItem("vq-theme", "dark");
	}
	syncThemeUI();
});
syncThemeUI();
