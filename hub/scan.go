package main

// Scanning a join QR code, and showing one.
//
// Two QR codes come up when adding to the mesh:
//
//   - A phone's join request. The Nostr VPN app shows it under Join Network:
//     the same nvpn://join-request/… link it copies and shares (join.go).
//   - A second box's ID, which that box's setup shows as nvpn://device/<npub>.
//     The Android app's own scanner reads that form too (its
//     parseScannedDeviceUri: scheme nvpn, the npub as the last path segment),
//     so a phone that runs the network can scan a box just as this page can.
//
// Every "Scan QR code" button runs scanJS. Whatever it reads, it posts to the
// same routes a paste does, without confirm, so what comes back is the review
// page that names the device, and the Add button there is the approval. A scan
// never adds anything on its own: that is the hole a deep link that approves
// on open would be. The routes keep their own guards (POST, Host, origin,
// CSRF token, admin only), and the button carries this page's token like the
// paste form does.
//
// The button takes a photo rather than running a live camera. A file input
// with capture="environment" opens the phone's camera app on any page, plain
// http included (home network, mesh, VPS), where a live camera would be
// refused; on a computer it is a file picker. jsQR reads the code from the
// picture in the page, so the photo never leaves the browser.
//
// Both QR libraries are vendored (qrvendor_*.go) and served from the hub, so
// nothing is fetched from anywhere else.

import (
	"net/http"
	"strings"
	"time"
)

// deviceQRPrefix is the form a box shows its ID in. parseBoxID accepts it.
const deviceQRPrefix = "nvpn://device/"

var assetTime = time.Now()

// serveScript serves one of the hub's own scripts. It is behind the login like
// every other route.
func serveScript(name, src string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, name, assetTime, strings.NewReader(src))
	}
}

// scanJS drives every element with data-scan. The button carries the page's
// CSRF token (data-csrf) and where to return (data-from). It runs once per
// page however many cards include it, and listens on the document, so a
// button rendered after the first copy of the script still works.
const scanJS = `(function () {
  'use strict';
  function reveal() {
    document.querySelectorAll('[data-scan]').forEach(function (b) { b.hidden = false; });
  }
  reveal();
  if (window.sdnsScan) return;
  window.sdnsScan = true;
  document.addEventListener('DOMContentLoaded', reveal);

  var JOIN = 'nvpn://join-request/';
  var DEVICE = 'nvpn://device/';
  var BOXCODE = 'sdns://join/';
  var codeRe = /^npub1[02-9ac-hj-np-z]{58}:[A-Za-z0-9_-]{1,64}$/;

  // A first box's join code (boxCardHTML), from its QR code. Mirrors
  // parseJoinCode in boxjoin.go; the server checks again.
  function joinCode(v) {
    var s = v.trim();
    if (s.slice(0, BOXCODE.length).toLowerCase() === BOXCODE) s = s.slice(BOXCODE.length);
    return codeRe.test(s) ? s : '';
  }
  var npubRe = /^npub1[02-9ac-hj-np-z]{58}$/;

  // Mirrors parseBoxID in boxjoin.go; the server checks again.
  function deviceId(v) {
    var s = v.trim();
    if (s.slice(0, DEVICE.length).toLowerCase() === DEVICE) {
      s = s.slice(DEVICE.length).split(/[?#]/)[0];
    } else if (s.slice(0, 6).toLowerCase() === 'nostr:') {
      s = s.slice(6);
    }
    s = s.trim().toLowerCase();
    return npubRe.test(s) ? s : '';
  }

  function post(action, fields) {
    var f = document.createElement('form');
    f.method = 'post';
    f.action = action;
    Object.keys(fields).forEach(function (k) {
      var i = document.createElement('input');
      i.type = 'hidden'; i.name = k; i.value = fields[k];
      f.appendChild(i);
    });
    document.body.appendChild(f);
    f.submit();
  }

  // Sends what was read to the review page. False when it is neither kind.
  // A button with data-fill only takes a first box's join code, and puts it
  // in that field for the reader to check and send with Join.
  function route(raw, btn) {
    var v = String(raw || '').trim();
    if (btn.dataset.fill) {
      var code = joinCode(v);
      var field = document.getElementById(btn.dataset.fill);
      if (!code || !field) return false;
      field.value = code;
      return 'filled';
    }
    var base = {csrf: btn.dataset.csrf || '', from: btn.dataset.from || ''};
    if (v.slice(0, JOIN.length).toLowerCase() === JOIN && v.length > JOIN.length) {
      base.request = v;
      post('/devices/add', base);
      return true;
    }
    if (deviceId(v)) {
      base.box = v;
      post('/devices/add-box', base);
      return true;
    }
    return false;
  }

  var jsqr;
  function loadJsQR() {
    if (!jsqr) {
      jsqr = new Promise(function (resolve, reject) {
        if (window.jsQR) return resolve(window.jsQR);
        var s = document.createElement('script');
        s.src = '/assets/jsqr.js';
        s.onload = function () { window.jsQR ? resolve(window.jsQR) : reject(new Error('no jsQR')); };
        s.onerror = function () { jsqr = null; reject(new Error('jsQR did not load')); };
        document.head.appendChild(s);
      });
    }
    return jsqr;
  }

  // Reads a QR code from anything drawable, scaled so its long side is at
  // most max pixels. Light-on-dark codes (a phone in dark mode) read too.
  var canvas = document.createElement('canvas');
  function decode(decoder, src, w, h, max) {
    var k = Math.min(1, max / Math.max(w, h));
    canvas.width = Math.max(1, Math.round(w * k));
    canvas.height = Math.max(1, Math.round(h * k));
    var ctx = canvas.getContext('2d', {willReadFrequently: true});
    ctx.drawImage(src, 0, 0, canvas.width, canvas.height);
    var img = ctx.getImageData(0, 0, canvas.width, canvas.height);
    var code = decoder(img.data, img.width, img.height, {inversionAttempts: 'attemptBoth'});
    return code && code.data ? code.data : '';
  }

  var css = '.scan-sheet{position:fixed;inset:0;z-index:50;background:rgba(5,7,10,.86);display:flex;align-items:center;justify-content:center;padding:16px}' +
    '.scan-sheet[hidden]{display:none}' +
    '.scan-panel{background:#171a21;border:1px solid #262b36;border-radius:12px;padding:18px 20px;max-width:440px;width:100%;color:#e6e9ef}' +
    '.scan-panel h2{font-size:19px;margin:0 0 10px}' +
    '.scan-panel .scan-msg{margin:0 0 12px;font-size:15px}' +
    '.scan-panel .scan-msg.warn{border-left:3px solid #e8a33d;padding:6px 10px;background:#1e1a12;border-radius:0 8px 8px 0}' +
    '.scan-panel .scan-row{display:flex;gap:10px;flex-wrap:wrap}' +
    '.scan-panel .btn{font:inherit;font-size:14px;font-weight:600;border-radius:8px;padding:9px 16px;cursor:pointer;border:1px solid #262b36;background:#1f2430;color:#e6e9ef;display:inline-block}' +
    '.scan-panel .btn.primary{background:#6ea8fe;border-color:#6ea8fe;color:#0b1220}';

  // One hidden file input serves every button. Clicking a Scan button opens
  // it straight away: the phone's camera, or a file picker on a computer.
  // The sheet only appears to say what happened with the photo.
  var photo, sheet, msg, btn;

  function input() {
    if (photo) return photo;
    photo = document.createElement('input');
    photo.type = 'file';
    photo.accept = 'image/*';
    photo.setAttribute('capture', 'environment');
    photo.hidden = true;
    photo.addEventListener('change', readPhoto);
    document.body.appendChild(photo);
    return photo;
  }

  function build() {
    var st = document.createElement('style');
    st.textContent = css;
    document.head.appendChild(st);
    sheet = document.createElement('div');
    sheet.className = 'scan-sheet';
    sheet.hidden = true;
    sheet.setAttribute('role', 'dialog');
    sheet.setAttribute('aria-modal', 'true');
    sheet.setAttribute('aria-label', 'Scan a join QR code');
    sheet.innerHTML = '<div class="scan-panel"><h2>Scan a join QR code</h2>' +
      '<p class="scan-msg" role="status"></p>' +
      '<div class="scan-row">' +
      '<button class="btn primary scan-again" type="button">Take another photo</button>' +
      '<button class="btn scan-close" type="button">Cancel</button></div></div>';
    document.body.appendChild(sheet);
    msg = sheet.querySelector('.scan-msg');
    sheet.querySelector('.scan-again').addEventListener('click', function () { input().click(); });
    sheet.querySelector('.scan-close').addEventListener('click', close);
    sheet.addEventListener('keydown', function (e) { if (e.key === 'Escape') close(); });
  }

  function say(text, warn) {
    if (!sheet) build();
    sheet.hidden = false;
    msg.textContent = text;
    msg.className = 'scan-msg' + (warn ? ' warn' : '');
  }

  function close() {
    if (sheet) sheet.hidden = true;
    if (btn) btn.focus();
  }

  function found(value) {
    var routed = route(value, btn);
    if (routed === 'filled') {
      say('Found the first box\'s join code. Check it below and press Join.');
      return;
    }
    if (routed) {
      say('Found it. Opening the review…');
      return;
    }
    if (btn.dataset.fill) {
      say('That QR code isn\'t a first box\'s join code. Photograph the one under Add another DNS box on the first box.', true);
      return;
    }
    say('That QR code isn\'t a Nostr VPN join code. Photograph the one under Join Network in the app, or on the new box\'s setup page.', true);
  }

  function readPhoto() {
    var file = photo.files && photo.files[0];
    photo.value = '';
    if (!file) return;
    say('Reading the photo…');
    var img = new Image();
    var url = URL.createObjectURL(file);
    img.onload = function () {
      loadJsQR().then(function (decoder) {
        var sizes = [1024, 640, 1600, 2400];
        for (var i = 0; i < sizes.length; i++) {
          var v = decode(decoder, img, img.naturalWidth, img.naturalHeight, sizes[i]);
          if (v) { URL.revokeObjectURL(url); found(v); return; }
        }
        URL.revokeObjectURL(url);
        say('No QR code found in that photo. Try again with the code filling most of the picture, or paste the link instead.', true);
      }, function () {
        URL.revokeObjectURL(url);
        say('The QR reader didn\'t load. Reload the page, or paste the link instead.', true);
      });
    };
    img.onerror = function () {
      URL.revokeObjectURL(url);
      say('That photo couldn\'t be opened. Try again, or paste the link instead.', true);
    };
    img.src = url;
  }

  function open(b) {
    btn = b;
    loadJsQR().catch(function () {});
    input().click();
  }

  document.addEventListener('click', function (e) {
    var b = e.target.closest && e.target.closest('[data-scan]');
    if (!b) return;
    e.preventDefault();
    open(b);
  });
})();
`
