/* Sentinel chain of custody, frontend.
 *
 * Two rules govern everything below.
 *
 * First, the API base is resolved at runtime, never compiled in. The static files are
 * built for nothing and deploy anywhere; the Go API lives on a different host.
 *
 * Second, and more important: this page renders the claims the API reports and nothing
 * else. In particular the RFC 8693 `act` claim, which is what would carry a delegation
 * chain, is not documented by Okta and has not been observed from this tenant. So when
 * it is absent the page says so, plainly, and shows what the token does carry instead.
 * A drawn chain that the token does not support would make the whole demo worthless.
 */

'use strict';

// ---------------------------------------------------------------------------- API base

const API_BASE_KEY = 'sentinel.apiBase';

function resolveApiBase() {
  // ?api=<url> wins and is remembered, so the base is set once per browser rather than
  // being pasted into every link. ?api= with no value forgets it again.
  const fromQuery = new URLSearchParams(location.search).get('api');
  if (fromQuery !== null) {
    const value = fromQuery.replace(/\/+$/, '');
    try {
      if (value) localStorage.setItem(API_BASE_KEY, value);
      else localStorage.removeItem(API_BASE_KEY);
    } catch (_) { /* private browsing, fall through */ }
    return value;
  }

  try {
    const saved = localStorage.getItem(API_BASE_KEY);
    if (saved) return saved;
  } catch (_) { /* private browsing, fall through */ }

  if (location.hostname === 'localhost' || location.hostname === '127.0.0.1') {
    return 'http://localhost:8090';
  }

  // Same origin, for anyone proxying /api to the Go service.
  return '';
}

const API = resolveApiBase();

// ------------------------------------------------------------------------------- state

// refused and failed are separate on purpose. "refused" means Okta decided no. "no
// decision" means the call never got that far, so nothing may be attributed to Okta.
const STATES = {
  idle: { text: 'idle' },
  in_progress: { text: 'in progress' },
  issued: { text: 'issued' },
  refused: { text: 'refused' },
  failed: { text: 'no decision' },
  skipped: { text: 'not attempted' },
};

const STEP_ORDER = ['watch_token', 'id_jag', 'access_token'];

const state = {
  config: null,
  steps: {},        // step id -> the latest event for that step
  running: false,
  selected: null,   // { kind: 'node' | 'hop', id }
};

// A hop's state is derived from its steps rather than sent, so the two cannot disagree.
function hopState(hopId) {
  const steps = (state.config ? state.config.steps : [])
    .filter((s) => s.hop === hopId)
    .map((s) => state.steps[s.step])
    .filter(Boolean);

  if (!steps.length) return 'idle';
  if (steps.some((s) => s.state === 'refused')) return 'refused';
  if (steps.some((s) => s.state === 'failed')) return 'failed';
  if (steps.some((s) => s.state === 'in_progress')) return 'in_progress';
  if (steps.every((s) => s.state === 'skipped')) return 'skipped';
  if (steps.length === steps.filter((s) => s.state === 'issued').length) return 'issued';
  return 'in_progress';
}

function nodeState(id) {
  const h1 = hopState('hop1');
  const h2 = hopState('hop2');
  if (id === 'watch') return h1;
  if (id === 'tasking') return h2;
  // The Intake Agent receives hop 1 and performs hop 2, so it shows whichever is further.
  return h2 === 'idle' ? h1 : h2;
}

// ------------------------------------------------------------------------ DOM utilities

// el builds elements with textContent, never innerHTML. Claim values come from a token
// and are therefore untrusted input; assigning them as markup would be a bug.
function el(tag, props, children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v === null || v === undefined) continue;
    if (k === 'class') node.className = v;
    else if (k === 'text') node.textContent = v;
    else if (k.startsWith('on')) node.addEventListener(k.slice(2), v);
    else node.setAttribute(k, v);
  }
  for (const c of [].concat(children || [])) {
    if (c === null || c === undefined || c === false) continue;
    node.appendChild(typeof c === 'string' ? document.createTextNode(c) : c);
  }
  return node;
}

function clear(node) {
  while (node.firstChild) node.removeChild(node.firstChild);
}

function truncate(s, n) {
  if (typeof s !== 'string') return '';
  return s.length <= n ? s : s.slice(0, n - 1) + '…';
}

// -------------------------------------------------------------------------- the diagram

const NODE_W = 250;
const NODE_H = 116;
const CENTRE_Y = 128;
const NODE_X = [138, 550, 962]; // centres, left to right

let svg = null;

function drawDiagram() {
  if (typeof d3 === 'undefined') {
    document.getElementById('no-d3').hidden = false;
    return;
  }

  const cfg = state.config;
  svg = d3.select('#chain');
  svg.selectAll('*').remove();

  // One arrowhead per state, because a marker cannot reliably inherit the stroke colour
  // of the path that uses it across browsers.
  const defs = svg.append('defs');
  for (const s of Object.keys(STATES)) {
    defs.append('marker')
      .attr('id', 'arrow-' + s)
      .attr('viewBox', '0 0 10 10')
      .attr('refX', 9).attr('refY', 5)
      .attr('markerWidth', 6).attr('markerHeight', 6)
      .attr('orient', 'auto-start-reverse')
      .append('path')
      .attr('d', 'M 0 0 L 10 5 L 0 10 z')
      .attr('class', 'arrowhead state-' + s);
  }

  // Edges first, so node boxes paint over the line ends.
  const edges = svg.append('g').attr('class', 'edges');
  cfg.hops.forEach((hop) => {
    const fromIdx = cfg.principals.findIndex((p) => p.id === hop.from);
    const toIdx = cfg.principals.findIndex((p) => p.id === hop.to);
    const x1 = NODE_X[fromIdx] + NODE_W / 2 + 6;
    const x2 = NODE_X[toIdx] - NODE_W / 2 - 6;

    const g = edges.append('g')
      .attr('class', 'edge')
      .attr('data-hop', hop.id)
      .attr('role', 'button')
      .attr('tabindex', 0);

    // A wide transparent line, so the click and focus target is not a 2px stroke.
    g.append('line')
      .attr('class', 'edge-hit')
      .attr('x1', x1).attr('y1', CENTRE_Y).attr('x2', x2).attr('y2', CENTRE_Y);

    g.append('line')
      .attr('class', 'edge-line')
      .attr('x1', x1).attr('y1', CENTRE_Y).attr('x2', x2).attr('y2', CENTRE_Y);

    g.append('text')
      .attr('class', 'edge-label')
      .attr('x', (x1 + x2) / 2).attr('y', CENTRE_Y - 16)
      .attr('text-anchor', 'middle')
      .text('hop ' + hop.number);

    g.append('text')
      .attr('class', 'edge-state')
      .attr('x', (x1 + x2) / 2).attr('y', CENTRE_Y + 26)
      .attr('text-anchor', 'middle');

    g.on('click', () => select('hop', hop.id));
    g.on('keydown', (event) => {
      if (event.key === 'Enter' || event.key === ' ') {
        event.preventDefault();
        select('hop', hop.id);
      }
    });
  });

  // Nodes.
  const nodes = svg.append('g').attr('class', 'nodes');
  cfg.principals.forEach((p, i) => {
    const x = NODE_X[i] - NODE_W / 2;
    const y = CENTRE_Y - NODE_H / 2;

    const g = nodes.append('g')
      .attr('class', 'node')
      .attr('data-node', p.id)
      .attr('role', 'button')
      .attr('tabindex', 0);

    g.append('rect')
      .attr('class', 'node-box')
      .attr('x', x).attr('y', y)
      .attr('width', NODE_W).attr('height', NODE_H)
      .attr('rx', 6);

    g.append('text')
      .attr('class', 'node-role')
      .attr('x', x + 14).attr('y', y + 24)
      .text(p.role.toUpperCase());

    g.append('text')
      .attr('class', 'node-name')
      .attr('x', x + 14).attr('y', y + 48)
      .text(truncate(p.name, 26));

    g.append('text')
      .attr('class', 'node-resource')
      .attr('x', x + 14).attr('y', y + 70)
      .text(p.resource_url ? truncate(p.resource_url, 30) : '');

    g.append('text')
      .attr('class', 'node-state')
      .attr('x', x + 14).attr('y', y + 98);

    g.append('title').text(p.name + ' — ' + p.role +
      (p.resource_url ? '\nresource: ' + p.resource_url : ''));

    g.on('click', () => select('node', p.id));
    g.on('keydown', (event) => {
      if (event.key === 'Enter' || event.key === ' ') {
        event.preventDefault();
        select('node', p.id);
      }
    });
  });

  paintStates();
}

// paintStates is the only place the diagram's appearance changes, so a new event never
// has to know how anything is drawn.
function paintStates() {
  if (!svg) return;

  svg.selectAll('g.node').each(function () {
    const g = d3.select(this);
    const id = g.attr('data-node');
    const s = nodeState(id);
    const p = state.config.principals.find((x) => x.id === id);

    g.attr('class', 'node state-' + s +
      (state.selected && state.selected.kind === 'node' && state.selected.id === id
        ? ' selected' : ''));
    g.select('text.node-state').text(STATES[s].text);
    g.attr('aria-label', p.name + ', ' + p.role + ', ' + STATES[s].text);
  });

  svg.selectAll('g.edge').each(function () {
    const g = d3.select(this);
    const id = g.attr('data-hop');
    const s = hopState(id);
    const hop = state.config.hops.find((x) => x.id === id);

    g.attr('class', 'edge state-' + s +
      (state.selected && state.selected.kind === 'hop' && state.selected.id === id
        ? ' selected' : ''));
    g.select('line.edge-line').attr('marker-end', 'url(#arrow-' + s + ')');
    g.select('text.edge-state').text(STATES[s].text);
    g.attr('aria-label', 'Hop ' + hop.number + ', ' + hop.label + ', ' + STATES[s].text);
  });
}

// ----------------------------------------------------------------------- the step list

function renderSteps() {
  const list = document.getElementById('steps');
  clear(list);

  (state.config.steps || []).forEach((shape) => {
    const ev = state.steps[shape.step];
    const s = ev ? ev.state : 'idle';

    list.appendChild(el('li', { class: 'step state-' + s }, [
      el('button', {
        type: 'button',
        class: 'step-button',
        onclick: () => select('hop', shape.hop),
      }, [
        el('span', { class: 'step-state', text: STATES[s].text }),
        // An explicit separator: the flex gap spaces these visually, but a screen reader
        // reading textContent would otherwise hear "refusedWatch Service mints…".
        ' — ',
        el('span', { class: 'step-label', text: shape.label }),
      ]),
    ]));
  });
}

// -------------------------------------------------------------------- the detail panel

function select(kind, id) {
  state.selected = { kind, id };
  paintStates();
  renderDetail();
  document.getElementById('detail').scrollIntoView({ block: 'nearest' });
}

function renderDetail() {
  const host = document.getElementById('detail');
  clear(host);

  if (!state.selected) {
    host.appendChild(el('p', {
      class: 'hint',
      text: 'Nothing selected. Choose a box or an arrow above, or a step.',
    }));
    return;
  }

  if (state.selected.kind === 'node') {
    renderNodeDetail(host, state.config.principals.find((p) => p.id === state.selected.id));
  } else {
    renderHopDetail(host, state.config.hops.find((h) => h.id === state.selected.id));
  }
}

function renderNodeDetail(host, p) {
  if (!p) return;
  const s = nodeState(p.id);

  host.appendChild(el('h3', { text: p.name }));
  host.appendChild(el('p', { class: 'kv' }, [
    el('strong', { text: 'role: ' }), p.role,
    el('br', {}),
    el('strong', { text: 'state: ' }), STATES[s].text,
  ]));
  if (p.resource_url) {
    host.appendChild(el('p', { class: 'kv' }, [
      el('strong', { text: 'resource URL: ' }),
      el('code', { text: p.resource_url }),
    ]));
  }
  host.appendChild(el('p', { class: 'note', text: p.note }));

  const involved = state.config.hops.filter((h) => h.from === p.id || h.to === p.id);
  if (involved.length) {
    host.appendChild(el('h4', { text: 'Takes part in' }));
    host.appendChild(el('ul', {}, involved.map((h) =>
      el('li', {}, [
        el('button', {
          type: 'button', class: 'link',
          onclick: () => select('hop', h.id),
        }, ['hop ' + h.number + ': ' + h.label]),
      ]),
    )));
  }
}

function renderHopDetail(host, hop) {
  if (!hop) return;

  host.appendChild(el('h3', { text: 'Hop ' + hop.number + ': ' + hop.label }));
  host.appendChild(el('p', { class: 'note', text: hop.note }));

  if (hop.scopes && hop.scopes.length) {
    host.appendChild(el('p', { class: 'kv' }, [
      el('strong', { text: 'scopes requested: ' }),
      el('code', { text: hop.scopes.join(' ') }),
    ]));
  }

  const shapes = (state.config.steps || []).filter((s) => s.hop === hop.id);
  shapes.forEach((shape) => {
    const ev = state.steps[shape.step];
    host.appendChild(renderStepCard(shape, ev));
  });
}

function renderStepCard(shape, ev) {
  const s = ev ? ev.state : 'idle';
  const card = el('div', { class: 'card state-' + s });

  card.appendChild(el('h4', {}, [
    el('span', { class: 'badge', text: STATES[s].text }),
    ' ' + shape.label,
  ]));

  if (!ev) {
    card.appendChild(el('p', { class: 'hint', text: 'Has not run yet.' }));
    return card;
  }

  if (ev.grant) {
    card.appendChild(el('p', { class: 'kv' }, [
      el('strong', { text: 'grant_type: ' }), el('code', { text: ev.grant }),
    ]));
  }
  if (ev.endpoint) {
    card.appendChild(el('p', { class: 'kv' }, [
      el('strong', { text: 'endpoint: ' }), el('code', { text: ev.endpoint }),
    ]));
  }
  if (ev.detail) card.appendChild(el('p', { class: 'note', text: ev.detail }));

  if (ev.error) card.appendChild(renderError(ev.error));
  if (ev.token) card.appendChild(renderToken(ev.token));

  return card;
}

function renderError(err) {
  const box = el('div', { class: 'error-box' + (err.from_okta ? '' : ' no-decision') });
  box.appendChild(el('h5', {
    text: err.from_okta
      ? 'Okta refused, in its own words'
      : 'The request did not reach a decision',
  }));

  if (err.error) {
    box.appendChild(el('p', { class: 'kv' }, [
      el('strong', { text: 'error: ' }), el('code', { text: err.error }),
    ]));
  }
  if (err.error_description) {
    box.appendChild(el('p', { class: 'kv' }, [
      el('strong', { text: 'error_description: ' }),
      el('code', { text: err.error_description }),
    ]));
  }
  if (err.http_status) {
    box.appendChild(el('p', { class: 'kv' }, [
      el('strong', { text: 'HTTP status: ' }), String(err.http_status),
    ]));
  }
  if (!err.from_okta) {
    box.appendChild(el('p', {
      class: 'hint',
      text: 'This wording is from this application, not from Okta. It means no policy ' +
            'decision was reached, which is not the same thing as a denial.',
    }));
  }
  return box;
}

// The claims worth putting first. Listed even when absent, because "no uid on this
// token" is itself the evidence that no user is involved.
const KEY_CLAIMS = ['sub', 'cid', 'uid', 'scp', 'aud', 'exp', 'jti', 'act'];

function renderToken(tok) {
  const box = el('div', { class: 'token-box' });

  box.appendChild(el('h5', {
    text: tok.kind ? 'What this step produced: ' + tok.kind : 'The token this step produced',
  }));

  // The two artefacts in hop 2 are not the same shape. Saying so stops the absent rows on
  // the assertion from reading as a fault.
  if (tok.kind && tok.kind.indexOf('assertion') === 0) {
    box.appendChild(el('p', { class: 'hint', text:
      'An assertion, not an access token, so it need not carry an access token’s ' +
      'claims. Rows below reading "not on this token" are a difference in kind, not a ' +
      'missing value.' }));
  }

  if (!tok.is_jwt) {
    box.appendChild(el('p', { class: 'warn', text:
      'This token could not be decoded, so no claims can be shown: ' +
      (tok.decode_error || 'unknown reason') +
      '. An authorization server configured to issue opaque tokens is a legitimate ' +
      'setup; it just means there is nothing to read here.' }));
    box.appendChild(previewLine(tok));
    return box;
  }

  // The act claim, handled honestly. This is the single most important paragraph on the
  // page: it is where a demo would be tempted to draw a chain that does not exist.
  if (tok.act_present) {
    box.appendChild(el('p', { class: 'act-present' }, [
      el('strong', { text: 'act claim present. ' }),
      'The delegation chain below is read from the token itself, outermost subject first:',
    ]));
    box.appendChild(el('p', { class: 'chain' },
      [el('code', { text: (tok.act_chain || []).join('  ←  ') })]));
  } else {
    // Name only the identifying claims this artefact actually has. Citing cid on an
    // assertion that carries none would be a small dishonesty of exactly the kind this
    // paragraph exists to prevent.
    const present = ['sub', 'cid', 'uid'].filter((k) => tok.claims[k] !== undefined);
    const basis = present.length
      ? 'inferred from ' + present.join(' and ') + ', not asserted by it'
      : 'not supported by this artefact at all: it carries no sub, cid or uid to infer from';

    box.appendChild(el('p', { class: 'act-absent' }, [
      el('strong', { text: 'No act claim here. ' }),
      'The chain drawn above is ' + basis + '. RFC 8693 act is what would carry the ' +
      'delegation chain, and it is absent, so this names ' +
      (present.length ? 'only ' + present.join(', ') : 'no principal') +
      ' and nothing about who delegated to whom.',
    ]));
  }

  box.appendChild(el('h6', { text: 'Claims that matter most' }));
  box.appendChild(claimTable(KEY_CLAIMS.map((k) => [k, tok.claims[k]]), true));

  const rest = Object.keys(tok.claims).filter((k) => !KEY_CLAIMS.includes(k)).sort();
  box.appendChild(el('h6', { text: 'Every other claim on the token (' + rest.length + ')' }));
  if (rest.length) {
    box.appendChild(claimTable(rest.map((k) => [k, tok.claims[k]]), false));
  } else {
    box.appendChild(el('p', { class: 'hint', text: 'None.' }));
  }

  if (tok.expires_in) {
    box.appendChild(el('p', { class: 'kv' }, [
      el('strong', { text: 'expires in: ' }), tok.expires_in,
    ]));
  }
  box.appendChild(previewLine(tok));
  return box;
}

function previewLine(tok) {
  return el('p', { class: 'kv preview' }, [
    el('strong', { text: 'token preview: ' }),
    el('code', { text: tok.preview }),
    el('span', { class: 'hint', text: ' (first 12 and last 8 characters; the API never returns more)' }),
  ]);
}

// showAbsent draws a row even for a missing claim, so absence is visible rather than
// merely unmentioned.
function claimTable(pairs, showAbsent) {
  const body = el('tbody');

  pairs.forEach(([k, v]) => {
    const absent = v === undefined || v === null;
    if (absent && !showAbsent) return;

    body.appendChild(el('tr', { class: absent ? 'absent' : null }, [
      el('th', { scope: 'row' }, [el('code', { text: k })]),
      el('td', {}, absent
        ? [el('span', { class: 'hint', text: 'not on this token' })]
        : claimValue(k, v)),
    ]));
  });

  return el('table', { class: 'claims' }, [body]);
}

const TIME_CLAIMS = ['exp', 'iat', 'nbf', 'auth_time'];

function claimValue(key, v) {
  if (Array.isArray(v)) {
    return [el('code', { text: v.map(String).join(', ') })];
  }
  if (typeof v === 'object') {
    return [el('pre', { text: JSON.stringify(v, null, 2) })];
  }
  const out = [el('code', { text: String(v) })];
  if (TIME_CLAIMS.includes(key)) {
    const seconds = Number(v);
    if (Number.isFinite(seconds)) {
      out.push(el('span', {
        class: 'hint',
        text: ' (' + new Date(seconds * 1000).toISOString().replace('.000Z', 'Z') + ')',
      }));
    }
  }
  return out;
}

// -------------------------------------------------------------------------------- runs

function setStatus(text, kind) {
  const node = document.getElementById('status');
  node.textContent = text;
  node.className = 'status' + (kind ? ' ' + kind : '');
}

async function loadConfig() {
  const res = await fetch(API + '/api/config', { headers: { Accept: 'application/json' } });
  if (!res.ok) throw new Error('HTTP ' + res.status);
  return res.json();
}

// fallbackConfig lets the page draw its idle state with no backend at all. It carries no
// resource URLs or scopes, because inventing them would be putting words in the tenant's
// mouth.
function fallbackConfig() {
  return {
    ready: false,
    missing: [],
    principals: [
      { id: 'watch', name: 'Sentinel Watch Service', role: 'service client',
        note: 'Starts the chain. A service client, not an agent.' },
      { id: 'intake', name: 'Sentinel Intake Agent', role: 'agent A',
        resource_url: '(unknown until the API answers)',
        note: 'Receives hop 1, then performs hop 2.' },
      { id: 'tasking', name: 'Sentinel Tasking Agent', role: 'agent B, privileged',
        resource_url: '(unknown until the API answers)',
        note: 'The privileged target of the chain.' },
    ],
    hops: [
      { id: 'hop1', number: 1, from: 'watch', to: 'intake',
        label: 'client_credentials', grants: ['client_credentials'],
        note: 'One call at a custom authorization server.' },
      { id: 'hop2', number: 2, from: 'intake', to: 'tasking',
        label: 'token exchange, then ID-JAG redemption', grants: [],
        note: 'Two calls, run by the plugin’s MintResourceToken.' },
    ],
    steps: [
      { step: 'watch_token', hop: 'hop1', label: 'Watch Service mints its own token' },
      { step: 'id_jag', hop: 'hop2', label: 'Intake Agent exchanges that token for an ID-JAG' },
      { step: 'access_token', hop: 'hop2', label: 'Intake Agent redeems the ID-JAG for the Tasking Agent’s token' },
    ],
  };
}

// parseEvent turns one SSE record into { event, data }.
function parseEvent(raw) {
  let name = 'message';
  const dataLines = [];
  for (const line of raw.split('\n')) {
    if (line.startsWith('event:')) name = line.slice(6).trim();
    else if (line.startsWith('data:')) dataLines.push(line.slice(5).trim());
  }
  let data = null;
  try {
    data = JSON.parse(dataLines.join('\n'));
  } catch (_) {
    data = null;
  }
  return { event: name, data };
}

async function run() {
  if (state.running) return;
  state.running = true;
  state.steps = {};
  state.selected = null;

  document.getElementById('run').disabled = true;
  document.getElementById('rerun').disabled = true;
  document.getElementById('rerun').hidden = true;
  setStatus('Running…', 'busy');
  renderSteps();
  renderDetail();
  paintStates();

  try {
    const res = await fetch(API + '/api/run', {
      method: 'POST',
      headers: { Accept: 'text/event-stream' },
    });

    if (!res.ok) {
      let message = 'HTTP ' + res.status;
      try {
        const body = await res.json();
        if (body.error) message = body.error;
        if (body.missing && body.missing.length) {
          message += ': ' + body.missing.join(', ');
        }
      } catch (_) { /* keep the status code */ }
      setStatus('The API refused to run: ' + message, 'bad');
      return;
    }

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';

    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });

      let split;
      while ((split = buffer.indexOf('\n\n')) >= 0) {
        const record = buffer.slice(0, split);
        buffer = buffer.slice(split + 2);
        handleEvent(parseEvent(record));
      }
    }
  } catch (err) {
    setStatus('Could not reach the API at ' + (API || 'this origin') + ': ' + err.message, 'bad');
  } finally {
    state.running = false;
    document.getElementById('run').disabled = false;
    document.getElementById('rerun').disabled = false;
  }
}

function handleEvent({ event, data }) {
  if (!data) return;

  if (event === 'step') {
    state.steps[data.step] = data;
    renderSteps();
    paintStates();
    // Keep an open panel current as later events for the same hop arrive.
    if (state.selected) renderDetail();
    return;
  }

  if (event === 'done') {
    setStatus(data.outcome || 'Finished.', data.ok ? 'good' : 'bad');
    document.getElementById('run').hidden = true;
    document.getElementById('rerun').hidden = false;

    // Open the hop that decided the run, not simply the last one. On a failure the last
    // step is "not attempted", whose panel explains nothing; the first step that was
    // refused or did not complete is the one worth reading.
    const decided = STEP_ORDER
      .map((s) => state.steps[s])
      .find((ev) => ev && (ev.state === 'refused' || ev.state === 'failed'));

    const finished = STEP_ORDER
      .map((s) => state.steps[s])
      .filter((ev) => ev && ev.state === 'issued')
      .pop();

    const open = decided || finished;
    if (open) select('hop', open.hop);
  }
}

// -------------------------------------------------------------------------------- start

async function start() {
  document.getElementById('api-base').textContent = API || location.origin + ' (same origin)';

  try {
    state.config = await loadConfig();
    if (!state.config.ready) {
      setStatus('The API is up but not fully configured. Missing: ' +
        (state.config.missing || []).join(', '), 'bad');
    }
  } catch (err) {
    state.config = fallbackConfig();
    setStatus('The API is not reachable at ' + (API || 'this origin') +
      ', so this is the default shape with nothing filled in. ' +
      'Add ?api=<url> to point at a running API.', 'bad');
  }

  drawDiagram();
  renderSteps();
  renderDetail();

  document.getElementById('run').addEventListener('click', run);
  document.getElementById('rerun').addEventListener('click', run);
}

start();
