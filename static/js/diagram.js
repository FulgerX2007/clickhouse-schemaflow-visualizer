/* Dagre-laid-out SVG renderer for ClickHouse Schema Flow Visualizer.
 *
 * Exports:
 *   renderDataFlow(container, graph, { onNodeClick })
 *   renderRelationships(container, graph, { onTableClick })
 *   renderDashboardUsage(container, usage, { engineType, hideUnconnected, panelMode, onOpen })
 *
 * graph payloads come from /api/dataflow/:db/:table and
 * /api/relationships/:db/:table — see models/graph.go for the shape. The usage
 * payload is the TableUsage from /api/grafana/usage/:db/:table — see
 * models/usage.go. renderDashboardUsage never builds a URL itself; onOpen hands
 * the reference back to the caller, which owns the scheme check.
 *
 * No string-templating, no parser sanitisation; nodes are drawn as the
 * Variant A "colored rail" card straight from data. */
(function (global) {
    'use strict';

    const SVG_NS = 'http://www.w3.org/2000/svg';
    const XHTML_NS = 'http://www.w3.org/1999/xhtml';

    // ─── Helpers ──────────────────────────────────────────────────────────
    function el(tag, attrs, children) {
        const node = document.createElementNS(SVG_NS, tag);
        if (attrs) {
            for (const k of Object.keys(attrs)) {
                if (attrs[k] != null) node.setAttribute(k, attrs[k]);
            }
        }
        if (children) {
            for (const c of [].concat(children)) {
                if (c != null) node.appendChild(typeof c === 'string' ? document.createTextNode(c) : c);
            }
        }
        return node;
    }

    function escapeText(s) {
        return String(s == null ? '' : s);
    }

    function cssAttr(s) {
        if (window.CSS && CSS.escape) return CSS.escape(String(s));
        return String(s).replace(/(["\\])/g, '\\$1');
    }

    function formatRows(n) {
        if (n == null) return null;
        if (n >= 1e9) return (n / 1e9).toFixed(2) + 'B';
        if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
        if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K';
        return Number(n).toLocaleString();
    }
    function formatBytes(n) {
        if (!n) return null;
        const units = ['B', 'KB', 'MB', 'GB', 'TB'];
        let size = n, i = 0;
        while (size >= 1024 && i < units.length - 1) { size /= 1024; i++; }
        return size.toFixed(1) + ' ' + units[i];
    }

    function getDagre() {
        const d = global.dagre || (global.window && global.window.dagre);
        if (!d) throw new Error('dagre is not loaded');
        return d;
    }

    // Edge path builder: dagre returns an array of x/y points along the edge.
    // We use a smooth cubic Bezier across consecutive segments.
    function buildEdgePath(points) {
        if (!points || points.length < 2) return '';
        const p0 = points[0];
        let d = `M ${p0.x},${p0.y}`;
        for (let i = 1; i < points.length; i++) {
            const p = points[i];
            const prev = points[i - 1];
            const mx = (prev.x + p.x) / 2;
            const my = (prev.y + p.y) / 2;
            d += ` Q ${prev.x},${prev.y} ${mx},${my}`;
            if (i === points.length - 1) d += ` T ${p.x},${p.y}`;
        }
        return d;
    }

    // ─── Pan & zoom ───────────────────────────────────────────────────────
    function attachPanZoom(svg, viewportGroup, opts) {
        const state = { zoom: 1, tx: 0, ty: 0 };
        const minZoom = (opts && opts.minZoom) || 0.3;
        const maxZoom = (opts && opts.maxZoom) || 4;

        function apply() {
            viewportGroup.setAttribute(
                'transform',
                `translate(${state.tx},${state.ty}) scale(${state.zoom})`,
            );
        }

        svg.addEventListener('wheel', (e) => {
            if (!(e.ctrlKey || e.metaKey)) return;
            e.preventDefault();
            const rect = svg.getBoundingClientRect();
            const cx = e.clientX - rect.left;
            const cy = e.clientY - rect.top;
            const factor = e.deltaY < 0 ? 1.1 : 1 / 1.1;
            const newZoom = Math.max(minZoom, Math.min(maxZoom, state.zoom * factor));
            // zoom around cursor: keep the point under cursor fixed
            const k = newZoom / state.zoom;
            state.tx = cx - k * (cx - state.tx);
            state.ty = cy - k * (cy - state.ty);
            state.zoom = newZoom;
            apply();
        }, { passive: false });

        let dragging = false;
        let startX = 0, startY = 0, startTx = 0, startTy = 0;
        svg.addEventListener('mousedown', (e) => {
            if (e.button !== 0) return;
            // ignore drag start on interactive elements
            if (e.target.closest('[data-interactive]')) return;
            dragging = true;
            startX = e.clientX;
            startY = e.clientY;
            startTx = state.tx;
            startTy = state.ty;
            svg.style.cursor = 'grabbing';
            e.preventDefault();
        });
        window.addEventListener('mousemove', (e) => {
            if (!dragging) return;
            state.tx = startTx + (e.clientX - startX);
            state.ty = startTy + (e.clientY - startY);
            apply();
        });
        window.addEventListener('mouseup', () => {
            if (dragging) {
                dragging = false;
                svg.style.cursor = 'grab';
            }
        });
        svg.style.cursor = 'grab';

        return {
            zoomIn:  () => { state.zoom = Math.min(maxZoom, state.zoom * 1.15); apply(); },
            zoomOut: () => { state.zoom = Math.max(minZoom, state.zoom / 1.15); apply(); },
            reset:   () => { state.zoom = 1; state.tx = 0; state.ty = 0; apply(); },
            fit: (bbox) => {
                const rect = svg.getBoundingClientRect();
                const pad = 24;
                const sx = (rect.width  - 2 * pad) / Math.max(1, bbox.w);
                const sy = (rect.height - 2 * pad) / Math.max(1, bbox.h);
                state.zoom = Math.max(minZoom, Math.min(maxZoom, Math.min(sx, sy)));
                state.tx = (rect.width  - bbox.w * state.zoom) / 2 - bbox.x * state.zoom;
                state.ty = (rect.height - bbox.h * state.zoom) / 2 - bbox.y * state.zoom;
                apply();
            },
        };
    }

    // ─── Data-flow node ───────────────────────────────────────────────────
    const NODE_W = 320;
    const NODE_H_BASE = 50;
    const NODE_H_WITH_META = 70;

    function buildDataFlowNode(node) {
        const hasMeta = node.total_rows != null || node.total_bytes != null;
        const h = hasMeta ? NODE_H_WITH_META : NODE_H_BASE;
        const engineClass = `engine-${node.engine_type || 'mergetree'}`;
        const isCurrent = !!node.current;

        const g = el('g', {
            class: `df-node ${engineClass}${isCurrent ? ' current' : ''}`,
            'data-id': node.id,
            'data-interactive': '1',
        });

        // selection halo for current node
        if (isCurrent) {
            g.appendChild(el('rect', {
                class: 'df-halo',
                x: -3, y: -3,
                width: NODE_W + 6, height: h + 6,
                rx: 9,
            }));
        }

        g.appendChild(el('rect', {
            class: 'df-card',
            x: 0, y: 0,
            width: NODE_W, height: h,
            rx: 6,
        }));
        g.appendChild(el('rect', {
            class: 'df-rail',
            x: 0, y: 0,
            width: 3, height: h,
        }));

        // Table name
        const tbName = node.table || node.id;
        const dbName = node.database || '';
        const engineName = node.engine || '';

        const title = el('text', {
            class: 'df-title',
            x: 14, y: 19,
        }, tbName);
        g.appendChild(title);

        const subParts = [dbName, engineName].filter(Boolean).join(' · ');
        if (subParts) {
            g.appendChild(el('text', {
                class: 'df-sub',
                x: 14, y: 32,
            }, subParts));
        }

        if (hasMeta) {
            const rowsStr = formatRows(node.total_rows);
            const sizeStr = formatBytes(node.total_bytes);
            const metaStr = [rowsStr ? `Rows: ${rowsStr}` : null, sizeStr ? `Size: ${sizeStr}` : null]
                .filter(Boolean).join('  ');
            if (metaStr) {
                g.appendChild(el('text', {
                    class: 'df-meta',
                    x: 14, y: h - 12,
                }, metaStr));
            }
        }

        // "CURRENT" pin in top-right
        if (isCurrent) {
            const pinG = el('g', { transform: `translate(${NODE_W - 56},6)` });
            pinG.appendChild(el('rect', { class: 'df-pin', width: 48, height: 14, rx: 3 }));
            pinG.appendChild(el('text', {
                class: 'df-pin-text', x: 24, y: 10, 'text-anchor': 'middle',
            }, 'CURRENT'));
            g.appendChild(pinG);
        }

        return { g, w: NODE_W, h };
    }

    // ─── Data-flow renderer ───────────────────────────────────────────────
    function renderDataFlow(container, graph, opts) {
        const dagre = getDagre();
        opts = opts || {};
        container.innerHTML = '';

        if (!graph || !graph.nodes || graph.nodes.length === 0) {
            container.appendChild(emptyState('No data flow available for this table.'));
            return null;
        }

        // Build dagre graph
        const dg = new dagre.graphlib.Graph();
        dg.setGraph({
            rankdir: 'TB',
            nodesep: 28,
            ranksep: 44,
            marginx: 16,
            marginy: 16,
        });
        dg.setDefaultEdgeLabel(() => ({}));

        const nodeGs = new Map();
        for (const n of graph.nodes) {
            const { g, w, h } = buildDataFlowNode(n);
            nodeGs.set(n.id, g);
            dg.setNode(n.id, { width: w, height: h });
        }
        for (const e of graph.edges) {
            dg.setEdge(e.from, e.to, { label: e.label });
        }

        dagre.layout(dg);

        // Build SVG
        const svg = el('svg', {
            xmlns: SVG_NS,
            class: 'flow-svg dataflow',
            width: '100%',
            height: '100%',
        });
        const defs = el('defs');
        defs.appendChild(arrowMarker('df-arrow', 'currentColor'));
        defs.appendChild(arrowMarker('df-arrow-active', 'var(--accent)'));
        svg.appendChild(defs);

        const viewport = el('g', { class: 'viewport' });
        svg.appendChild(viewport);

        // Edges first so nodes draw on top
        const edgesG = el('g', { class: 'edges' });
        viewport.appendChild(edgesG);
        for (const e of dg.edges()) {
            const edge = dg.edge(e);
            const fromNode = graph.nodes.find((n) => n.id === e.v);
            const toNode = graph.nodes.find((n) => n.id === e.w);
            const active = (fromNode && fromNode.current) || (toNode && toNode.current);
            const path = el('path', {
                class: 'df-edge' + (active ? ' active' : ''),
                d: buildEdgePath(edge.points),
                'marker-end': `url(#${active ? 'df-arrow-active' : 'df-arrow'})`,
            });
            edgesG.appendChild(path);
        }

        // Nodes
        const nodesG = el('g', { class: 'nodes' });
        viewport.appendChild(nodesG);
        for (const id of dg.nodes()) {
            const layout = dg.node(id);
            const g = nodeGs.get(id);
            if (!g) continue;
            // dagre's (x, y) is the centre — convert to top-left
            const x = layout.x - layout.width / 2;
            const y = layout.y - layout.height / 2;
            g.setAttribute('transform', `translate(${x},${y})`);
            if (opts.onNodeClick) {
                g.style.cursor = 'pointer';
                g.addEventListener('click', () => opts.onNodeClick(id));
            }
            nodesG.appendChild(g);
        }

        container.appendChild(svg);

        // Set viewBox so the SVG scales properly
        const gw = dg.graph().width || 600;
        const gh = dg.graph().height || 400;
        svg.setAttribute('viewBox', `0 0 ${Math.max(gw, 600)} ${Math.max(gh, 200)}`);
        svg.setAttribute('preserveAspectRatio', 'xMidYMin meet');

        const panzoom = attachPanZoom(svg, viewport);
        return { svg, panzoom, width: gw, height: gh };
    }

    // ─── Relationships: column-level renderer ────────────────────────────
    const COL_ROW_H = 22;
    const TABLE_HEADER_H = 38;
    const TABLE_W = 240;

    function buildRelTable(table) {
        const engineClass = `engine-${table.engine_type || 'mergetree'}`;
        const isCurrent = table.role === 'current';
        const colCount = table.columns.length;
        const h = TABLE_HEADER_H + colCount * COL_ROW_H + 8;

        const g = el('g', {
            class: `rel-table ${engineClass}${isCurrent ? ' current' : ''}`,
            'data-id': table.id,
            'data-interactive': '1',
        });

        if (isCurrent) {
            g.appendChild(el('rect', {
                class: 'rel-halo',
                x: -3, y: -3,
                width: TABLE_W + 6, height: h + 6,
                rx: 9,
            }));
        }

        g.appendChild(el('rect', {
            class: 'rel-card', x: 0, y: 0, width: TABLE_W, height: h, rx: 6,
        }));
        g.appendChild(el('rect', {
            class: 'rel-rail', x: 0, y: 0, width: 3, height: h,
        }));

        // Top row: small role label (left) and engine name (right) share the
        // same baseline since both use the 9.5px muted font. The bold table
        // name then gets the full width of row 2 to itself. The role is
        // omitted when the engine name would otherwise crash into it (e.g.
        // ReplicatedAggregatingMergeTree, 30 chars) — the highlight halo and
        // arrow direction already convey the role visually.
        const engineName = table.engine || '';
        if (engineName.length <= 24) {
            g.appendChild(el('text', { class: 'rel-role', x: 14, y: 14 }, table.role));
        }
        g.appendChild(el('text', {
            class: 'rel-engine', x: TABLE_W - 12, y: 14, 'text-anchor': 'end',
        }, engineName));
        g.appendChild(el('text', { class: 'rel-name', x: 14, y: 30 }, table.table));

        g.appendChild(el('line', {
            class: 'rel-divider',
            x1: 0, y1: TABLE_HEADER_H, x2: TABLE_W, y2: TABLE_HEADER_H,
        }));

        // Column rows. Each row has a transparent hit-rect that acts both as
        // a click target and as the visible background when the row is focused.
        table.columns.forEach((col, i) => {
            const y = TABLE_HEADER_H + i * COL_ROW_H;
            const row = el('g', {
                class: 'rel-col',
                'data-table': table.id,
                'data-column': col.name,
                'data-interactive': '1',
                transform: `translate(0,${y})`,
            });
            if (i % 2 === 1) {
                row.appendChild(el('rect', { class: 'rel-col-bg', x: 4, y: 0, width: TABLE_W - 8, height: COL_ROW_H, rx: 3 }));
            }
            row.appendChild(el('rect', { class: 'rel-col-hit', x: 4, y: 0, width: TABLE_W - 8, height: COL_ROW_H, rx: 3 }));
            // Same opposite-anchor problem as the dashboard card: an
            // AggregateFunction type is wider than the card it shares with the
            // column name, and printed in full it runs through it.
            const fitted = fitRowPair(col.name, col.type, TABLE_W - 34);
            const nameEl = el('text', { class: 'rel-col-name', x: 14, y: 14 }, fitted.left);
            if (fitted.leftClipped) nameEl.appendChild(el('title', null, col.name));
            row.appendChild(nameEl);
            const typeEl = el('text', {
                class: 'rel-col-type', x: TABLE_W - 12, y: 14, 'text-anchor': 'end',
            }, fitted.right);
            if (fitted.rightClipped) typeEl.appendChild(el('title', null, col.type));
            row.appendChild(typeEl);
            g.appendChild(row);
        });

        return { g, w: TABLE_W, h, colY: (i) => TABLE_HEADER_H + i * COL_ROW_H + COL_ROW_H / 2 };
    }

    function renderRelationships(container, graph, opts) {
        const dagre = getDagre();
        opts = opts || {};
        container.innerHTML = '';

        if (!graph || !graph.tables || graph.tables.length === 0) {
            container.appendChild(emptyState('No column-level relationships available for this table.'));
            return null;
        }

        const tableMap = new Map(graph.tables.map((t) => [t.id, t]));
        const builders = new Map();
        for (const t of graph.tables) {
            builders.set(t.id, buildRelTable(t));
        }

        // Dagre layout — table-level only; column row positions are computed
        // analytically inside each table.
        const dg = new dagre.graphlib.Graph();
        dg.setGraph({
            rankdir: 'LR',
            nodesep: 40,
            ranksep: 120,
            marginx: 24,
            marginy: 24,
        });
        dg.setDefaultEdgeLabel(() => ({}));

        for (const t of graph.tables) {
            const b = builders.get(t.id);
            dg.setNode(t.id, { width: b.w, height: b.h });
        }
        // Use one synthetic edge per pair of tables so dagre orders them; we draw
        // the actual column-level edges manually after layout.
        const pairKey = (a, b) => `${a}->${b}`;
        const seenPair = new Set();
        for (const e of graph.edges) {
            const k = pairKey(e.from_table, e.to_table);
            if (seenPair.has(k)) continue;
            seenPair.add(k);
            dg.setEdge(e.from_table, e.to_table);
        }

        dagre.layout(dg);

        // Compose SVG
        const svg = el('svg', {
            xmlns: SVG_NS,
            class: 'flow-svg relationships',
            width: '100%',
            height: '100%',
        });
        const defs = el('defs');
        defs.appendChild(arrowMarker('rel-arrow', 'var(--accent)'));
        svg.appendChild(defs);

        const viewport = el('g', { class: 'viewport' });
        svg.appendChild(viewport);

        // Place tables
        const tablePos = new Map();
        const nodesG = el('g', { class: 'tables' });
        viewport.appendChild(nodesG);
        for (const id of dg.nodes()) {
            const layout = dg.node(id);
            const b = builders.get(id);
            if (!b) continue;
            const x = layout.x - layout.width / 2;
            const y = layout.y - layout.height / 2;
            b.g.setAttribute('transform', `translate(${x},${y})`);
            tablePos.set(id, { x, y, w: b.w, h: b.h, colY: b.colY });
            if (opts.onTableClick) {
                b.g.style.cursor = 'pointer';
                b.g.addEventListener('click', (ev) => {
                    // only fire if user clicked the header strip, not a column
                    if (ev.target.closest('.rel-col')) return;
                    opts.onTableClick(id);
                });
            }
            nodesG.appendChild(b.g);
        }

        // Draw column-level edges with optional expression labels.
        // We keep a per-column index so clicking a row can light up its edges.
        const edgesG = el('g', { class: 'rel-edges' });
        viewport.insertBefore(edgesG, nodesG);

        // columnKey -> { row, edges:[], labels:[], partners:Set<columnKey> }
        const colIndex = new Map();
        const colKey = (table, col) => `${table}::${col}`;
        const registerCol = (key) => {
            if (colIndex.has(key)) return colIndex.get(key);
            const [tableId, columnName] = key.split('::');
            const row = svg.querySelector(
                `g.rel-col[data-table="${cssAttr(tableId)}"][data-column="${cssAttr(columnName)}"]`,
            );
            const entry = { row, edges: [], labels: [], partners: new Set() };
            colIndex.set(key, entry);
            return entry;
        };

        for (const e of graph.edges) {
            const a = tablePos.get(e.from_table);
            const b = tablePos.get(e.to_table);
            if (!a || !b) continue;
            const fromT = tableMap.get(e.from_table);
            const toT = tableMap.get(e.to_table);
            const fromIdx = fromT.columns.findIndex((c) => c.name === e.from_column);
            const toIdx = toT.columns.findIndex((c) => c.name === e.to_column);
            if (fromIdx < 0 || toIdx < 0) continue;

            const x1 = a.x + a.w;
            const y1 = a.y + a.colY(fromIdx);
            const x2 = b.x;
            const y2 = b.y + b.colY(toIdx);
            const mx = (x1 + x2) / 2;

            const d = `M ${x1},${y1} C ${mx},${y1} ${mx},${y2} ${x2},${y2}`;
            const pathEl = el('path', {
                class: 'rel-edge',
                d,
                'marker-end': 'url(#rel-arrow)',
            });
            edgesG.appendChild(pathEl);

            let labelEl = null;
            if (e.expression && e.expression !== '—') {
                const labelW = Math.min(220, Math.max(48, e.expression.length * 6.5));
                labelEl = el('g', { class: 'rel-edge-label', transform: `translate(${mx - labelW / 2},${(y1 + y2) / 2 - 9})` });
                labelEl.appendChild(el('rect', { width: labelW, height: 18, rx: 3 }));
                const text = el('text', { x: labelW / 2, y: 13, 'text-anchor': 'middle' });
                text.appendChild(document.createTextNode(truncateExpression(e.expression, Math.floor(labelW / 6.2))));
                const titleEl = el('title');
                titleEl.appendChild(document.createTextNode(e.expression));
                text.appendChild(titleEl);
                labelEl.appendChild(text);
                edgesG.appendChild(labelEl);
            }

            const fromKey = colKey(e.from_table, e.from_column);
            const toKey   = colKey(e.to_table,   e.to_column);
            const fromEntry = registerCol(fromKey);
            const toEntry   = registerCol(toKey);
            fromEntry.edges.push(pathEl);
            toEntry.edges.push(pathEl);
            if (labelEl) {
                fromEntry.labels.push(labelEl);
                toEntry.labels.push(labelEl);
            }
            fromEntry.partners.add(toKey);
            toEntry.partners.add(fromKey);
        }

        // Wire focus mode: click a column row to light up its edges and partners.
        let focusKey = null;
        function applyFocus(nextKey) {
            svg.querySelectorAll('.is-focus, .is-related, .is-dim').forEach((node) => {
                node.classList.remove('is-focus', 'is-related', 'is-dim');
            });
            svg.classList.remove('has-focus');
            focusKey = nextKey && colIndex.has(nextKey) ? nextKey : null;
            if (!focusKey) return;

            svg.classList.add('has-focus');
            const entry = colIndex.get(focusKey);
            if (entry.row) entry.row.classList.add('is-focus');
            entry.edges.forEach((eEl)  => eEl.classList.add('is-related'));
            entry.labels.forEach((lEl) => lEl.classList.add('is-related'));
            entry.partners.forEach((pKey) => {
                const p = colIndex.get(pKey);
                if (p && p.row) p.row.classList.add('is-related');
            });
        }

        colIndex.forEach((entry, key) => {
            if (!entry.row) return;
            entry.row.addEventListener('click', (ev) => {
                ev.stopPropagation();
                applyFocus(focusKey === key ? null : key);
            });
        });

        svg.addEventListener('click', (ev) => {
            if (ev.target.closest('.rel-col')) return;
            applyFocus(null);
        });

        const escHandler = (ev) => {
            if (ev.key === 'Escape' && focusKey) {
                ev.preventDefault();
                applyFocus(null);
            }
        };
        document.addEventListener('keydown', escHandler);
        // Tidy up when the container is re-rendered.
        const cleanupObs = new MutationObserver(() => {
            if (!container.contains(svg)) {
                document.removeEventListener('keydown', escHandler);
                cleanupObs.disconnect();
            }
        });
        cleanupObs.observe(container, { childList: true });

        const gw = dg.graph().width || 800;
        const gh = dg.graph().height || 400;
        svg.setAttribute('viewBox', `0 0 ${Math.max(gw, 600)} ${Math.max(gh, 200)}`);
        svg.setAttribute('preserveAspectRatio', 'xMidYMin meet');

        container.appendChild(svg);
        const panzoom = attachPanZoom(svg, viewport);
        return { svg, panzoom, width: gw, height: gh };
    }

    // ─── Dashboard usage: table columns ⇄ Grafana panels ──────────────────
    //
    // A strictly bipartite picture: the selected table on the left, one card
    // per dashboard on the right, one line per (column, panel) pair that reads
    // it. Laid out by hand rather than by dagre — with exactly two ranks the
    // only interesting decision is the vertical order of the dashboards, and
    // ordering them by the average row they connect to removes most of the
    // crossings dagre would leave behind.
    const DU_TABLE_W = 268;
    const DU_DASH_W = 300;
    const DU_ROW_H = 22;
    const DU_HEADER_H = 38;
    const DU_CARD_PAD = 8;
    const DU_RANK_GAP = 190;
    // Above this many panels the view collapses to one row per dashboard unless
    // the caller asks otherwise; above this many rows the rest are dropped with
    // a stated count; above this many edges the resting state fades so focus reads.
    const DU_AUTO_PANEL_LIMIT = 12;
    const DU_MAX_ROWS = 30;
    const DU_DENSE_EDGES = 120;
    const DU_STACK_GAP = 26;
    const DU_MARGIN = 24;

    // panelKey identifies one panel. The dashboard uid plus the panel id is what
    // makes a panel unique — Grafana is happy to hold two panels with the same
    // title, and a repeated row produces exactly that.
    function panelKey(ref) {
        return `${ref.dashboard_uid || ref.dashboard_title || '?'}::${ref.panel_id || 0}`;
    }

    // newColumnClaim / mergeClaim record what one panel can be said to read of
    // one column, and — the part that decides whether a line is drawn at all —
    // how precisely.
    //
    // The two lineage families are not equally precise, and treating them alike
    // is what turns this view into a hairball. propagateDistributed carries each
    // column across a wrapper individually, so a "distributed:" reference names
    // a real column. propagateViews cannot: it is a cross product of every
    // column a view's SELECT touches against every panel reading that view's
    // destination, deliberately, so that "SELECT a + b AS c" cannot make b look
    // droppable. That makes an "mv:" reference a statement about the table, not
    // about the column it is filed under — one panel reading a rollup produced
    // 39 identical lines out of a 40-column table here, burying the single
    // column it actually named.
    function newColumnClaim() {
        return { direct: false, precise: false, via: '', blanketVia: '', heuristic: false };
    }

    function mergeClaim(claim, ref) {
        if (!ref.via) {
            claim.direct = true;
            claim.precise = true;
        } else if (ref.via.startsWith('mv:')) {
            claim.blanketVia = ref.via;
        } else {
            claim.precise = true;
            claim.via = ref.via;
        }
        if (ref.confidence === 'heuristic') claim.heuristic = true;
        return claim;
    }

    // namedColumns returns only the columns a row can be said to name. Empty
    // means the row reads the table without naming any column of it.
    function namedColumns(columns) {
        return [...columns.entries()].filter(([, meta]) => meta.precise);
    }

    // groupDashboards turns a /api/grafana/usage payload into the right-hand
    // rank: dashboards holding panels, each panel holding the columns it reads.
    //
    // Column references are read first so that a panel already attributed to a
    // column is not later mistaken for a table-level one. A panel that survives
    // with tableLevel still set reads the table but named no column the parser
    // could resolve — the visual form of the "unknown" verdict, and the reason
    // its line lands on the table header instead of on a row.
    function groupDashboards(usage) {
        const dashboards = new Map();

        function panelEntry(ref) {
            const dashKey = ref.dashboard_uid || ref.dashboard_title || '?';
            if (!dashboards.has(dashKey)) {
                dashboards.set(dashKey, { key: dashKey, ref, panels: new Map() });
            }
            const dash = dashboards.get(dashKey);
            const key = panelKey(ref);
            if (!dash.panels.has(key)) {
                dash.panels.set(key, { key, ref, columns: new Map(), tableLevel: true });
            }
            return dash.panels.get(key);
        }

        for (const column of usage.columns || []) {
            for (const ref of [].concat(column.direct || [], column.derived || [])) {
                const panel = panelEntry(ref);
                panel.tableLevel = false;
                const existing = panel.columns.get(column.column) || newColumnClaim();
                mergeClaim(existing, ref);
                panel.columns.set(column.column, existing);
            }
        }

        for (const ref of usage.dashboards || []) {
            panelEntry(ref);
        }

        return [...dashboards.values()].map((dash) => {
            const panels = [...dash.panels.values()].sort(
                (a, b) => (a.ref.panel_id || 0) - (b.ref.panel_id || 0),
            );
            // The union across the dashboard's panels, for the collapsed row.
            // Merged the same way a single panel merges its own references: a
            // direct read anywhere in the dashboard is still a direct read.
            const columns = new Map();
            panels.forEach((panel) => panel.columns.forEach((meta, name) => {
                const existing = columns.get(name) || newColumnClaim();
                if (meta.direct) existing.direct = true;
                if (meta.precise) existing.precise = true;
                if (meta.via) existing.via = meta.via;
                if (meta.blanketVia) existing.blanketVia = meta.blanketVia;
                if (meta.heuristic) existing.heuristic = true;
                columns.set(name, existing);
            }));
            return { key: dash.key, ref: dash.ref, panels, columns };
        }).sort((a, b) => String(a.ref.dashboard_title || a.key)
            .localeCompare(String(b.ref.dashboard_title || b.key)));
    }

    // buildUsageTableCard draws the left-hand card. It reuses the .rel-* classes
    // rather than cloning them: the two renderers never share an SVG, and a
    // second copy of the card styling is a second place to forget.
    function buildUsageTableCard(usage, columns, engineType, named) {
        const h = DU_HEADER_H + columns.length * DU_ROW_H + DU_CARD_PAD;
        const g = el('g', {
            class: `rel-table du-source engine-${engineType || 'mergetree'} current`,
            'data-id': `${usage.database}.${usage.table}`,
        });

        g.appendChild(el('rect', {
            class: 'rel-halo', x: -3, y: -3, width: DU_TABLE_W + 6, height: h + 6, rx: 9,
        }));
        g.appendChild(el('rect', {
            class: 'rel-card', x: 0, y: 0, width: DU_TABLE_W, height: h, rx: 6,
        }));
        g.appendChild(el('rect', { class: 'rel-rail', x: 0, y: 0, width: 3, height: h }));

        const engine = usage.engine || '';
        if (engine.length <= 24) {
            g.appendChild(el('text', { class: 'rel-role', x: 14, y: 14 }, usage.database));
        }
        g.appendChild(el('text', {
            class: 'rel-engine', x: DU_TABLE_W - 12, y: 14, 'text-anchor': 'end',
        }, engine));
        g.appendChild(el('text', { class: 'rel-name', x: 14, y: 30 }, usage.table));
        g.appendChild(el('line', {
            class: 'rel-divider', x1: 0, y1: DU_HEADER_H, x2: DU_TABLE_W, y2: DU_HEADER_H,
        }));

        columns.forEach((column, i) => {
            const y = DU_HEADER_H + i * DU_ROW_H;
            // Two independent facts, two channels. Colour is the verdict — red
            // still means, and only means, safe to drop. Fill is whether any
            // query names the column: hollow says nothing points at it, which on
            // a table feeding a materialized view is normal and is emphatically
            // not the same as droppable.
            const isNamed = !named || named.has(column.column);
            const row = el('g', {
                class: `rel-col du-col verdict-${column.verdict || 'no-coverage'}`
                    + (isNamed ? ' du-named' : ''),
                'data-column': column.column,
                'data-interactive': '1',
                transform: `translate(0,${y})`,
            });
            if (i % 2 === 1) {
                row.appendChild(el('rect', {
                    class: 'rel-col-bg', x: 4, y: 0, width: DU_TABLE_W - 8, height: DU_ROW_H, rx: 3,
                }));
            }
            row.appendChild(el('rect', {
                class: 'rel-col-hit', x: 4, y: 0, width: DU_TABLE_W - 8, height: DU_ROW_H, rx: 3,
            }));
            const dot = el('circle', { class: 'du-dot', cx: 14, cy: DU_ROW_H / 2, r: 3.5 });
            dot.appendChild(el('title', null, verdictTooltip(column, isNamed)));
            row.appendChild(dot);

            // Name and type share one row and are anchored from opposite ends,
            // so neither may be written in full. A real ClickHouse type runs to
            // "SimpleAggregateFunction(sum, UInt64)" — 36 characters, wider than
            // the whole card — and un-truncated it prints straight through the
            // column name it is supposed to sit beside.
            const fitted = fitRowPair(column.column, column.type, DU_TABLE_W - 44);
            const name = el('text', { class: 'rel-col-name', x: 24, y: 14 }, fitted.left);
            if (fitted.leftClipped) name.appendChild(el('title', null, column.column));
            row.appendChild(name);
            const type = el('text', {
                class: 'rel-col-type', x: DU_TABLE_W - 12, y: 14, 'text-anchor': 'end',
            }, fitted.right);
            if (fitted.rightClipped) type.appendChild(el('title', null, column.type));
            row.appendChild(type);
            g.appendChild(row);
        });

        return { g, w: DU_TABLE_W, h, rowY: (i) => DU_HEADER_H + i * DU_ROW_H + DU_ROW_H / 2 };
    }

    // fitRowPair splits one row's width between a left-anchored label and a
    // right-anchored one, giving the left the larger share but handing back
    // whatever the right does not need. Widths are the measured advance of the
    // two mono sizes in use (11px and 9.5px); nothing here needs sub-pixel
    // accuracy, only a guarantee that the two never overlap.
    const LEFT_CH = 6.2;
    const RIGHT_CH = 5.4;
    function fitRowPair(left, right, available) {
        const gap = 10;
        const usable = Math.max(0, available - gap);
        let leftChars = Math.min(left.length, Math.floor((usable * 0.62) / LEFT_CH));
        let rightChars = Math.max(0, Math.floor((usable - leftChars * LEFT_CH) / RIGHT_CH));
        if (rightChars > right.length) {
            rightChars = right.length;
            leftChars = Math.min(left.length,
                Math.floor((usable - rightChars * RIGHT_CH) / LEFT_CH));
        }
        return {
            left: clip(left, Math.max(4, leftChars)),
            leftClipped: left.length > leftChars,
            right: clip(right, rightChars),
            rightClipped: right.length > rightChars,
        };
    }

    // clip is truncateExpression without its 8-character floor, which would
    // overrun a budget this tight and put the two labels back on top of
    // each other — the exact thing fitRowPair exists to prevent.
    function clip(s, maxChars) {
        if (!s) return '';
        if (s.length <= maxChars) return s;
        if (maxChars <= 1) return maxChars === 1 ? '…' : '';
        return s.slice(0, maxChars - 1) + '…';
    }

    // verdictTooltip spells the verdict out. The dot alone is a colour, and a
    // colour that means "safe to drop" has to say so in words somewhere.
    function verdictTooltip(column, isNamed) {
        const words = {
            'used': 'used — a dashboard, lineage, or a storage key reads this column',
            'unused': 'unused — the table is read, every query reading it was understood, and none name this column',
            'unknown': 'unknown — the table is read but a query touching it could not be enumerated',
            'no-coverage': 'no data — nothing observed reads this table at all',
        };
        const verdict = column.verdict || 'no-coverage';
        const lines = [words[verdict]];
        if (column.reason) lines.push(column.reason);
        lines.push(isNamed
            ? 'filled dot: a dashboard query names this column'
            : 'hollow dot: no query names it — the colour says whether that makes it droppable');
        return lines.join('\n');
    }

    // buildDashboardCard draws one dashboard as a card of panel rows. This is
    // the detailed mode; see buildDashboardListCard for the one that scales.
    function buildDashboardCard(dash) {
        const h = DU_HEADER_H + dash.panels.length * DU_ROW_H + DU_CARD_PAD;
        const g = el('g', { class: 'du-dash', 'data-dashboard': dash.key });

        g.appendChild(el('rect', {
            class: 'du-card', x: 0, y: 0, width: DU_DASH_W, height: h, rx: 6,
        }));
        g.appendChild(el('rect', { class: 'du-rail', x: 0, y: 0, width: 3, height: h }));

        const header = el('g', {
            class: 'du-dash-header' + (dash.ref.dashboard_url ? ' openable' : ''),
            'data-interactive': '1',
        });
        header.appendChild(el('rect', {
            class: 'du-dash-hit', x: 4, y: 4, width: DU_DASH_W - 8, height: DU_HEADER_H - 6, rx: 4,
        }));
        header.appendChild(el('text', { class: 'du-dash-kind', x: 14, y: 14 }, 'grafana'));
        // The ↗ is a promise that clicking goes somewhere. Directory mode has no
        // base URL, so there it must not appear.
        const count = `${dash.panels.length} panel${dash.panels.length === 1 ? '' : 's'}`;
        header.appendChild(el('text', {
            class: 'du-dash-count', x: DU_DASH_W - 12, y: 14, 'text-anchor': 'end',
        }, dash.ref.dashboard_url ? `${count} ↗` : count));
        const title = el('text', { class: 'du-dash-title', x: 14, y: 30 },
            truncateExpression(dash.ref.dashboard_title || dash.key, 34));
        title.appendChild(el('title', null, dash.ref.dashboard_title || dash.key));
        header.appendChild(title);
        g.appendChild(header);

        g.appendChild(el('line', {
            class: 'rel-divider', x1: 0, y1: DU_HEADER_H, x2: DU_DASH_W, y2: DU_HEADER_H,
        }));

        const rows = dash.panels.map((panel, i) => {
            const y = DU_HEADER_H + i * DU_ROW_H;
            const row = el('g', {
                class: 'du-panel',
                'data-panel': panel.key,
                'data-interactive': '1',
                transform: `translate(0,${y})`,
            });
            if (i % 2 === 1) {
                row.appendChild(el('rect', {
                    class: 'rel-col-bg', x: 4, y: 0, width: DU_DASH_W - 8, height: DU_ROW_H, rx: 3,
                }));
            }
            row.appendChild(el('rect', {
                class: 'rel-col-hit', x: 4, y: 0, width: DU_DASH_W - 8, height: DU_ROW_H, rx: 3,
            }));

            const tag = panelTag(panel);
            const nameW = tag ? DU_DASH_W - 30 - tag.text.length * 5.6 : DU_DASH_W - 28;
            const name = el('text', { class: 'du-panel-name', x: 14, y: 14 },
                truncateExpression(panel.ref.panel_title || `panel ${panel.ref.panel_id}`,
                    Math.max(8, Math.floor(nameW / 6.2))));
            name.appendChild(el('title', null, panelTooltip(panel)));
            row.appendChild(name);

            if (tag) {
                const label = el('text', {
                    class: `du-panel-tag ${tag.kind}`,
                    x: DU_DASH_W - 12, y: 14, 'text-anchor': 'end',
                }, tag.text);
                label.appendChild(el('title', null, tag.title));
                row.appendChild(label);
            }
            g.appendChild(row);
            return {
                key: `panel::${panel.key}`,
                el: row,
                columns: panel.columns,
                tableLevel: panel.tableLevel,
                openRef: panel.ref,
                cy: DU_HEADER_H + i * DU_ROW_H + DU_ROW_H / 2,
            };
        });

        return { g, w: DU_DASH_W, h, rows, headerRef: dash.ref };
    }

    // buildDashboardListCard draws every dashboard as a single row in one card.
    //
    // This is the default above a handful of panels, and it is what makes the
    // view survive a real schema: a table read by 26 dashboards across 89 panels
    // needs 89 rows and 26 cards in the detailed mode — near 4000px of content,
    // which the viewBox then scales to a fifth of legible size. Collapsed to one
    // row per dashboard the same answer fits in 618px.
    function buildDashboardListCard(dashboards, hidden) {
        const extra = hidden > 0 ? 1 : 0;
        const h = DU_HEADER_H + (dashboards.length + extra) * DU_ROW_H + DU_CARD_PAD;
        const g = el('g', { class: 'du-dash du-dash-list' });

        g.appendChild(el('rect', {
            class: 'du-card', x: 0, y: 0, width: DU_DASH_W, height: h, rx: 6,
        }));
        g.appendChild(el('rect', { class: 'du-rail', x: 0, y: 0, width: 3, height: h }));
        g.appendChild(el('text', { class: 'du-dash-kind', x: 14, y: 14 }, 'grafana'));
        g.appendChild(el('text', {
            class: 'du-dash-count', x: DU_DASH_W - 12, y: 14, 'text-anchor': 'end',
        }, `${dashboards.length + hidden} dashboard${dashboards.length + hidden === 1 ? '' : 's'}`));
        g.appendChild(el('text', { class: 'du-dash-title', x: 14, y: 30 }, 'reading this table'));
        g.appendChild(el('line', {
            class: 'rel-divider', x1: 0, y1: DU_HEADER_H, x2: DU_DASH_W, y2: DU_HEADER_H,
        }));

        const rows = dashboards.map((dash, i) => {
            const y = DU_HEADER_H + i * DU_ROW_H;
            const row = el('g', {
                class: 'du-panel' + (dash.ref.dashboard_url ? ' openable' : ''),
                'data-panel': dash.key,
                'data-interactive': '1',
                transform: `translate(0,${y})`,
            });
            if (i % 2 === 1) {
                row.appendChild(el('rect', {
                    class: 'rel-col-bg', x: 4, y: 0, width: DU_DASH_W - 8, height: DU_ROW_H, rx: 3,
                }));
            }
            row.appendChild(el('rect', {
                class: 'rel-col-hit', x: 4, y: 0, width: DU_DASH_W - 8, height: DU_ROW_H, rx: 3,
            }));

            const tag = dashboardTag(dash);
            const nameW = DU_DASH_W - 30 - tag.text.length * 5.6;
            const name = el('text', { class: 'du-panel-name', x: 14, y: 14 },
                truncateExpression(dash.ref.dashboard_title || dash.key,
                    Math.max(8, Math.floor(nameW / 6.2))));
            name.appendChild(el('title', null, dashboardTooltip(dash)));
            row.appendChild(name);

            const label = el('text', {
                class: `du-panel-tag ${tag.kind}`,
                x: DU_DASH_W - 12, y: 14, 'text-anchor': 'end',
            }, tag.text);
            label.appendChild(el('title', null, tag.title));
            row.appendChild(label);
            g.appendChild(row);

            return {
                key: `dash::${dash.key}`,
                el: row,
                columns: dash.columns,
                tableLevel: dash.columns.size === 0,
                openRef: { ...dash.ref, panel_id: 0 },
                cy: y + DU_ROW_H / 2,
            };
        });

        // The cap has to announce itself. A silently truncated list is a wrong
        // answer to "which dashboards read this table".
        if (hidden > 0) {
            const y = DU_HEADER_H + dashboards.length * DU_ROW_H;
            const more = el('g', { class: 'du-more', transform: `translate(0,${y})` });
            const text = el('text', { class: 'du-more-text', x: 14, y: 14 },
                `+ ${hidden} more not shown — see Unused columns`);
            text.appendChild(el('title', null,
                `${hidden} further dashboard${hidden === 1 ? '' : 's'} read this table. `
                + 'Only the most-connected are drawn, so the picture stays legible.'));
            more.appendChild(text);
            g.appendChild(more);
        }

        return { g, w: DU_DASH_W, h, rows, headerRef: null };
    }

    // claimLines separates what a row names from what it merely keeps alive.
    // Listing the two together reads as "this panel reads 39 columns", which is
    // the misreading this whole distinction exists to prevent.
    function claimLines(columns) {
        const named = namedColumns(columns).map(([name]) => name);
        const lineage = [...columns.entries()]
            .filter(([, meta]) => !meta.precise && meta.blanketVia)
            .map(([name]) => name);
        const lines = [];
        if (named.length) lines.push(`names: ${named.join(', ')}`);
        if (lineage.length) {
            lines.push(`keeps alive through a view: ${lineage.slice(0, 12).join(', ')}`
                + (lineage.length > 12 ? `, +${lineage.length - 12} more` : ''));
        }
        return lines;
    }

    // blanketTag explains a row whose line comes off the table header: either
    // nothing in its query could be resolved, or its only claim on this table is
    // a materialized-view blanket, which says the table is read without saying
    // which column.
    function blanketTag(columns, nothingResolved) {
        if (nothingResolved) {
            return {
                kind: 'table-level', text: 'table-level',
                title: 'Reads the table, but no column could be resolved from the query.',
            };
        }
        const views = new Set();
        columns.forEach((meta) => {
            if (meta.blanketVia) views.add(meta.blanketVia.slice(3));
        });
        return {
            kind: 'table-level',
            text: `via mv · ${columns.size} col${columns.size === 1 ? '' : 's'}`,
            title: 'Reaches this table only through a materialized view, which keeps every '
                + `column its SELECT touches alive — ${columns.size} here. That is a claim about `
                + 'the table, not about any one column, so the line comes off the header rather '
                + `than fanning out across all of them.\n${[...views].sort().join('\n')}`,
        };
    }

    // panelTag names the one qualification worth showing on a panel row, in
    // descending order of how much it weakens the claim.
    function panelTag(panel) {
        const named = namedColumns(panel.columns);
        if (!named.length) return blanketTag(panel.columns, panel.tableLevel);

        const vias = new Set();
        let heuristic = false;
        named.forEach(([, meta]) => {
            if (meta.via && !meta.direct) vias.add(meta.via);
            if (meta.heuristic) heuristic = true;
        });
        if (vias.size) {
            // Only the kind goes on the row. A via reads
            // "mv:aggregated.airport_traffic_hourly_mv" in full, which is wider
            // than the panel title it would sit beside; the path belongs in the
            // tooltip, where it costs nothing.
            const kinds = [...new Set([...vias].map((via) => via.split(':')[0]))].sort();
            return {
                kind: 'derived', text: `via ${kinds.join('/')}`,
                title: 'Reached through ClickHouse lineage, not read directly:\n'
                    + [...vias].sort().join('\n'),
            };
        }
        if (heuristic) {
            return {
                kind: 'heuristic', text: 'heuristic',
                title: 'Attributed by matching names, not by parsing. Cannot license an unused verdict.',
            };
        }
        return null;
    }

    function panelTooltip(panel) {
        const lines = [panel.ref.panel_title || `panel ${panel.ref.panel_id}`];
        lines.push(...claimLines(panel.columns));
        if (panel.ref.snippet) lines.push(panel.ref.snippet);
        lines.push(panel.ref.dashboard_url
            ? 'click to trace · double-click to open in Grafana'
            : 'click to trace');
        return lines.join('\n');
    }

    // dashboardTag summarises one dashboard row: how many panels, and the
    // weakest qualification across them.
    function dashboardTag(dash) {
        const panels = dash.panels.length;
        const count = `${panels}p`;
        const named = namedColumns(dash.columns);
        if (!named.length) {
            const tag = blanketTag(dash.columns, dash.columns.size === 0);
            return { kind: tag.kind, text: `${count} · ${tag.text}`, title: tag.title };
        }

        const vias = new Set();
        let heuristic = false;
        named.forEach(([, meta]) => {
            if (meta.via && !meta.direct) vias.add(meta.via);
            if (meta.heuristic) heuristic = true;
        });
        const cols = `${named.length} col${named.length === 1 ? '' : 's'}`;
        if (vias.size) {
            const kinds = [...new Set([...vias].map((via) => via.split(':')[0]))].sort();
            return {
                kind: 'derived', text: `${count} · via ${kinds.join('/')}`,
                title: `${cols} across ${panels} panel${panels === 1 ? '' : 's'}, reached through `
                    + `ClickHouse lineage:\n${[...vias].sort().join('\n')}`,
            };
        }
        return {
            kind: heuristic ? 'heuristic' : '',
            text: heuristic ? `${count} · heuristic` : `${count} · ${cols}`,
            title: heuristic
                ? `${cols} across ${panels} panel${panels === 1 ? '' : 's'}, attributed by matching `
                    + 'names rather than by parsing.'
                : `${cols} across ${panels} panel${panels === 1 ? '' : 's'}.`,
        };
    }

    function dashboardTooltip(dash) {
        const lines = [dash.ref.dashboard_title || dash.key];
        const titles = dash.panels.map((panel) => panel.ref.panel_title).filter(Boolean);
        if (titles.length) {
            lines.push(`panels: ${titles.slice(0, 8).join(', ')}`
                + (titles.length > 8 ? `, +${titles.length - 8} more` : ''));
        }
        lines.push(...claimLines(dash.columns));
        lines.push(dash.ref.dashboard_url
            ? 'click to trace · double-click to open in Grafana'
            : 'click to trace');
        return lines.join('\n');
    }

    function renderDashboardUsage(container, usage, opts) {
        opts = opts || {};
        container.innerHTML = '';

        if (!usage || !usage.table) {
            container.appendChild(emptyState('Select a table to see the dashboards that read it.'));
            return null;
        }

        const dashboards = groupDashboards(usage);
        if (!dashboards.length) {
            container.appendChild(emptyState(
                `No Grafana dashboard was found reading ${usage.database}.${usage.table}. `
                + 'That is not evidence its columns are dead — only that nothing scanned reads them.',
            ));
            return null;
        }

        const totalPanels = dashboards.reduce((sum, dash) => sum + dash.panels.length, 0);
        const totalRefs = dashboards.reduce((sum, dash) => sum + dash.panels.reduce(
            (n, panel) => n + Math.max(1, panel.columns.size), 0), 0);
        // Panel mode is opt-in above a handful of panels. Drawn in full, a table
        // read by 89 panels produces a card stack four times taller than the
        // pane, which the viewBox then scales into illegibility.
        const panelMode = opts.panelMode != null
            ? Boolean(opts.panelMode)
            : totalPanels <= DU_AUTO_PANEL_LIMIT;

        // Which columns a query actually names, and which are only held alive by
        // a view. The first set is what gets a line and what "read only" keeps;
        // the second is counted and stated, because those columns are genuinely
        // in use and the reader must not conclude otherwise from the absence of
        // a line to them.
        const connected = new Set();
        const lineageOnly = new Set();
        dashboards.forEach((dash) => dash.panels.forEach(
            (panel) => panel.columns.forEach((meta, name) => {
                if (meta.precise) connected.add(name);
                else if (meta.blanketVia) lineageOnly.add(name);
            }),
        ));
        lineageOnly.forEach((name) => {
            if (connected.has(name)) lineageOnly.delete(name);
        });

        const allColumns = usage.columns || [];
        const columns = opts.hideUnconnected
            ? allColumns.filter((column) => connected.has(column.column))
            : allColumns;

        const table = buildUsageTableCard(usage, columns, opts.engineType, connected);
        const columnIndex = new Map(columns.map((column, i) => [column.column, i]));

        // Keep the most-connected dashboards when there are more than fit. Which
        // ones were dropped is stated on the card, never swallowed.
        const ranked = [...dashboards].sort((a, b) => b.columns.size - a.columns.size
            || b.panels.length - a.panels.length);
        const shown = ranked.slice(0, DU_MAX_ROWS);
        const hidden = ranked.length - shown.length;

        let cards;
        if (panelMode) {
            cards = shown.map((dash) => ({ dash, card: buildDashboardCard(dash) }));
        } else {
            // One card holding every dashboard as a row, so ordering happens
            // inside it rather than between cards.
            const ordered = orderByBarycenter(shown, columnIndex);
            cards = [{ dash: null, card: buildDashboardListCard(ordered, hidden) }];
        }

        if (panelMode) {
            const ordered = orderByBarycenter(cards.map((entry) => entry.dash), columnIndex);
            const rank = new Map(ordered.map((dash, i) => [dash.key, i]));
            cards.sort((a, b) => rank.get(a.dash.key) - rank.get(b.dash.key));
        }

        const stackH = cards.reduce((sum, entry) => sum + entry.card.h, 0)
            + DU_STACK_GAP * Math.max(0, cards.length - 1);
        const height = Math.max(table.h, stackH) + DU_MARGIN * 2;
        const width = DU_MARGIN * 2 + DU_TABLE_W + DU_RANK_GAP + DU_DASH_W;

        const svg = el('svg', {
            xmlns: SVG_NS, class: 'flow-svg dashboards', width: '100%', height: '100%',
        });
        const defs = el('defs');
        defs.appendChild(arrowMarker('du-arrow', 'var(--grafana-fg)'));
        svg.appendChild(defs);
        const viewport = el('g', { class: 'viewport' });
        svg.appendChild(viewport);

        const edgesG = el('g', { class: 'du-edges' });
        const nodesG = el('g', { class: 'du-nodes' });
        viewport.appendChild(edgesG);
        viewport.appendChild(nodesG);

        // Place the two ranks, each vertically centred against the taller one.
        const tableX = DU_MARGIN;
        const tableY = DU_MARGIN + Math.max(0, (stackH - table.h) / 2);
        table.g.setAttribute('transform', `translate(${tableX},${tableY})`);
        nodesG.appendChild(table.g);

        const dashX = DU_MARGIN + DU_TABLE_W + DU_RANK_GAP;
        let cursorY = DU_MARGIN + Math.max(0, (table.h - stackH) / 2);
        const placedRows = [];
        cards.forEach((entry) => {
            entry.card.g.setAttribute('transform', `translate(${dashX},${cursorY})`);
            nodesG.appendChild(entry.card.g);
            entry.card.rows.forEach((row) => {
                placedRows.push({ ...row, x: dashX, y: cursorY + row.cy });
            });
            cursorY += entry.card.h + DU_STACK_GAP;
        });

        // ── Edges, and the focus index that lights them up ──
        const focusIndex = new Map();
        function focusEntry(key, row) {
            if (!focusIndex.has(key)) {
                focusIndex.set(key, { row, edges: [], partners: new Set() });
            }
            return focusIndex.get(key);
        }

        function link(fromKey, fromRow, toKey, toRow, path) {
            const a = focusEntry(fromKey, fromRow);
            const b = focusEntry(toKey, toRow);
            a.edges.push(path);
            b.edges.push(path);
            a.partners.add(toKey);
            b.partners.add(fromKey);
        }

        let edgeCount = 0;
        placedRows.forEach((row) => {
            // A row with no column it can be said to name hangs off the table
            // header: it reads the table, just not a column we can point at.
            // Lineage blankets land here rather than fanning out across every
            // column of the table — that is what they actually claim.
            const targets = namedColumns(row.columns).filter(([name]) => columnIndex.has(name));
            if (!targets.length) {
                const path = edgePath(
                    tableX + DU_TABLE_W, tableY + DU_HEADER_H / 2, row.x, row.y,
                    'du-edge table-level',
                );
                edgesG.appendChild(path);
                edgeCount++;
                link('table::header', null, row.key, row.el, path);
                return;
            }

            targets.forEach(([name, meta]) => {
                const columnRow = table.g.querySelector(
                    `g.rel-col[data-column="${cssAttr(name)}"]`,
                );
                const path = edgePath(
                    tableX + DU_TABLE_W, tableY + table.rowY(columnIndex.get(name)),
                    row.x, row.y,
                    'du-edge' + (meta.direct ? '' : ' derived')
                        + (meta.heuristic ? ' heuristic' : ''),
                );
                edgesG.appendChild(path);
                edgeCount++;
                link(`col::${name}`, columnRow, row.key, row.el, path);
            });
        });

        // Past a certain density every line is on top of every other one, and
        // the picture only becomes readable once something is focused. Fading
        // the resting state is what makes the focus legible.
        if (edgeCount > DU_DENSE_EDGES) svg.classList.add('dense');

        // Rows with no edge at all still take a focus entry, so clicking an
        // unused column reads as "nothing" rather than as a dead control.
        columns.forEach((column) => {
            focusEntry(`col::${column.column}`,
                table.g.querySelector(`g.rel-col[data-column="${cssAttr(column.column)}"]`));
        });

        let focusKey = null;
        function applyFocus(nextKey) {
            svg.querySelectorAll('.is-focus, .is-related').forEach((node) => {
                node.classList.remove('is-focus', 'is-related');
            });
            svg.classList.remove('has-focus');
            focusKey = nextKey && focusIndex.has(nextKey) ? nextKey : null;
            if (!focusKey) return;

            svg.classList.add('has-focus');
            const entry = focusIndex.get(focusKey);
            if (entry.row) entry.row.classList.add('is-focus');
            entry.edges.forEach((path) => path.classList.add('is-related'));
            entry.partners.forEach((key) => {
                const partner = focusIndex.get(key);
                if (partner && partner.row) partner.row.classList.add('is-related');
            });
        }

        focusIndex.forEach((entry, key) => {
            if (!entry.row) return;
            entry.row.addEventListener('click', (ev) => {
                ev.stopPropagation();
                applyFocus(focusKey === key ? null : key);
            });
        });

        if (opts.onOpen) {
            cards.forEach((entry) => {
                const header = entry.card.headerRef && entry.card.headerRef.dashboard_url
                    ? entry.card.g.querySelector('.du-dash-header')
                    : null;
                if (header) {
                    header.style.cursor = 'pointer';
                    header.addEventListener('click', (ev) => {
                        ev.stopPropagation();
                        opts.onOpen({ ...entry.card.headerRef, panel_id: 0 });
                    });
                }
                entry.card.rows.forEach((row) => {
                    if (!row.openRef || !row.openRef.dashboard_url) return;
                    row.el.addEventListener('dblclick', (ev) => {
                        ev.stopPropagation();
                        opts.onOpen(row.openRef);
                    });
                });
            });
        }

        svg.addEventListener('click', (ev) => {
            if (ev.target.closest('.rel-col, .du-panel')) return;
            applyFocus(null);
        });

        const escHandler = (ev) => {
            if (ev.key === 'Escape' && focusKey) {
                ev.preventDefault();
                applyFocus(null);
            }
        };
        document.addEventListener('keydown', escHandler);
        const cleanupObs = new MutationObserver(() => {
            if (!container.contains(svg)) {
                document.removeEventListener('keydown', escHandler);
                cleanupObs.disconnect();
            }
        });
        cleanupObs.observe(container, { childList: true });

        svg.setAttribute('viewBox', `0 0 ${width} ${Math.max(height, 200)}`);
        svg.setAttribute('preserveAspectRatio', 'xMidYMin meet');
        container.appendChild(svg);

        const panzoom = attachPanZoom(svg, viewport);
        return {
            svg,
            panzoom,
            width,
            height,
            // The caller states these in words above the diagram: at this scale
            // the picture alone cannot be read off precisely, and a reader who
            // cannot count the lines still needs the totals.
            stats: {
                dashboards: dashboards.length,
                panels: totalPanels,
                references: totalRefs,
                columnsNamed: connected.size,
                columnsLineage: lineageOnly.size,
                columnsTotal: allColumns.length,
                hidden,
                panelMode,
                autoPanelMode: opts.panelMode == null,
            },
        };
    }

    // orderByBarycenter sorts dashboards by the average column row they connect
    // to, so lines run roughly parallel instead of crossing the whole picture. A
    // dashboard reading nothing resolvable sorts to the top, next to the table
    // header its line comes from.
    function orderByBarycenter(dashboards, columnIndex) {
        return [...dashboards].map((dash) => {
            const rows = [];
            dash.panels.forEach((panel) => panel.columns.forEach((_, name) => {
                if (columnIndex.has(name)) rows.push(columnIndex.get(name));
            }));
            return {
                dash,
                order: rows.length ? rows.reduce((sum, row) => sum + row, 0) / rows.length : -1,
            };
        }).sort((a, b) => a.order - b.order).map((entry) => entry.dash);
    }

    // edgePath draws one horizontal S-curve between two anchors.
    function edgePath(x1, y1, x2, y2, className) {
        const mx = (x1 + x2) / 2;
        return el('path', {
            class: className,
            d: `M ${x1},${y1} C ${mx},${y1} ${mx},${y2} ${x2},${y2}`,
            'marker-end': 'url(#du-arrow)',
        });
    }


    // ─── Misc ──────────────────────────────────────────────────────────
    function arrowMarker(id, fillColor) {
        const marker = el('marker', {
            id,
            viewBox: '0 0 10 10',
            refX: 9,
            refY: 5,
            markerWidth: 6,
            markerHeight: 6,
            orient: 'auto-start-reverse',
        });
        marker.appendChild(el('path', {
            d: 'M0,0 L10,5 L0,10 z',
            fill: fillColor,
            stroke: 'none',
        }));
        return marker;
    }

    function emptyState(message) {
        const wrap = document.createElement('div');
        wrap.className = 'diagram-empty';
        wrap.textContent = message;
        return wrap;
    }

    function truncateExpression(s, maxChars) {
        if (!s) return '';
        if (s.length <= maxChars) return s;
        return s.slice(0, Math.max(8, maxChars - 1)) + '…';
    }

    // Export
    global.SchemaDiagram = {
        renderDataFlow,
        renderRelationships,
        renderDashboardUsage,
    };

})(typeof window !== 'undefined' ? window : globalThis);
