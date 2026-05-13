/* Dagre-laid-out SVG renderer for ClickHouse Schema Flow Visualizer.
 *
 * Exports:
 *   renderDataFlow(container, graph, { onNodeClick })
 *   renderRelationships(container, graph, { onTableClick })
 *
 * graph payloads come from /api/dataflow/:db/:table and
 * /api/relationships/:db/:table — see models/graph.go for the shape.
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
            row.appendChild(el('text', { class: 'rel-col-name', x: 14, y: 14 }, col.name));
            row.appendChild(el('text', {
                class: 'rel-col-type', x: TABLE_W - 12, y: 14, 'text-anchor': 'end',
            }, col.type));
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
    };

})(typeof window !== 'undefined' ? window : globalThis);
