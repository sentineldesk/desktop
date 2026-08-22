// SentinelDesk
// A collaborative operating system for people and AI agents.
//
// Copyright 2026 Federico Pereira <fpereira@cnsoluciones.com>
//
// Licensed under the Apache License, Version 2.0.
//
// This product's name and logo are trademarks of Federico Pereira and are not
// covered by the license above. See the README for the trademark policy.
//
// SPDX-License-Identifier: Apache-2.0

package stream

// The page at /logs, as a string in this package rather than a file in
// internal/webui.
//
// It looks like it belongs beside the rest of the client, and there is one
// reason it does not: this page has to work when the desktop client will not.
// It is opened from a link an agent pasted into a chat, on a phone, by somebody
// who wants to know what a command printed — with no WebRTC session, no video
// decoder and possibly no room slot left to give them. Building it out of the
// client's modules would make reading a log depend on the machinery for watching
// a screen, and the moment somebody most wants the log is the moment that
// machinery is least likely to be working.
//
// So it is deliberately one file with no imports, no build step and no shared
// state: fetch three JSON endpoints and print them.
//
// # The login in here is not a second gate
//
// It is the SAME gate. The page has no password of its own and no endpoint to
// post one to — there is deliberately no /login in this daemon — so when it
// needs a token it opens the WebSocket, sends the one frame the protocol
// expects, takes the token out of the reply and closes the socket. Every
// credential still travels through the one door in internal/stream/session.go,
// including the rate limiting and the ban ledger in front of it.
//
// The cost, stated so nobody discovers it as a bug: closing the socket that
// early means the daemon briefly creates and tears down a session, the same way
// it does for anybody who closes a tab mid-handshake. The alternative was
// holding the connection open, which would spend one of MAX_VIEWERS slots for as
// long as somebody left a log open in a tab.
//
// The token is kept in sessionStorage under the same key the desktop client
// uses, so a tab that already has one never asks. sessionStorage and not
// localStorage, matching client.js: a credential that survives closing the tab
// is a credential somebody has to remember to revoke.
const logViewerHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>SentinelDesk — logs</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  body { margin: 0; background: #0e1116; color: #d7dde5;
         font: 14px/1.5 -apple-system, "Segoe UI", Roboto, sans-serif; }
  header { display: flex; gap: 12px; align-items: baseline; flex-wrap: wrap;
           padding: 14px 18px; border-bottom: 1px solid #232a33; background: #131820; }
  h1 { font-size: 15px; margin: 0; font-weight: 600; letter-spacing: .3px; }
  .sub { color: #7d8794; font-size: 12px; }
  nav { display: flex; gap: 6px; padding: 10px 18px; flex-wrap: wrap;
        border-bottom: 1px solid #232a33; }
  button.tab { background: #1a212b; color: #b8c2ce; border: 1px solid #2b3441;
               border-radius: 6px; padding: 5px 11px; cursor: pointer; font: inherit; }
  button.tab[aria-selected="true"] { background: #2d5fa8; border-color: #2d5fa8; color: #fff; }
  main { padding: 18px; }
  pre { background: #0a0d12; border: 1px solid #232a33; border-radius: 6px;
        padding: 12px; overflow-x: auto; white-space: pre-wrap; word-break: break-word;
        margin: 0 0 16px; max-height: 60vh; overflow-y: auto; }
  h2 { font-size: 13px; text-transform: uppercase; letter-spacing: .6px;
       color: #8b96a4; margin: 18px 0 8px; }
  table { border-collapse: collapse; width: 100%; font-size: 13px; }
  td, th { padding: 5px 8px; border-bottom: 1px solid #1c232c; vertical-align: top;
           text-align: left; }
  th { color: #8b96a4; font-weight: 600; }
  td.mono, .mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
  td.detail { word-break: break-word; }
  .who-terminal { color: #7fd18b; } .who-person { color: #7fd18b; }
  .who-agent { color: #7fb6f0; }
  .bad { color: #f0857f; } .ok { color: #7fd18b; } .warn { color: #e8c07d; }
  .empty { color: #7d8794; font-style: italic; }
  form.login { max-width: 320px; display: grid; gap: 10px; }
  input { background: #0a0d12; border: 1px solid #2b3441; border-radius: 6px;
          color: inherit; padding: 8px; font: inherit; }
  input[type=submit] { background: #2d5fa8; border-color: #2d5fa8; color: #fff;
                       cursor: pointer; }
  a { color: #7fb6f0; }
  .jobs li { margin-bottom: 4px; }
  ul { padding-left: 18px; margin: 0 0 16px; }
</style>
</head>
<body>
<header>
  <h1>SentinelDesk — logs</h1>
  <span class="sub" id="where"></span>
</header>
<nav id="tabs" hidden>
  <button class="tab" data-view="job" hidden>this job</button>
  <button class="tab" data-view="jobs">what the agent ran</button>
  <button class="tab" data-view="agent">agent tool calls</button>
  <button class="tab" data-view="people">what the people here ran</button>
  <button class="tab" data-view="refresh">refresh</button>
</nav>
<main id="main">loading…</main>

<script>
(function () {
  'use strict';

  var TOKEN_KEY = 'sentineldesk_token';
  var main = document.getElementById('main');
  var tabs = document.getElementById('tabs');
  var jobId = new URLSearchParams(location.search).get('job') || '';
  var view = jobId ? 'job' : 'jobs';

  document.getElementById('where').textContent = location.host;

  function token() { try { return sessionStorage.getItem(TOKEN_KEY) || ''; } catch (e) { return ''; } }
  function keep(t) { try { sessionStorage.setItem(TOKEN_KEY, t); } catch (e) {} }
  function forget() { try { sessionStorage.removeItem(TOKEN_KEY); } catch (e) {} }

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"]/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c];
    });
  }

  // Every request carries the session token in a header, never in the URL. The
  // URL of this page gets pasted into chats.
  function api(path) {
    return fetch(path, { headers: { 'X-SentinelDesk-Token': token() },
                         cache: 'no-store' })
      .then(function (r) {
        if (r.status === 401) { forget(); var e = new Error('unauthorized'); e.auth = true; throw e; }
        return r.json().then(function (body) {
          if (!r.ok) { throw new Error(body.error || ('HTTP ' + r.status)); }
          return body;
        });
      });
  }

  // The WebSocket is the only authentication gate in this daemon, so this is
  // where a password goes — not to an HTTP endpoint, because there is none and
  // adding one would put a second door in a building designed with one.
  function login(user, pass) {
    return new Promise(function (resolve, reject) {
      var proto = location.protocol === 'https:' ? 'wss://' : 'ws://';
      var ws = new WebSocket(proto + location.host + '/ws');
      var settled = false;
      ws.onopen = function () { ws.send(JSON.stringify({ type: 'auth', user: user, pass: pass })); };
      ws.onmessage = function (ev) {
        var msg; try { msg = JSON.parse(ev.data); } catch (e) { return; }
        if (msg.type !== 'auth') { return; }
        settled = true;
        // Closed as soon as the token is in hand: this page wants a
        // credential, not a seat in the room.
        ws.close();
        if (msg.ok) { resolve(msg.token || ''); }
        else { reject(new Error(msg.reason || 'invalid credentials')); }
      };
      ws.onclose = function () { if (!settled) { reject(new Error('the desktop closed the connection')); } };
      ws.onerror = function () { if (!settled) { reject(new Error('could not reach the desktop')); } };
    });
  }

  function askForLogin(why) {
    main.innerHTML =
      '<h2>sign in</h2>' +
      (why ? '<p class="bad">' + esc(why) + '</p>' : '') +
      '<p class="sub">These are the commands run on this desktop, by the agent and ' +
      'by the people here. They are behind the same login as the desktop itself.</p>' +
      '<form class="login" id="lf">' +
      '<input name="user" placeholder="user" autocomplete="username" autofocus>' +
      '<input name="pass" type="password" placeholder="password" autocomplete="current-password">' +
      '<input type="submit" value="Sign in">' +
      '</form>';
    document.getElementById('lf').addEventListener('submit', function (ev) {
      ev.preventDefault();
      var f = ev.target;
      main.textContent = 'signing in…';
      login(f.user.value, f.pass.value).then(function (t) {
        keep(t);
        render();
      }, function (err) { askForLogin(err.message); });
    });
  }

  function fail(err) {
    if (err && err.auth) { askForLogin('the session expired'); return; }
    main.innerHTML = '<p class="bad">' + esc(err && err.message ? err.message : err) + '</p>';
  }

  function stream(title, text) {
    if (!text) { return '<h2>' + title + '</h2><p class="empty">nothing on this stream</p>'; }
    return '<h2>' + title + '</h2><pre>' + esc(text) + '</pre>';
  }

  function jobHeadline(job) {
    var cls = job.status === 'done' ? 'ok'
            : job.status === 'running' ? 'warn' : 'bad';
    var bits = ['<span class="' + cls + '">' + esc(job.status) + '</span>'];
    if (job.exit_code != null) { bits.push('exit ' + esc(job.exit_code)); }
    if (job.aborted_by) { bits.push('stopped by ' + esc(job.aborted_by)); }
    return bits.join(' · ');
  }

  function showJob(id) {
    return api('/logs/api/job?id=' + encodeURIComponent(id)).then(function (d) {
      main.innerHTML =
        '<h2>job ' + esc(d.job.id) + '</h2>' +
        '<p class="mono">' + esc(d.job.command) + '</p>' +
        '<p>' + jobHeadline(d.job) + '</p>' +
        stream('stdout', d.stdout) +
        stream('stderr', d.stderr) +
        '<p class="sub">Last ' + esc(d.tail) + ' lines of each stream. They are kept ' +
        'apart on disk, so an empty stderr means the command printed no errors.</p>';
    });
  }

  function showJobs() {
    return api('/logs/api/index').then(function (d) {
      if (!d.jobs.length) {
        main.innerHTML = '<p class="empty">No job has run on this desktop yet.</p>';
        return;
      }
      var rows = d.jobs.map(function (j) {
        return '<li><a href="?job=' + encodeURIComponent(j.id) + '">' + esc(j.id) + '</a> — ' +
               '<span class="mono">' + esc(j.command) + '</span> — ' + jobHeadline(j) + '</li>';
      }).join('');
      main.innerHTML = '<h2>jobs</h2><ul class="jobs">' + rows + '</ul>';
    });
  }

  function lineTable(entries, note) {
    if (!entries.length) {
      return '<p class="empty">nothing recorded yet</p>' + (note ? '<p class="sub">' + esc(note) + '</p>' : '');
    }
    var rows = entries.map(function (e) {
      var res = e.ok == null ? '' : (e.ok ? '<span class="ok">ok</span>' : '<span class="bad">failed</span>');
      if (e.exit_code != null) { res += ' <span class="sub">exit ' + esc(e.exit_code) + '</span>'; }
      return '<tr><td class="mono">' + esc(e.time) + '</td>' +
             '<td class="who-' + esc(e.source) + '">' + esc(e.actor) + '</td>' +
             '<td>' + esc(e.what) + '</td>' +
             '<td class="detail mono">' + esc(e.detail) + '</td>' +
             '<td>' + res + '</td></tr>';
    }).join('');
    return '<table><thead><tr><th>time (UTC)</th><th>who</th><th>what</th>' +
           '<th>detail</th><th></th></tr></thead><tbody>' + rows + '</tbody></table>' +
           (note ? '<p class="sub">' + esc(note) + '</p>' : '');
  }

  function showPeople() {
    return api('/logs/api/people').then(function (d) {
      main.innerHTML = '<h2>what the people here ran</h2>' + lineTable(d.entries, d.note);
    });
  }

  function showAgent() {
    return api('/logs/api/agent').then(function (d) {
      main.innerHTML = '<h2>agent tool calls</h2>' + lineTable(d.entries, d.note);
    });
  }

  function render() {
    tabs.hidden = false;
    Array.prototype.forEach.call(tabs.querySelectorAll('.tab'), function (b) {
      if (b.dataset.view === 'job') { b.hidden = !jobId; }
      b.setAttribute('aria-selected', b.dataset.view === view ? 'true' : 'false');
    });
    main.textContent = 'loading…';
    var job = view === 'job' && jobId;
    var p = job ? showJob(jobId)
          : view === 'people' ? showPeople()
          : view === 'agent' ? showAgent()
          : showJobs();
    p.catch(fail);
  }

  tabs.addEventListener('click', function (ev) {
    var b = ev.target.closest('.tab');
    if (!b) { return; }
    if (b.dataset.view !== 'refresh') { view = b.dataset.view; }
    render();
  });

  // /auth is the one open endpoint and it returns one boolean: whether a login
  // is required at all. With authentication off — development mode — the page
  // goes straight to the data, the same way the file manager does.
  fetch('/auth', { cache: 'no-store' })
    .then(function (r) { return r.json(); })
    .then(function (a) {
      if (a.required && !token()) { askForLogin(''); return; }
      render();
    })
    .catch(function () { render(); });
})();
</script>
</body>
</html>
`
