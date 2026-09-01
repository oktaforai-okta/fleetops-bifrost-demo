/* Sentinel chain of custody, frontend.
 *
 * Two rules govern everything below.
 *
 * First, the API base is resolved at runtime, never compiled in. The static files are
 * built for nothing and deploy anywhere; the Go API lives on a different host.
 *
 * Second, and more important: this page renders the claims the API reports and nothing
 * else.
 *
 * The RFC 8693 `act` claim is what carries the delegation chain. It IS observed from this
 * tenant, on every successful exchange, along with a `sub_profile` at each level typing
 * the party `service` or `ai_agent`. What it is not is documented: `act` and `sub_profile`
 * appear in none of Okta's published developer pages, so both are verified empirically
 * rather than contractually and could change without notice. Do not present them to a
 * customer as documented behaviour.
 *
 * The discipline that follows is unchanged, and it is the reason the demo is worth
 * anything. Every chain on screen is read out of a token actually received. When `act` is
 * absent the page says so plainly and shows what the token does carry instead. When a
 * level is folded away it says that too, and the raw nested claim stays on screen beside
 * the rendering so the two can be compared. A drawn chain the token does not support
 * would make the whole demo worthless.
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

// activePath is the shape of the demonstration currently selected. Everything the page
// draws comes from it, so switching path re-draws rather than special-casing.
function activePath() {
  const paths = (state.config && state.config.paths) || {};
  return paths[state.path] || { principals: [], hops: [], steps: [], deny: {} };
}

// stepOrder is the selected path's steps, in the order they run. Derived from the API's
// own declaration rather than hardcoded, because the two paths do not share a step list.
function stepOrder() {
  return (activePath().steps || []).map((s) => s.step);
}

const state = {
  config: null,
  steps: {},        // step id -> the latest event for that step
  running: false,
  selected: null,   // { kind: 'node' | 'hop', id }
  mode: 'grant',    // which run is in flight: 'grant' or 'deny'
  path: 'gateway',  // which demonstration: 'gateway' or 'okta'. Set from the API on load.

  // profiles maps a diagram node id to the sub_profile a TOKEN stated for it. Populated
  // only from tokens actually received, so it is empty before the first run and never
  // contains anything the token did not say.
  profiles: {},
};

// observeProfiles records the sub_profile values a token stated, so the diagram can label
// each principal with what kind of thing the token says it is.
function observeProfiles(tok) {
  if (!tok) return;

  // The chain is authoritative: it pairs an id with the sub_profile at that level.
  if (tok.act && tok.act.chain) {
    for (const e of tok.act.chain) {
      if (e.principal && e.sub_profile) state.profiles[e.principal] = e.sub_profile;
    }
  }
  // The per-id map carries the same statements, for ids named outside the chain.
  if (tok.principals) {
    for (const ref of Object.values(tok.principals)) {
      if (ref.principal && ref.sub_profile) state.profiles[ref.principal] = ref.sub_profile;
    }
  }
}

// A hop's state is derived from its steps rather than sent, so the two cannot disagree.
function hopState(hopId) {
  const steps = (activePath().steps || [])
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

// A node's state comes from the hops it takes part in, derived rather than hardcoded so
// that a three-node and a four-node flow both work without special cases.
//
// A party in the middle of a flow touches two hops, so it shows whichever has got further:
// the last hop it is involved in that has started. That reproduces what you want to see
// while a run is in flight, which is how far the work has actually reached.
function nodeState(id) {
  const path = activePath();
  const principal = (path.principals || []).find((p) => p.id === id);

  // An aside is not on the flow, so it has no hop of its own. Okta is asked on every hop,
  // so it takes the state of the run as a whole: any refusal is its refusal.
  if (principal && principal.aside) {
    const all = (path.hops || []).map((h) => hopState(h.id));
    if (all.includes('refused')) return 'refused';
    if (all.includes('failed')) return 'failed';
    if (all.includes('in_progress')) return 'in_progress';
    if (all.length && all.every((s) => s === 'issued')) return 'issued';
    return 'idle';
  }

  const involved = (path.hops || [])
    .filter((h) => h.from === id || h.to === id)
    .map((h) => hopState(h.id));

  if (!involved.length) return 'idle';
  for (let i = involved.length - 1; i >= 0; i--) {
    if (involved[i] !== 'idle') return involved[i];
  }
  return 'idle';
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

// Sized for a projector rather than a laptop: this diagram is the first thing an audience
// reads and it is usually read from the back of a room.
const VIEW_W = 1240;
const NODE_W = 300;
const NODE_H = 168;

// The main row sits low enough to leave room above it for an aside. Okta is drawn there
// rather than in the row because it is ASKED on every hop rather than being passed
// through, and putting it in the row would say something false about the order of events.
const CENTRE_Y = 270;
const ASIDE_W = 260;
const ASIDE_H = 100;
const ASIDE_CENTRE_Y = 78;

// Hop captions go in the band between the aside and the top of the node row.
const CAPTION_Y = 152;

// Centres are computed from the number of row principals rather than being a fixed list,
// so the same code lays out a three-node or a four-node flow without being edited.
function nodeCentres(n) {
  const span = VIEW_W / n;
  return Array.from({ length: n }, (_, i) => span * (i + 0.5));
}

// What visibly happens on each hop, for the arrow captions. Keyed by path then hop, since
// the two paths reuse hop ids for different things. Falls back to the API's own label so
// an unrecognised hop still gets a caption rather than a bare arrow.
const HOP_ACTION = {
  gateway: {
    hop1: 'presents its caller token to',
    hop2: 'calls a tool, with a token Okta issued',
  },
  okta: {
    hop1: 'gets a token addressed to',
    hop2: 'delegates on the caller’s behalf',
  },
};

function hopAction(hop) {
  const forPath = HOP_ACTION[state.path] || {};
  return forPath[hop.id] || truncate(hop.label, 34);
}

let svg = null;

function drawDiagram() {
  if (typeof d3 === 'undefined') {
    document.getElementById('no-d3').hidden = false;
    return;
  }

  // Only the row principals take part in the left-to-right layout. Asides are positioned
  // relative to whichever row node they are anchored to.
  const all = activePath().principals || [];
  const cfg = {
    principals: all.filter((p) => !p.aside),
    asides: all.filter((p) => p.aside),
    hops: activePath().hops || [],
  };

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

  const NODE_X = nodeCentres(cfg.principals.length);

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

    // Captions sit ABOVE the node boxes, not in the gap between them. The gap is about
    // 110px wide and these strings are twice that, so drawing them level with the boxes
    // put them underneath the boxes on screen. Above the row they can be their natural
    // width, and the two gap centres are far enough apart not to collide with each other.
    g.append('text')
      .attr('class', 'edge-label')
      .attr('x', (x1 + x2) / 2).attr('y', CAPTION_Y)
      .attr('text-anchor', 'middle')
      .text('HOP ' + hop.number);

    // What actually happens on this hop, in words. Without it the diagram is a row of
    // boxes and some arrows, which means nothing to someone seeing it for the first time.
    g.append('text')
      .attr('class', 'edge-action')
      .attr('x', (x1 + x2) / 2).attr('y', CAPTION_Y + 24)
      .attr('text-anchor', 'middle')
      .text(hopAction(hop));

    g.append('text')
      .attr('class', 'edge-state')
      .attr('x', (x1 + x2) / 2).attr('y', CENTRE_Y + 34)
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
      .attr('x', x + 16).attr('y', y + 28)
      .text(p.role.toUpperCase());

    g.append('text')
      .attr('class', 'node-name')
      .attr('x', x + 16).attr('y', y + 56)
      .text(truncate(p.name, 24));

    // The token's own statement of what kind of principal this is. Drawn only once a
    // token has actually said so, and labelled with the claim it came from, so it can
    // never be mistaken for something this page decided.
    g.append('text')
      .attr('class', 'node-profile')
      .attr('x', x + 16).attr('y', y + 84);

    g.append('text')
      .attr('class', 'node-resource')
      .attr('x', x + 16).attr('y', y + 112)
      .text(p.resource_url ? truncate(p.resource_url, 32) : '');

    g.append('text')
      .attr('class', 'node-state')
      .attr('x', x + 16).attr('y', y + 146);

    g.append('title').text(p.name + '\n' + p.role +
      (p.resource_url ? '\nresource: ' + p.resource_url : ''));

    g.on('click', () => select('node', p.id));
    g.on('keydown', (event) => {
      if (event.key === 'Enter' || event.key === ' ') {
        event.preventDefault();
        select('node', p.id);
      }
    });
  });

  // Asides, drawn above the row and joined to their anchor by a dashed line. The line is
  // dashed and labelled because it is not a hop: nothing travels along it. It is a
  // question being asked, on every hop, of the party that answers all of them.
  cfg.asides.forEach((p) => {
    const anchorIdx = cfg.principals.findIndex((x) => x.id === p.anchor);
    const cx = anchorIdx >= 0 ? NODE_X[anchorIdx] : VIEW_W / 2;
    const x = cx - ASIDE_W / 2;
    const y = ASIDE_CENTRE_Y - ASIDE_H / 2;

    const g = nodes.append('g')
      .attr('class', 'node aside')
      .attr('data-node', p.id)
      .attr('role', 'button')
      .attr('tabindex', 0);

    g.append('line')
      .attr('class', 'aside-link')
      .attr('x1', cx).attr('y1', y + ASIDE_H)
      .attr('x2', cx).attr('y2', CENTRE_Y - NODE_H / 2);

    g.append('rect')
      .attr('class', 'node-box')
      .attr('x', x).attr('y', y)
      .attr('width', ASIDE_W).attr('height', ASIDE_H)
      .attr('rx', 6);

    g.append('text')
      .attr('class', 'node-role')
      .attr('x', x + 16).attr('y', y + 24)
      .text(p.role.toUpperCase());

    g.append('text')
      .attr('class', 'node-name')
      .attr('x', x + 16).attr('y', y + 52)
      .text(truncate(p.name, 22));

    // Inside the box rather than on the connector. On the connector it sat in the same
    // horizontal band as the hop captions and collided with one of them.
    g.append('text')
      .attr('class', 'aside-link-label')
      .attr('x', x + 16).attr('y', y + 74)
      .text('asked on every hop');

    g.append('text')
      .attr('class', 'node-state')
      .attr('x', x + 16).attr('y', y + 94);

    g.append('title').text(p.name + '\n' + p.role);

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
    const p = activePath().principals.find((x) => x.id === id);

    g.attr('class', 'node state-' + s +
      (state.selected && state.selected.kind === 'node' && state.selected.id === id
        ? ' selected' : ''));
    g.select('text.node-state').text(STATES[s].text);

    const profile = state.profiles[id];
    g.select('text.node-profile').text(profile ? 'sub_profile: ' + profile : '');

    g.attr('aria-label', p.name + ', ' + p.role +
      (profile ? ', sub_profile ' + profile : '') + ', ' + STATES[s].text);
  });

  svg.selectAll('g.edge').each(function () {
    const g = d3.select(this);
    const id = g.attr('data-hop');
    const s = hopState(id);
    const hop = activePath().hops.find((x) => x.id === id);

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

  (activePath().steps || []).forEach((shape) => {
    const ev = state.steps[shape.step];
    const s = ev ? ev.state : 'idle';

    list.appendChild(el('li', { class: 'step state-' + s }, [
      el('button', {
        type: 'button',
        class: 'step-button',
        onclick: () => select('hop', shape.hop),
      }, [
        el('span', { class: 'step-state', text: STATES[s].text }),
        // A separator for screen readers ONLY. Without it, reading textContent runs the
        // words together as "refusedWatch Service mints". Visually the flex gap already
        // separates them, so this must not be visible: a bare ", " here rendered as a
        // stray comma floating between the state and the label.
        el('span', { class: 'sr-only', text: ', ' }),
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
    renderNodeDetail(host, activePath().principals.find((p) => p.id === state.selected.id));
  } else {
    renderHopDetail(host, activePath().hops.find((h) => h.id === state.selected.id));
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

  const involved = activePath().hops.filter((h) => h.from === p.id || h.to === p.id);
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

  const shapes = (activePath().steps || []).filter((s) => s.hop === hop.id);
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
  // What this step asked for, next to what came back. On a refusal this is the request
  // that earned it, so the two belong on screen together.
  if (ev.scopes && ev.scopes.length) {
    card.appendChild(el('p', { class: 'kv' }, [
      el('strong', { text: 'scopes requested: ' }), el('code', { text: ev.scopes.join(' ') }),
    ]));
  }
  if (ev.tool) {
    card.appendChild(el('p', { class: 'kv' }, [
      el('strong', { text: 'tool called: ' }), el('code', { text: ev.tool }),
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
  if (ev.result) card.appendChild(renderResourceResult(ev.result));

  return card;
}

// renderResourceResult shows what the upstream MCP server said, verbatim.
//
// Nothing here is parsed into a claim. The server's authorization block is its own account
// of the token it received, and it is the strongest evidence on the page precisely because
// it does not come from this page: the resource server is independent of both the gateway
// and this app, and this app never holds the token it is describing.
function renderResourceResult(result) {
  const box = el('div', { class: 'resource-box' });

  box.appendChild(el('h5', { text: 'What the MCP server returned' }));
  // No caveat paragraph here. The step's own Detail already says this is verbatim from the
  // server and that this app never held the token, and saying it twice on one screen read
  // as hedging. If that Detail ever loses the point, put it back here rather than in both.

  if (result.body) {
    box.appendChild(el('h6', { text: 'The tool’s own output' }));
    box.appendChild(el('pre', { class: 'verbatim', text: result.body }));
  }

  if (result.attribution) {
    box.appendChild(el('h6', { text: 'The server’s account of the token it received' }));
    box.appendChild(el('pre', { class: 'verbatim attribution', text: result.attribution }));
  } else {
    box.appendChild(el('p', {
      class: 'warn',
      text: 'The reply carried no authorization block, so there is nothing here about the ' +
            'token the server received. Nothing has been added in its place.',
    }));
  }

  return box;
}

// How to head an error box, by where the wording came from. The three are genuinely
// different claims and collapsing them would overstate one of them.
const ERROR_HEADING = {
  okta: 'Okta refused, in its own words',
  gateway: 'The gateway refused, quoting Okta',
  sentinel: 'The request did not reach a decision',
};

function renderError(err) {
  const relayed = err.source === 'gateway';
  const box = el('div', {
    class: 'error-box' + (err.from_okta || relayed ? '' : ' no-decision'),
  });
  box.appendChild(el('h5', {
    text: ERROR_HEADING[err.source] || ERROR_HEADING.sentinel,
  }));
  if (relayed) {
    box.appendChild(el('p', {
      class: 'hint',
      text: 'This is Bifrost’s message, and the reason inside it is Okta’s. This app did ' +
            'not call Okta on this request, so the wording is reproduced exactly as the ' +
            'gateway relayed it rather than being presented as an error body read from Okta.',
    }));
  }

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
  if (err.source === 'sentinel') {
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
//
// sub_profile sits next to sub because it is the token's own statement of what KIND of
// principal the subject is, and act stays on the list so the raw nested object is always
// on screen next to the chain rendered from it. Anyone can then check one against the
// other without being asked to trust the rendering.
const KEY_CLAIMS = ['sub', 'sub_profile', 'cid', 'uid', 'scp', 'aud', 'exp', 'jti', 'act'];

// What each role in the chain means, in words a customer can read. The token supplies the
// role; this only translates it.
const ROLE_TEXT = {
  subject: 'the party the work is for',
  actor: 'the party that acted',
};

// renderActSection is the honesty of the page. Either the token carries an act claim and
// the chain is read out of it, or it does not and the page says so.
function renderActSection(box, tok) {
  const act = tok.act;

  if (!act || !act.present) {
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
    return;
  }

  const chain = act.chain || [];

  box.appendChild(el('p', { class: 'act-present' }, [
    el('strong', { text: 'act claim present. ' }),
    'Everything below is read out of the token. ' + chainCountText(chain, act),
  ]));

  // The plain sentence first, because it is the thing a customer takes away.
  const sentence = chainSentence(chain);
  if (sentence) box.appendChild(el('p', { class: 'chain-sentence', text: sentence }));

  box.appendChild(chainCards(chain));

  // Say when something was folded away, and point at the raw claim. Collapsing quietly
  // would be its own kind of dishonesty, even though the collapse is the correct call.
  if (act.collapsed) {
    box.appendChild(el('p', { class: 'chain-note' }, [
      el('strong', { text: 'One nested level is not drawn as a hop. ' }),
      'The token carries ' + act.levels + ' nested act ' +
      (act.levels === 1 ? 'level' : 'levels') + ', and the innermost one names the ' +
      'subject again as its own actor’s delegator. Drawing it would imply a ' +
      'delegation hop that did not happen, so it is not drawn. Nothing is hidden: the ' +
      'raw nested act object is in the claims table below, exactly as it arrived.',
    ]));
  }
}

// chainCountText states the party count against the raw nesting depth, so the number on
// screen can always be reconciled with the claim.
function chainCountText(chain, act) {
  const parties = chain.length + ' distinct ' + (chain.length === 1 ? 'party' : 'parties');
  return 'It names ' + parties + ', from ' + act.levels + ' nested act ' +
    (act.levels === 1 ? 'level' : 'levels') + '.';
}

// chainSentence turns the chain into one line of prose. Built by walking the entries
// rather than templated for two, so a longer chain reads correctly instead of silently
// losing its middle.
function chainSentence(chain) {
  if (!chain.length) return '';

  const label = (e) => partyLabel(e) + (e.sub_profile ? ' (' + e.sub_profile + ')' : '');

  let s = 'The work is for ' + label(chain[0]) + '.';
  for (let i = 1; i < chain.length; i++) {
    s += i === 1
      ? ' ' + label(chain[i]) + ' acted on its behalf.'
      : ' ' + label(chain[i]) + ' acted on behalf of ' + label(chain[i - 1]) + '.';
  }
  return s;
}

// partyLabel prefers the friendly name and falls back to the raw id. It never invents a
// name: an id the API could not resolve is shown as the id.
function partyLabel(e) {
  return e.name || e.sub;
}

// chainCards draws one card per party, in token order: subject first, then each actor
// outward. The connector is labelled so the direction cannot be misread.
function chainCards(chain) {
  const wrap = el('div', { class: 'chain-cards' });

  chain.forEach((e, i) => {
    if (i > 0) {
      wrap.appendChild(el('div', { class: 'chain-arrow' }, [
        el('span', { class: 'chain-arrow-mark', text: '◀' }),
        el('span', { class: 'chain-arrow-text', text: 'acted on behalf of' }),
      ]));
    }

    const card = el('div', { class: 'chain-card role-' + e.role });
    card.appendChild(el('div', { class: 'chain-role', text: ROLE_TEXT[e.role] || e.role }));
    card.appendChild(el('div', { class: 'chain-name', text: partyLabel(e) }));

    // sub_profile is the strongest evidence on the page: the token itself says what kind
    // of principal this is, rather than leaving it to a naming convention.
    if (e.sub_profile) {
      card.appendChild(el('div', { class: 'chain-profile' }, [
        el('span', { class: 'profile-chip', text: e.sub_profile }),
        el('span', { class: 'chain-profile-src', text: 'from sub_profile' }),
      ]));
    } else {
      card.appendChild(el('div', { class: 'chain-profile' }, [
        el('span', { class: 'hint', text: 'no sub_profile at this level' }),
      ]));
    }

    // The id stays visible but secondary. It is what the token actually said, and the
    // friendly name above it is this application's annotation.
    card.appendChild(el('div', { class: 'chain-id' }, [el('code', { text: e.sub })]));
    if (!e.name) {
      card.appendChild(el('div', {}, [el('span', {
        class: 'hint',
        text: 'not an id this app was configured with, so it is shown as it arrived',
      })]));
    }

    wrap.appendChild(card);
  });

  return wrap;
}

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

  // The act claim, handled honestly. This is the single most important part of the page:
  // it is where a demo would be tempted to draw a chain that does not exist.
  renderActSection(box, tok);

  box.appendChild(el('h6', { text: 'Claims that matter most' }));
  box.appendChild(claimTable(KEY_CLAIMS.map((k) => [k, tok.claims[k]]), true, tok));

  const rest = Object.keys(tok.claims).filter((k) => !KEY_CLAIMS.includes(k)).sort();
  box.appendChild(el('h6', { text: 'Every other claim on the token (' + rest.length + ')' }));
  if (rest.length) {
    box.appendChild(claimTable(rest.map((k) => [k, tok.claims[k]]), false, tok));
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
function claimTable(pairs, showAbsent, tok) {
  const body = el('tbody');

  pairs.forEach(([k, v]) => {
    const absent = v === undefined || v === null;
    if (absent && !showAbsent) return;

    body.appendChild(el('tr', { class: absent ? 'absent' : null }, [
      el('th', { scope: 'row' }, [el('code', { text: k })]),
      el('td', {}, absent
        ? [el('span', { class: 'hint', text: 'not on this token' })]
        : claimValue(k, v, tok)),
    ]));
  });

  return el('table', { class: 'claims' }, [body]);
}

const TIME_CLAIMS = ['exp', 'iat', 'nbf', 'auth_time'];

function claimValue(key, v, tok) {
  if (Array.isArray(v)) {
    return [el('code', { text: v.map(String).join(', ') })];
  }
  if (typeof v === 'object') {
    // Objects are printed as they arrived. This is how the raw nested act claim stays on
    // screen next to the chain rendered from it, so the rendering can be checked rather
    // than taken on trust.
    return [el('pre', { text: JSON.stringify(v, null, 2) })];
  }
  const out = [el('code', { text: String(v) })];

  // Annotate an id with the name this app was configured with, when it has one. The id
  // stays first and unmodified; the name is an aid to reading, not a replacement.
  const known = tok && tok.principals && tok.principals[String(v)];
  if (known) {
    out.push(el('span', { class: 'id-name', text: ' ' + known.name }));
    if (known.sub_profile) {
      out.push(el('span', { class: 'profile-chip small', text: known.sub_profile }));
    }
  }
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

// decidingStep is the first step that was refused or that reached no decision. On a
// failure the LAST step is "not attempted", whose panel explains nothing; the step that
// actually decided the run is the one worth reading.
function decidingStep() {
  return stepOrder().map((s) => state.steps[s])
    .find((ev) => ev && (ev.state === 'refused' || ev.state === 'failed'));
}

// renderVerdict draws the one-line answer, large, at the top of the page.
//
// It reads mode and outcome together, because they are different questions. A refusal run
// that is refused has ok false and has done exactly what was asked, and a refusal run that
// is ISSUED is the interesting failure: the scope was grantable after all. Reporting only
// ok would get both of those backwards.
function renderVerdict(done) {
  const host = document.getElementById('verdict');
  clear(host);

  const deny = state.mode === 'deny';
  const gateway = done.path === 'gateway';
  const ev = decidingStep();
  const err = ev && ev.error;
  const refused = ev && ev.state === 'refused';

  // What this run asked for, in the terms the path uses. On the gateway path the request
  // is a tool; on the direct path it is a scope list.
  const asked = gateway
    ? 'the tool ' + (done.requested_tool || 'requested')
    : (done.requested_scopes || []).join(' ');

  let kind;
  let headline;
  let sub;

  if (done.ok && !deny) {
    kind = 'issued';
    headline = gateway
      ? 'The tool call went through, authorized by Okta.'
      : 'Okta issued the token.';
    sub = gateway
      ? 'Bifrost obtained a delegated token from Okta and the MCP server accepted it. The ' +
        'server’s own account of that token is below, in its words.'
      : 'The delegation chain below is read from that token.';
  } else if (done.ok && deny) {
    kind = 'unexpected';
    headline = 'This run expected a refusal and was allowed instead.';
    sub = 'It asked for ' + asked + ', and it succeeded. That is not a bug in this page: ' +
      'it means the agent’s managed connection does grant this. Nothing here is a refusal.';
  } else if (refused) {
    kind = 'refused';
    headline = gateway ? 'Refused. The call never reached the tool.' : 'Okta refused.';
    sub = deny
      ? 'This run asked for ' + asked + ', which the agent’s managed connection does not ' +
        'grant. Nothing in the tenant was changed to arrange this. The refusal below is ' +
        'the demonstration, not a fault.'
      : (gateway
        ? 'The gateway asked Okta, Okta said no, and the gateway obeyed. Its words are below.'
        : 'The chain stopped at a policy decision. Okta’s own words are below.');
  } else if (ev) {
    kind = 'no-decision';
    headline = 'No decision was reached.';
    sub = 'The call did not complete, so nothing here may be attributed to Okta. ' +
      'This is not a denial.';
  } else {
    kind = 'unknown';
    headline = done.outcome || 'Finished.';
    sub = '';
  }

  const box = el('div', { class: 'verdict verdict-' + kind });
  box.appendChild(el('p', { class: 'verdict-headline', text: headline }));
  if (sub) box.appendChild(el('p', { class: 'verdict-sub', text: sub }));

  // The refusal, verbatim and large. This is the sentence the demo turns on, so it is
  // quoted rather than paraphrased and it is not tucked into a panel. The label states
  // whose words they are, because Okta's own error body and a refusal relayed by the
  // gateway are different provenances even when the text inside them is identical.
  const QUOTE_LABEL = {
    okta: 'Okta’s exact words:',
    gateway: 'The gateway’s exact words, with Okta’s reason inside them:',
    sentinel: 'This wording is this application’s, not Okta’s:',
  };

  if (err) {
    box.appendChild(el('p', {
      class: 'verdict-quote-label',
      text: QUOTE_LABEL[err.source] || QUOTE_LABEL.sentinel,
    }));
    const quote = el('div', { class: 'verdict-quote' });
    if (err.error) quote.appendChild(el('div', { class: 'verdict-code', text: err.error }));
    if (err.error_description) {
      quote.appendChild(el('div', { class: 'verdict-desc', text: err.error_description }));
    }
    box.appendChild(quote);
  }

  host.appendChild(box);
}

async function loadConfig() {
  const res = await fetch(API + '/api/config', { headers: { Accept: 'application/json' } });
  if (!res.ok) throw new Error('HTTP ' + res.status);
  return res.json();
}

// fallbackConfig lets the page draw its idle state with no backend at all. It carries no
// resource URLs or scopes, because inventing them would be putting words in the tenant's
// mouth. Only the direct path is described: whether a gateway is reachable is exactly the
// thing an unreachable API cannot tell us, so no gateway path is offered.
function fallbackConfig() {
  return {
    ready: false,
    missing: [],
    default_path: 'okta',
    paths: { okta: directFallback() },
  };
}

function directFallback() {
  return {
    id: 'okta',
    name: 'Direct to Okta, no gateway',
    available: false,
    unavailable: 'the API is not reachable',
    summary: 'The delegation with nothing in the middle.',
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
    // No API means no way to know which scope a refusal run would ask for, so no refusal
    // button is offered rather than one that might name the wrong scope.
    deny: { available: false, scopes: [] },
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

// Every button that starts a run, so they can be disabled together while one is in flight.
const RUN_BUTTONS = ['run', 'rerun', 'run-deny'];

function setRunButtons(disabled) {
  for (const id of RUN_BUTTONS) {
    const node = document.getElementById(id);
    if (node) node.disabled = disabled;
  }
}

async function run(mode) {
  if (state.running) return;
  state.running = true;
  state.mode = mode === 'deny' ? 'deny' : 'grant';
  state.steps = {};
  state.selected = null;

  // Reset the observed profiles so the diagram only ever shows what THIS run's tokens
  // stated. Carrying them over from a previous run would let a refused run appear to have
  // produced evidence it never got.
  state.profiles = {};

  setRunButtons(true);
  document.getElementById('rerun').hidden = true;
  clear(document.getElementById('verdict'));
  setStatus(state.mode === 'deny'
    ? 'Running, asking for a scope the connection is not expected to grant…'
    : 'Running…', 'busy');
  renderSteps();
  renderDetail();
  paintStates();

  try {
    const query = '?path=' + encodeURIComponent(state.path) +
      (state.mode === 'deny' ? '&mode=deny' : '');
    const res = await fetch(API + '/api/run' + query, {
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
    setRunButtons(false);
  }
}

function handleEvent({ event, data }) {
  if (!data) return;

  if (event === 'step') {
    state.steps[data.step] = data;
    observeProfiles(data.token);
    renderSteps();
    paintStates();
    // Keep an open panel current as later events for the same hop arrive.
    if (state.selected) renderDetail();
    return;
  }

  if (event === 'done') {
    setStatus(data.outcome || 'Finished.', data.ok ? 'good' : 'bad');
    renderVerdict(data);
    document.getElementById('run').hidden = true;
    document.getElementById('rerun').hidden = false;

    // Open the hop that decided the run, not simply the last one. On a failure the last
    // step is "not attempted", whose panel explains nothing; the first step that was
    // refused or did not complete is the one worth reading.
    const decided = decidingStep();

    const finished = stepOrder()
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

  // The API decides which demonstration opens, because deciding it means probing the
  // gateway and only the API can do that.
  const paths = state.config.paths || {};
  state.path = paths[state.config.default_path] ? state.config.default_path
    : Object.keys(paths)[0] || 'okta';

  renderPathChooser();
  labelRunButton();
  drawDiagram();
  renderSteps();
  renderDetail();
  setUpDenyButton();
  reportPathAvailability();

  // Wired once. setUpDenyButton runs again on every path switch and only relabels, so
  // attaching there would stack a listener per switch.
  document.getElementById('run').addEventListener('click', () => run('grant'));
  document.getElementById('rerun').addEventListener('click', () => run('grant'));
  document.getElementById('run-deny').addEventListener('click', () => run('deny'));
}

// renderPathChooser offers the two demonstrations. An unavailable one is shown disabled
// with the reason, rather than hidden: "the gateway registered no tools" is information,
// and a button that has quietly vanished is not.
function renderPathChooser() {
  const host = document.getElementById('paths');
  if (!host) return;
  clear(host);

  const paths = state.config.paths || {};
  const ids = Object.keys(paths);
  if (ids.length < 2) {
    host.hidden = true;
    return;
  }
  host.hidden = false;

  // Gateway first: it is the demonstration, and the direct path is the control.
  ids.sort((a, b) => (a === 'gateway' ? -1 : b === 'gateway' ? 1 : 0));

  for (const id of ids) {
    const p = paths[id];
    const selected = id === state.path;

    const button = el('button', {
      type: 'button',
      class: 'path-button' + (selected ? ' selected' : ''),
      // Lets CSS address one specific demonstration, which is how the no-gateway path is
      // hidden for a customer who asked to see the gateway. Purely an attribute: it
      // changes no behaviour, and the path stays fully runnable via the API.
      'data-path': id,
      'aria-pressed': selected ? 'true' : 'false',
      disabled: p.available ? null : 'disabled',
      title: p.available ? p.summary : p.unavailable,
      onclick: () => selectPath(id),
    }, [
      el('span', { class: 'path-name', text: p.name }),
      el('span', {
        class: 'path-note',
        text: p.available ? p.summary : 'unavailable: ' + p.unavailable,
      }),
    ]);
    host.appendChild(button);
  }
}

function selectPath(id) {
  if (state.running || id === state.path) return;
  state.path = id;
  state.steps = {};
  state.profiles = {};
  state.selected = null;

  clear(document.getElementById('verdict'));
  document.getElementById('run').hidden = false;
  document.getElementById('rerun').hidden = true;

  renderPathChooser();
  labelRunButton();
  drawDiagram();
  renderSteps();
  renderDetail();
  setUpDenyButton();
  reportPathAvailability();
}

// labelRunButton fills the ALLOWED row. The buttons stay "Run" and "Run again": the row's
// label already says what will happen, so repeating it on the button made the button long
// and the outcome hard to spot. What varies per path is the thing being named, a tool on
// the gateway path and a scope set on the direct one, and it comes from /api/config rather
// than from this file.
function labelRunButton() {
  document.getElementById('run').textContent = 'Run';
  document.getElementById('rerun').textContent = 'Run again';

  const grant = activePath().grant || {};
  const target = grant.tool || (grant.scopes || []).join(' ');
  setActionTool('grant-tool', target);
}

// setActionTool writes a tool or scope name into an action row, and hides the element when
// there is nothing to name so an empty box never appears next to a label.
function setActionTool(id, target) {
  const node = document.getElementById(id);
  if (!node) return;
  node.textContent = target || '';
  node.hidden = !target;
}

// reportPathAvailability says plainly when the selected demonstration cannot run. The
// gateway having registered no tools is the failure this setup actually has, and it looks
// nothing like its cause, so it is worth stating rather than leaving to a failed run.
function reportPathAvailability() {
  const p = activePath();
  if (p.available === false && p.unavailable) {
    setStatus('This demonstration cannot run: ' + p.unavailable, 'bad');
    setRunButtons(true);
    return;
  }
  setRunButtons(false);
  setStatus('Not run yet.', null);
}

// setUpDenyButton labels the refusal button with the scope it will actually ask for, taken
// from the API rather than written into this page, and hides it entirely when the API
// offers no refusal run. A button that names the wrong scope would be worse than none.
function setUpDenyButton() {
  const row = document.getElementById('action-deny');
  const button = document.getElementById('run-deny');
  if (!row || !button) return;

  const deny = activePath().deny || {};
  // The two paths arrange the refusal differently: the gateway path calls a tool whose
  // scope was never granted, the direct path asks for that scope outright. Both name the
  // thing they will be refused, taken from the API.
  const target = deny.tool || (deny.scopes || []).join(' ');

  // The whole ROW is hidden when there is no refusal run, not just the button. Leaving a
  // "Tool not allowed" label on screen with no way to run it would read as a broken
  // control rather than as an absent one.
  if (!deny.available || !target) {
    row.hidden = true;
    return;
  }

  row.hidden = false;
  button.textContent = 'Run';
  setActionTool('deny-tool', target);
}

start();
